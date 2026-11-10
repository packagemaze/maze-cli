package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/doctor"
	"github.com/packagemaze/maze-cli/internal/endpoint"
	"github.com/packagemaze/maze-cli/internal/identity"
	"github.com/packagemaze/maze-cli/internal/output"
	publishcmd "github.com/packagemaze/maze-cli/internal/publish"
	"github.com/packagemaze/maze-cli/internal/version"
)

func DefaultDependencies() auth.Dependencies {
	return auth.DefaultDependencies()
}

func NewRootCommand(deps auth.Dependencies) *cobra.Command {
	return NewRootCommandWithPublishDependencies(deps, publishcmd.DefaultDependencies())
}

func NewRootCommandWithPublishDependencies(deps auth.Dependencies, publishDeps publishcmd.Dependencies) *cobra.Command {
	return NewRootCommandWithDependencies(deps, publishDeps, doctor.Dependencies{})
}

func NewRootCommandWithDependencies(deps auth.Dependencies, publishDeps publishcmd.Dependencies, doctorDeps doctor.Dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "maze",
		Short:         "PackageMaze command line interface",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand())
	root.AddCommand(newAuthCommand(deps))
	root.AddCommand(newPublishCommand(deps, publishDeps))
	root.AddCommand(newDoctorCommand(deps, doctorDeps))
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the maze version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.Info())
			return err
		},
	}
}

func newAuthCommand(deps auth.Dependencies) *cobra.Command {
	authCommand := &cobra.Command{
		Use:   "auth",
		Short: "Work with PackageMaze authentication",
	}
	authCommand.AddCommand(newExchangeOIDCCommand(deps))
	authCommand.AddCommand(newShowIdentityCommand(deps))
	return authCommand
}

func newExchangeOIDCCommand(deps auth.Dependencies) *cobra.Command {
	var config auth.Config
	command := &cobra.Command{
		Use:   "exchange-oidc",
		Short: "Exchange a CI OIDC identity token for a PackageMaze Token",
		Long: "Exchange a CI OIDC identity token for a short-lived PackageMaze Token.\n\n" +
			"The command supports GitHub Actions and CircleCI, and any other platform PackageMaze has an OIDC access rule for: request that platform's identity token for the PackageMaze audience and pass it with --provider oidc. Manual token input is for local development. GitLab token input is recognized for forward compatibility, but PackageMaze does not yet accept GitLab identities in production.",
		Example: "  maze auth exchange-oidc --feed your-org/your-feed --purpose install --format github-output\n" +
			"  maze auth exchange-oidc --feed your-org/your-feed --purpose publish --package your-package --format github-output" + "\n" +
			"  your-platform-token-command | maze auth exchange-oidc --provider oidc --oidc-token-stdin --feed your-org/your-feed --purpose install",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runDeps := deps
			if runDeps.Env == nil {
				runDeps.Env = auth.DefaultDependencies().Env
			}
			runDeps.Stdin = cmd.InOrStdin()
			result, resolved, err := auth.Exchange(cmd.Context(), config, runDeps)
			if err != nil {
				return err
			}
			if resolved.Verbose {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "PackageMaze OIDC exchange provider: %s\n", resolved.ProviderValue)
			}
			githubOutputPath := ""
			if value, ok := runDeps.Env("GITHUB_OUTPUT"); ok {
				githubOutputPath = value
			}
			return output.Write(result, output.WriteConfig{
				Format:           resolved.FormatValue,
				OutputName:       resolved.OutputName,
				GitHubOutputPath: githubOutputPath,
				Stdout:           cmd.OutOrStdout(),
				Stderr:           cmd.ErrOrStderr(),
			})
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.BaseURL, "base-url", "", "PackageMaze API Domain base URL (default: MAZE_BASE_URL, else https://api.packagemaze.com)")
	flags.StringVar(&config.APIURL, "api-url", "", "Full PackageMaze API root URL override (default: {base-url}/v1)")
	flags.StringVar(&config.Feed, "feed", "", "PackageMaze Feed in org/feed form")
	flags.StringVar(&config.Purpose, "purpose", "", "Token request purpose: install, publish, docker-build, or test")
	flags.StringVar(&config.Package, "package", "", "Package name; required when --purpose publish")
	flags.StringVar(&config.Provider, "provider", "auto", "CI provider: auto, github, gitlab, circleci, oidc, or manual. Use oidc for any other platform PackageMaze has an OIDC access rule for, with a supplied token")
	flags.StringVar(&config.Audience, "audience", "", "OIDC audience to request or expect (default: {base-url})")
	flags.StringVar(&config.OIDCTokenEnv, "oidc-token-env", auth.DefaultOIDCTokenEnv, "Environment variable containing an OIDC token")
	flags.StringVar(&config.OIDCTokenFile, "oidc-token-file", "", "File containing an OIDC token")
	flags.BoolVar(&config.OIDCTokenStdin, "oidc-token-stdin", false, "Read the OIDC token from stdin")
	flags.StringVar(&config.SetupInvocationID, "setup-invocation-id", "", "Unverified non-secret wrapper invocation id (default: MAZE_SETUP_INVOCATION_ID)")
	flags.StringVar(&config.ClientContextJSON, "client-context-json", "", "Legacy caller-supplied, unverified client metadata JSON accepted for compatibility")
	flags.StringVar(&config.Format, "format", string(output.FormatToken), "Output format: token, json, shell, or github-output")
	flags.StringVar(&config.OutputName, "output-name", auth.DefaultOutputName, "Output name when --format github-output")
	flags.DurationVar(&config.Timeout, "timeout", 15*time.Second, "HTTP timeout")
	flags.BoolVar(&config.Verbose, "verbose", false, "Print non-secret diagnostics to stderr")
	flags.BoolVar(&config.NoColor, "no-color", false, "Disable color output")
	flags.BoolVar(&config.JSONAlias, "json", false, "Alias for --format json")
	flags.BoolVar(&config.AllowInsecureLocalhost, "allow-insecure-localhost", false, "Allow http URLs only for localhost development")
	flags.BoolVar(&config.AllowGitHubOutputOutside, "allow-github-output-outside-actions", false, "Allow github-output format outside GitHub Actions for tests")
	_ = flags.MarkHidden("allow-github-output-outside-actions")
	_ = command.MarkFlagRequired("feed")
	_ = command.MarkFlagRequired("purpose")
	return command
}

