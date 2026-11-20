package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/clientconfig"
	"github.com/packagemaze/maze-cli/internal/clientconfig/npm"
	"github.com/packagemaze/maze-cli/internal/clientconfig/pypi"
	"github.com/packagemaze/maze-cli/internal/output"
	"github.com/packagemaze/maze-cli/internal/subprocess"
)

// packageClientContributors is the composition root's choice of package-client
// configuration for each Artifact Protocol PackageMaze serves.
var packageClientContributors = clientconfig.Registry{
	"npm":  npm.Contribute,
	"pypi": pypi.Contribute,
}

// ExitError carries the exit status of the command maze auth exec ran. The
// command has already reported its own failure, so maze prints nothing more.
type ExitError struct {
	Code int
}

func (err *ExitError) Error() string {
	return fmt.Sprintf("the command exited with status %d", err.Code)
}

type execDependencies struct {
	Filesystem clientconfig.Filesystem
	Run        subprocess.Runner
	Signals    func() (<-chan os.Signal, func())
	Environ    func() []string
}

func defaultExecDependencies() execDependencies {
	return execDependencies{
		Filesystem: clientconfig.OSFilesystem(),
		Run:        subprocess.Run,
		Signals:    subprocess.NotifySignals,
		Environ:    os.Environ,
	}
}

// outputFlags print the Token, which maze auth exec gives only to the command.
var outputFlags = []string{"format", "output-name", "json", "allow-github-output-outside-actions"}

func newAuthExecCommand(deps auth.Dependencies, execDeps execDependencies) *cobra.Command {
	var config auth.Config
	var reportedRef, reportedCommit string
	command := &cobra.Command{
		Use:   "exec [flags] -- <command> [args...]",
		Short: "Run a command with package clients signed in to PackageMaze Feeds",
		Long: "Exchange the job's identity token for a PackageMaze Token exactly as exchange-oidc does, then run the command with temporary package-client configuration for each Feed the Token was granted for, and remove it when the command ends.\n\n" +
			"npm Feeds: a private npm user config (NPM_CONFIG_USERCONFIG, read by npm and pnpm) with the first Feed as the registry and a Feed-URL-scoped credential for each, plus Corepack's registry and token variables for the first Feed (Yarn Berry reads MAZE_TOKEN from the project's .yarnrc.yml). " +
			"PyPI Feeds: PIP_INDEX_URL and UV_DEFAULT_INDEX for the first Feed, PIP_EXTRA_INDEX_URL and UV_INDEX for the others. The Token is in MAZE_TOKEN.\n\n" +
			"The Token is never printed, passed as an argument, or written to a file. maze exits with the command's exit status.",
		Example: "  maze auth exec --feed your-org/npm --purpose install -- npm ci\n" +
			"  maze auth exec --feed your-org/npm --feed your-org/pypi --purpose install -- make install\n" +
			"  your-platform-token-command | maze auth exec --provider oidc --oidc-token-stdin --feed your-org/pypi --purpose install -- uv sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseOutputFlags(cmd); err != nil {
				return err
			}
			if len(args) == 0 {
				return errors.New("maze auth exec needs a command to run after --")
			}
			if cmd.Flags().Changed("ref") {
				config.ReportedRef = &reportedRef
			}
			if cmd.Flags().Changed("commit") {
				config.ReportedCommit = &reportedCommit
			}
			runDeps := deps
			if runDeps.Env == nil {
				runDeps.Env = auth.DefaultDependencies().Env
			}
			runDeps.Stdin = cmd.InOrStdin()
			return runAuthExec(cmd, config, runDeps, execDeps, args)
		},
	}
	bindExchangeFlags(command, &config, &reportedRef, &reportedCommit)
	command.Flags().SetInterspersed(false)
	for _, name := range outputFlags {
		_ = command.Flags().MarkHidden(name)
	}
	return command
}

