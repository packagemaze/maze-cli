package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/output"
	"github.com/packagemaze/maze-cli/internal/version"
)

func DefaultDependencies() auth.Dependencies {
	return auth.DefaultDependencies()
}

func NewRootCommand(deps auth.Dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "maze",
		Short:         "PackageMaze command line interface",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand())
	root.AddCommand(newAuthCommand(deps))
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
	return authCommand
}

func newExchangeOIDCCommand(deps auth.Dependencies) *cobra.Command {
	var config auth.Config
	command := &cobra.Command{
		Use:   "exchange-oidc",
		Short: "Exchange a CI OIDC identity token for a PackageMaze Token",
		Long: "Exchange a CI OIDC identity token for a short-lived PackageMaze Token.\n\n" +
			"The command supports GitHub Actions, GitLab CI/CD, CircleCI, and manual token input.",
		Example: "  maze auth exchange-oidc --feed your-org/your-feed --purpose install --format github-output\n" +
			"  maze auth exchange-oidc --feed your-org/your-feed --purpose publish --package your-package --format github-output",
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
	flags.StringVar(&config.Provider, "provider", "auto", "CI provider: auto, github, gitlab, circleci, or manual")
	flags.StringVar(&config.Audience, "audience", "", "OIDC audience to request or expect (default: {base-url})")
	flags.StringVar(&config.OIDCTokenEnv, "oidc-token-env", auth.DefaultOIDCTokenEnv, "Environment variable containing an OIDC token")
	flags.StringVar(&config.OIDCTokenFile, "oidc-token-file", "", "File containing an OIDC token")
	flags.BoolVar(&config.OIDCTokenStdin, "oidc-token-stdin", false, "Read the OIDC token from stdin")
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