func newShowIdentityCommand(deps auth.Dependencies) *cobra.Command {
	var config auth.Config
	var format string
	var jsonAlias bool
	command := &cobra.Command{
		Use:   "show-identity",
		Short: "Print the claims in this job's identity token without signing in",
		Long: "Obtain this job's identity token exactly as exchange-oidc does, decode it locally, and print the claims it carries, so a platform can be set up in PackageMaze without failing a run first.\n\n" +
			"The token is not verified and nothing is sent to PackageMaze. Every claim value the token carries is printed to the job log; the token itself and its signature are never printed.",
		Example: "  maze auth show-identity\n" +
			"  your-platform-token-command | maze auth show-identity --provider oidc --oidc-token-stdin\n" +
			"  maze auth show-identity --format json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			formatValue, err := parseIdentityFormat(format, jsonAlias)
			if err != nil {
				return err
			}
			runDeps := deps
			if runDeps.Env == nil {
				runDeps.Env = auth.DefaultDependencies().Env
			}
			runDeps.Stdin = cmd.InOrStdin()
			resolved, err := auth.ResolveIdentitySource(config, runDeps)
			if err != nil {
				return err
			}
			rawToken, err := auth.AcquireIdentityToken(cmd.Context(), resolved, runDeps)
			if err != nil {
				return err
			}
			token, err := identity.Decode(rawToken)
			if err != nil {
				return err
			}
			if formatValue == "json" {
				return identity.WriteJSON(cmd.OutOrStdout(), token)
			}
			return identity.WriteText(cmd.OutOrStdout(), token, time.Now())
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.BaseURL, "base-url", "", "PackageMaze API Domain base URL; sets the default audience (default: MAZE_BASE_URL, else https://api.packagemaze.com)")
	flags.StringVar(&config.Provider, "provider", "auto", "CI provider: auto, github, gitlab, circleci, oidc, or manual. Use oidc for any other platform, with a supplied token")
	flags.StringVar(&config.Audience, "audience", "", "OIDC audience to request (default: {base-url})")
	flags.StringVar(&config.OIDCTokenEnv, "oidc-token-env", auth.DefaultOIDCTokenEnv, "Environment variable containing an OIDC token")
	flags.StringVar(&config.OIDCTokenFile, "oidc-token-file", "", "File containing an OIDC token")
	flags.BoolVar(&config.OIDCTokenStdin, "oidc-token-stdin", false, "Read the OIDC token from stdin")
	flags.StringVar(&format, "format", "text", "Output format: text or json")
	flags.BoolVar(&jsonAlias, "json", false, "Alias for --format json")
	flags.BoolVar(&config.AllowInsecureLocalhost, "allow-insecure-localhost", false, "Allow http URLs only for localhost development")
	return command
}

func parseIdentityFormat(format string, jsonAlias bool) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if jsonAlias {
		if normalized != "" && normalized != "text" {
			return "", fmt.Errorf("--json cannot be combined with --format")
		}
		return "json", nil
	}
	switch normalized {
	case "", "text":
		return "text", nil
	case "json":
		return "json", nil
	default:
		return "", fmt.Errorf("format must be text or json")
	}
}