func refuseOutputFlags(cmd *cobra.Command) error {
	for _, name := range outputFlags {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--%s does not apply to maze auth exec: it gives the Token only to the command's package clients", name)
		}
	}
	return nil
}

func runAuthExec(cmd *cobra.Command, config auth.Config, deps auth.Dependencies, execDeps execDependencies, argv []string) error {
	result, resolved, err := auth.Exchange(cmd.Context(), config, deps)
	if err != nil {
		return err
	}
	feeds, err := grantedFeeds(result)
	if err != nil {
		return err
	}
	signals, stopSignals := execDeps.Signals()
	defer stopSignals()
	workspace, err := clientconfig.NewWorkspace(execDeps.Filesystem)
	if err != nil {
		return fmt.Errorf("create private package-client configuration: %w", err)
	}
	defer removeWorkspace(workspace, cmd.ErrOrStderr())
	contribution, err := clientconfig.Configure(feeds, result.Token, packageClientContributors, workspace)
	if err != nil {
		return err
	}
	if resolved.Verbose {
		describeExec(cmd.ErrOrStderr(), result.Provider, feeds, contribution)
	}
	code, err := execDeps.Run(subprocess.Spec{
		Path:   argv[0],
		Args:   argv[1:],
		Env:    clientconfig.Environ(execDeps.Environ(), contribution),
		Stdin:  cmd.InOrStdin(),
		Stdout: cmd.OutOrStdout(),
		Stderr: cmd.ErrOrStderr(),
	}, signals)
	if err != nil {
		return err
	}
	if code != 0 {
		return &ExitError{Code: code}
	}
	return nil
}

func removeWorkspace(workspace *clientconfig.Workspace, stderr io.Writer) {
	if err := workspace.Close(); err != nil {
		_, _ = fmt.Fprintf(stderr, "maze auth exec: could not remove %s: %v\n", workspace.Dir(), err)
	}
}

// grantedFeeds reads the Feeds the Token covers from PackageMaze's answer:
// its own fields for one Feed, one feeds entry each for several.
func grantedFeeds(result output.Result) ([]clientconfig.Feed, error) {
	if len(result.Feeds) == 0 {
		feed, err := clientconfig.NewFeed(result.Feed, result.ArtifactProtocol, result.FeedBaseURL)
		return []clientconfig.Feed{feed}, err
	}
	var entries []struct {
		Feed             string `json:"feed"`
		ArtifactProtocol string `json:"artifact_protocol"`
		FeedBaseURL      string `json:"feed_base_url"`
	}
	if err := json.Unmarshal(result.Feeds, &entries); err != nil {
		return nil, errors.New("PackageMaze's feeds list could not be read")
	}
	feeds := make([]clientconfig.Feed, 0, len(entries))
	for _, entry := range entries {
		feed, err := clientconfig.NewFeed(entry.Feed, entry.ArtifactProtocol, entry.FeedBaseURL)
		if err != nil {
			return nil, err
		}
		feeds = append(feeds, feed)
	}
	return feeds, nil
}

// describeExec names what was configured, never a value.
func describeExec(stderr io.Writer, provider string, feeds []clientconfig.Feed, contribution clientconfig.Contribution) {
	if provider == "" {
		provider = "none named"
	}
	_, _ = fmt.Fprintf(stderr, "PackageMaze OIDC exchange provider: %s\n", provider)
	for _, feed := range feeds {
		_, _ = fmt.Fprintf(stderr, "maze auth exec: %s Feed %s at %s\n", feed.ArtifactProtocol, feed.Reference, feed.BaseURL)
	}
	_, _ = fmt.Fprintf(stderr, "maze auth exec: set %s\n", strings.Join(contribution.Names(), ", "))
	if len(contribution.Clear) > 0 {
		_, _ = fmt.Fprintf(stderr, "maze auth exec: cleared %s\n", strings.Join(contribution.Clear, ", "))
	}
}
