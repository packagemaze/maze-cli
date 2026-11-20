package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/endpoint"
	"github.com/packagemaze/maze-cli/internal/login"
)

func newLoginCommand(deps auth.Dependencies, loginDeps login.Dependencies) *cobra.Command {
	var config login.Config
	var baseURL string
	command := &cobra.Command{
		Use:   "login",
		Short: "Get a PackageMaze Token for this machine by approving it in your browser",
		Long: "Ask PackageMaze for a Token, open the approval page in your browser, and wait while you approve it there. " +
			"The Token is saved to a profile in your user config directory and verified; it is never printed.\n\n" +
			"For a person's machine only. CI jobs and sandboxes sign in with maze auth exchange-oidc instead.",
		Example: "  maze auth login --org your-org\n" +
			"  maze auth login --org your-org --feed npm --feed pypi --purpose publish --profile release",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env := deps.Env
			if env == nil {
				env = auth.DefaultDependencies().Env
			}
			if config.APIURL == "" {
				base, _ := env("MAZE_BASE_URL")
				config.APIURL = strings.TrimRight(firstSet(baseURL, base, auth.DefaultBaseURL), "/") + "/v1"
			}
			if err := endpoint.ValidateURL("api-url", config.APIURL, config.AllowInsecureLocalhost); err != nil {
				return err
			}
			config.PackageClientURL = endpoint.ResolvePackageClientURL(config.PackageClientURL, env)
			if err := endpoint.ValidateURL("package-client-url", config.PackageClientURL, config.AllowInsecureLocalhost); err != nil {
				return err
			}
			runDeps := loginDeps
			runDeps.Stderr = cmd.ErrOrStderr()
			if runDeps.HTTPClient == nil {
				runDeps.HTTPClient = deps.HTTPClient
			}
			result, err := login.Run(cmd.Context(), config, runDeps)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Token for Organization %s saved to profile %s and verified.\n", organizationLabel(result.Organization), result.Profile)
			return err
		},
	}
	flags := command.Flags()
	flags.StringVar(&config.Organization, "org", "", "PackageMaze Organization slug")
	flags.StringArrayVar(&config.Feeds, "feed", nil, "Feed name, or `org/feed`; repeat for several. Default: every Feed of the Organization")
	flags.StringVar(&config.Purpose, "purpose", "install", "install (Read) or publish (Read and publish)")
	flags.StringVar(&config.Profile, "profile", "", "Profile to save the Token to (default: the Organization slug)")
	flags.StringVar(&config.TokenName, "token-name", "", "Token name shown in PackageMaze (default: maze CLI on <hostname>)")
	flags.BoolVar(&config.NoBrowser, "no-browser", false, "Print the approval URL without opening a browser")
	flags.StringVar(&baseURL, "base-url", "", "PackageMaze API Domain base URL (default: MAZE_BASE_URL, else https://api.packagemaze.com)")
	flags.StringVar(&config.APIURL, "api-url", "", "Full PackageMaze API root URL override (default: {base-url}/v1)")
	flags.StringVar(&config.PackageClientURL, "package-client-url", "", endpoint.PackageClientURLFlagHelp+"; saved with the profile so maze publish and maze doctor use it only there")
	flags.BoolVar(&config.AllowInsecureLocalhost, "allow-insecure-localhost", false, "Allow http URLs only for localhost development")
	_ = command.MarkFlagRequired("org")
	return command
}

func organizationLabel(organization login.Organization) string {
	if organization.Name == "" || organization.Name == organization.Slug {
		return organization.Slug
	}
	return fmt.Sprintf("%s (%s)", organization.Name, organization.Slug)
}

func firstSet(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