func newPublishCommand(authDeps auth.Dependencies, publishDeps publishcmd.Dependencies) *cobra.Command {
	config := publishcmd.Config{Wait: true}
	command := &cobra.Command{
		Use:   "publish [paths...] --feed <organization/feed>",
		Short: "Publish artifacts to a PackageMaze Feed",
		Long:  "Publish artifacts by asking PackageMaze for a backend-steered plan, uploading bytes, and waiting for Publish Finalization.",
		Example: "  maze publish dist/* --feed your-org/your-feed\n" +
			"  maze publish ./package-1.0.0.tgz --feed your-org/your-feed --json",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runDeps := publishDeps
			if runDeps.Env == nil {
				runDeps.Env = authDeps.Env
			}
			if runDeps.Env == nil {
				runDeps.Env = auth.DefaultDependencies().Env
			}
			if runDeps.Stdin == nil {
				runDeps.Stdin = cmd.InOrStdin()
			}
			if runDeps.HTTPClient == nil {
				runDeps.HTTPClient = authDeps.HTTPClient
			}
			result, resolved, err := publishcmd.Run(cmd.Context(), config, args, runDeps, cmd.ErrOrStderr())
			if err != nil {
				if result.PublishSessionID != "" && resolved.FormatValue == publishcmd.FormatJSON {
					_ = publishcmd.Write(result, resolved.FormatValue, cmd.OutOrStdout())
				}
				return err
			}
			return publishcmd.Write(result, resolved.FormatValue, cmd.OutOrStdout())
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.PackageClientURL, "package-client-url", "", endpoint.PackageClientURLFlagHelp)
	flags.StringVar(&config.Feed, "feed", "", "PackageMaze Feed in org/feed form")
	flags.StringVar(&config.TokenEnv, "token-env", publishcmd.DefaultTokenEnv, "Environment variable containing a PackageMaze Token")
	flags.StringVar(&config.TokenFile, "token-file", "", "File containing a PackageMaze Token")
	flags.BoolVar(&config.StdinToken, "token-stdin", false, "Read the PackageMaze Token from stdin")
	flags.StringVar(&config.PackageHint, "package", "", "Optional package name hint for backend planning")
	flags.StringVar(&config.VersionHint, "version", "", "Optional package version hint for backend planning")
	flags.BoolVar(&config.Wait, "wait", true, "Wait until PackageMaze reports ready or error")
	flags.StringVar(&config.Format, "format", string(publishcmd.FormatText), "Output format: text or json")
	flags.BoolVar(&config.JSONAlias, "json", false, "Alias for --format json")
	flags.DurationVar(&config.Timeout, "timeout", 30*time.Second, "HTTP timeout")
	flags.BoolVar(&config.Verbose, "verbose", false, "Print non-secret diagnostics to stderr")
	flags.BoolVar(&config.AllowInsecureLocalhost, "allow-insecure-localhost", false, "Allow http URLs only for localhost development")
	_ = command.MarkFlagRequired("feed")
	return command
}

func newDoctorCommand(authDeps auth.Dependencies, doctorDeps doctor.Dependencies) *cobra.Command {
	var config doctor.Config
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose a repository's PackageMaze setup without changing it",
		Long: "Read the repository's package-client configuration (.npmrc, .yarnrc.yml, bunfig.toml, pnpm-workspace.yaml, package.json, requirements files, pip.conf, pyproject.toml, uv.toml, Pipfile, and GitHub workflows using packagemaze/setup-maze), " +
			"say which PackageMaze Feed it points at or that none does, and check each setting's shape against the Feed's URLs.\n\n" +
			"With MAZE_TOKEN set, make one read-only request per Feed to prove the Token is accepted for reads. " +
			"The command writes nothing, prints no Token value, and contacts only the PackageMaze Package Client Domain. It exits non-zero only when a check failed.",
		Example: "  maze doctor\n" +
			"  maze doctor --format json\n" +
			"  maze doctor --dir packages/app --feed your-org/your-feed",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runDeps := doctorDeps
			if runDeps.Env == nil {
				runDeps.Env = authDeps.Env
			}
			if runDeps.HTTPClient == nil {
				runDeps.HTTPClient = authDeps.HTTPClient
			}
			report, resolved, err := doctor.Run(cmd.Context(), config, runDeps)
			if err != nil {
				return err
			}
			if err := doctor.Write(report, resolved.FormatValue, cmd.OutOrStdout(), resolved.Token); err != nil {
				return err
			}
			return report.FailureError()
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.Dir, "dir", ".", "Repository directory to diagnose; its parents up to the repository root are read too")
	flags.StringVar(&config.Feed, "feed", "", "Diagnose this PackageMaze Feed (org/feed) even when no file names it")
	flags.StringVar(&config.Format, "format", string(doctor.FormatMarkdown), "Output format: markdown or json")
	flags.BoolVar(&config.JSONAlias, "json", false, "Alias for --format json")
	flags.StringVar(&config.TokenEnv, "token-env", endpoint.DefaultTokenEnv, "Environment variable holding the PackageMaze Token to test; its value is never printed")
	flags.StringVar(&config.PackageClientURL, "package-client-url", "", endpoint.PackageClientURLFlagHelp)
	flags.BoolVar(&config.Offline, "offline", false, "Skip the live Token check; read files only")
	flags.DurationVar(&config.Timeout, "timeout", 15*time.Second, "HTTP timeout for the live Token check")
	flags.BoolVar(&config.AllowInsecureLocalhost, "allow-insecure-localhost", false, "Allow http URLs only for localhost development")
	return command
}
