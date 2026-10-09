package doctor

import (
	"fmt"
	"strings"

	"github.com/packagemaze/maze-cli/internal/ci"
)

type environmentIndex struct {
	name     string
	client   string
	protocol ArtifactProtocol
	extra    bool
}

// Extra-index variables hold a space-separated list; the others hold one URL.
var environmentIndexes = []environmentIndex{
	{name: "PIP_INDEX_URL", client: "pip", protocol: ProtocolPyPI},
	{name: "PIP_EXTRA_INDEX_URL", client: "pip", protocol: ProtocolPyPI, extra: true},
	{name: "UV_DEFAULT_INDEX", client: "uv", protocol: ProtocolPyPI},
	{name: "UV_INDEX_URL", client: "uv", protocol: ProtocolPyPI},
	{name: "UV_INDEX", client: "uv", protocol: ProtocolPyPI, extra: true},
	{name: "UV_EXTRA_INDEX_URL", client: "uv", protocol: ProtocolPyPI, extra: true},
	{name: "NPM_CONFIG_REGISTRY", client: "npm", protocol: ProtocolNpm},
	{name: "npm_config_registry", client: "npm", protocol: ProtocolNpm},
}

// Environment variables whose presence means a package client already has a
// credential, even when MAZE_TOKEN is not the one carrying it.
var clientCredentialVariables = []string{
	"UV_INDEX_PACKAGEMAZE_PASSWORD",
	"UV_PUBLISH_TOKEN",
	"NODE_AUTH_TOKEN",
	"NPM_TOKEN",
	"POETRY_HTTP_BASIC_PACKAGEMAZE_PASSWORD",
	"TWINE_PASSWORD",
	"PDM_PUBLISH_PASSWORD",
}

// readEnvironment treats index variables as declarations like any file. The
// environment is where credentials belong, so a secret in a URL here is not a
// finding; it is still never printed.
func readEnvironment(env ci.LookupEnv, domain packageClientDomain) reading {
	var result reading
	var pypiIndexes []pypiIndexDeclaration
	for _, variable := range environmentIndexes {
		value := envValue(env, variable.name)
		if value == "" {
			continue
		}
		values := []string{value}
		if variable.extra {
			values = strings.Fields(value)
		}
		source := environmentSourcePrefix + variable.name
		for _, raw := range values {
			if variable.protocol == ProtocolNpm {
				result.npmRegistry(domain, source, variable.client, variable.name, raw)
				continue
			}
			pypiIndexes = append(pypiIndexes, result.pypiIndex(domain, source, variable.client, variable.name, raw, variable.extra, true))
		}
	}
	result.add(pypiIndexSetChecks("the environment", pypiIndexes)...)
	return result
}

func readTokenStatus(env ci.LookupEnv, tokenEnv string, present bool) TokenStatus {
	status := TokenStatus{Env: tokenEnv, Present: present}
	for _, name := range clientCredentialVariables {
		if envValue(env, name) != "" {
			status.ClientCredentials = append(status.ClientCredentials, name)
		}
	}
	for _, variable := range environmentIndexes {
		if variable.protocol == ProtocolPyPI && !variable.extra && urlHasCredentials(envValue(env, variable.name)) {
			status.ClientCredentials = append(status.ClientCredentials, variable.name)
		}
	}
	return status
}

func tokenChecks(status TokenStatus, feeds []FeedReport) []Check {
	if len(feeds) == 0 {
		return nil
	}
	if status.Present {
		return []Check{check(
			"token_env_present",
			StatusPass,
			fmt.Sprintf("%s is set; its value is never printed.", status.Env),
			"No action needed.",
		).at(environmentSourcePrefix + status.Env)}
	}
	if len(status.ClientCredentials) > 0 {
		variables := strings.Join(status.ClientCredentials, ", ")
		return []Check{check(
			"client_credential_present",
			StatusPass,
			fmt.Sprintf("%s carries a package-client credential; %s is not set, so maze doctor cannot test it against the Feed.", variables, status.Env),
			fmt.Sprintf("Set %s to the same Token Secret to let maze doctor verify it.", status.Env),
		).at(environmentSourcePrefix + variables)}
	}
	remediation := fmt.Sprintf(
		"Create a Token with Read access to Feed %s in PackageMaze (the Token Secret is shown once), then run: export %s=\"<Token Secret>\". Package clients read it from there (%s). Get one: %s and %s",
		feeds[0].Slug(), status.Env, tokenWiringHint(feeds[0].Protocol), DocsFixAFailingInstall, DocsAgentQuickstart,
	)
	return []Check{check(
		"token_env_missing",
		StatusFail,
		fmt.Sprintf("%s is not set and no package-client credential variable is set, so installs from Feed %s run unauthenticated and PackageMaze refuses them.", status.Env, feeds[0].Slug()),
		remediation,
	).at(environmentSourcePrefix + status.Env).forFeed(feeds[0].Feed)}
}

func tokenWiringHint(protocol ArtifactProtocol) string {
	switch protocol {
	case ProtocolNpm:
		return "npm: npm config set \"//<host>/<organization>/<feed>/:_authToken\" \"${MAZE_TOKEN}\" in user-level config; pnpm: pass it as env-config or a CI temp npm config"
	case ProtocolPyPI:
		return "pip: PIP_INDEX_URL=https://__token__:${MAZE_TOKEN}@<host>/<organization>/<feed>/simple/; uv: UV_INDEX_PACKAGEMAZE_USERNAME=__token__ and UV_INDEX_PACKAGEMAZE_PASSWORD=${MAZE_TOKEN}; Poetry: poetry config http-basic.packagemaze __token__ \"${MAZE_TOKEN}\""
	default:
		return "see the Feed's Setup Instructions for the client's credential variable"
	}
}

func envValue(env ci.LookupEnv, key string) string {
	value, _ := env(key)
	return strings.TrimSpace(value)
}
