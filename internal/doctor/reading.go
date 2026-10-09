package doctor

import "fmt"

// reading is what one configuration file or environment contributes: the
// registry declarations it makes and the checks that follow from its shape.
type reading struct {
	declarations []Declaration
	checks       []Check
}

func (r *reading) declare(declaration Declaration) {
	r.declarations = append(r.declarations, declaration)
}

func (r *reading) add(checks ...Check) {
	r.checks = append(r.checks, checks...)
}

func (r *reading) merge(other reading) {
	r.declarations = append(r.declarations, other.declarations...)
	r.checks = append(r.checks, other.checks...)
}

func wrongRegistryCheck(declaration Declaration, expected string, why string) Check {
	return check(
		"client_config_wrong_registry",
		StatusFail,
		fmt.Sprintf("%s %s is %s; %s.", declaration.Source, declaration.Setting, declaration.URL, why),
		fmt.Sprintf("Set %s to %s.", declaration.Setting, expected),
	).at(declaration.Source).forFeed(*declaration.Feed)
}

func literalTokenCheck(source string, setting string) Check {
	return check(
		"client_config_contains_literal_token",
		StatusFail,
		fmt.Sprintf("%s commits a literal PackageMaze Token Secret in %s.", source, setting),
		"Rotate that Token now (its Secret is in version control), then keep credentials in user-level client config, the environment (MAZE_TOKEN), or CI secrets. Reference an environment variable instead.",
	).at(source)
}

func committedCredentialCheck(source string, setting string) Check {
	return check(
		"client_config_contains_credentials",
		StatusWarning,
		fmt.Sprintf("%s commits a credential in %s.", source, setting),
		"Move credentials out of the repository: user-level client config, the environment (MAZE_TOKEN), or CI secrets.",
	).at(source)
}

func credentialPlaceholderCheck(source string, setting string) Check {
	return check(
		"client_config_credential_placeholder",
		StatusWarning,
		fmt.Sprintf("%s sets %s from an environment variable.", source, setting),
		"The committed file should carry the Feed Base URL and nothing else; keep credentials in user-level client config or CI. See "+DocsSetUpNpmAndPnpm,
	).at(source)
}

func pnpmPlaceholderCheck(source string, setting string) Check {
	return check(
		"pnpm_ignores_committed_credential_placeholder",
		StatusWarning,
		fmt.Sprintf("%s sets %s from an environment variable, which pnpm 10.34.2+ and 11.5.3+ ignore in repository-controlled config.", source, setting),
		"Supply pnpm's credential from user-level .npmrc, pnpm env-config for one command, or a CI temp npm config selected with NPM_CONFIG_USERCONFIG. Keep the committed file registry-only. See "+DocsSetUpNpmAndPnpm,
	).at(source)
}

func readyCheck(declaration Declaration, summary string) Check {
	return check(
		"client_config_ready",
		StatusPass,
		summary,
		"No change needed; keep Token Secrets outside committed configuration.",
	).at(declaration.Source).forFeed(*declaration.Feed)
}

func bypassCheck(source string, summary string, remediation string) Check {
	return check("index_bypasses_packagemaze", StatusFail, summary, remediation).at(source)
}

func incompleteCheck(declaration Declaration, summary string, remediation string) Check {
	return check("client_config_incomplete", StatusWarning, summary, remediation).at(declaration.Source).forFeed(*declaration.Feed)
}
