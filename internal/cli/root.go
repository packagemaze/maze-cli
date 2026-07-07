package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/output"
	publishcmd "github.com/packagemaze/maze-cli/internal/publish"
	"github.com/packagemaze/maze-cli/internal/version"
)

func DefaultDependencies() auth.Dependencies {
	return auth.DefaultDependencies()
}

func NewRootCommand(deps auth.Dependencies) *cobra.Command {
	return NewRootCommandWithPublishDependencies(deps, publishcmd.Dependencies{})
}

func NewRootCommandWithPublishDependencies(deps auth.Dependencies, publishDeps publishcmd.Dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "maze",
		Short:         "PackageMaze command line interface",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newVersionCommand())
	root.AddCommand(newAuthCommand(deps))
	root.AddCommand(newPublishCommand(deps, publishDeps))
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
	flags.StringVar(&config.ClientContextJSON, "client-context-json", "", "Bounded non-secret client context JSON object to attach to the exchange")
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
	flags.StringVar(&config.PackageClientURL, "package-client-url", "", "PackageMaze Package Client Domain base URL (default: MAZE_PACKAGE_CLIENT_URL, else https://pkg.packagemaze.com)")
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
