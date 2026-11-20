package doctor

import (
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

func envValue(env ci.LookupEnv, key string) string {
	value, _ := env(key)
	return strings.TrimSpace(value)
}
