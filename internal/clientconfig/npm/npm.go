// Package npm configures npm-protocol package clients (npm, pnpm, Yarn
// Berry, and Corepack) for a command, as PackageMaze's setup instructions
// do: the first Feed is the default registry, and every Feed's URL carries
// its own credential.
package npm

import (
	"strings"

	"github.com/packagemaze/maze-cli/internal/clientconfig"
)

// Contribute writes a private npm user config, which npm and pnpm read
// through NPM_CONFIG_USERCONFIG, and sets Yarn Berry's and Corepack's
// variables, which take one registry: the first Feed.
func Contribute(request clientconfig.Request) (clientconfig.Contribution, error) {
	userConfig, err := request.Files.Write("npmrc", []byte(userConfig(request.Feeds)))
	if err != nil {
		return clientconfig.Contribution{}, err
	}
	first := request.Feeds[0]
	set := []clientconfig.Var{
		{Name: "NPM_CONFIG_USERCONFIG", Value: userConfig},
		{Name: "YARN_NPM_REGISTRY_SERVER", Value: registryURL(first)},
		{Name: "YARN_NPM_AUTH_TOKEN", Value: request.Token},
		{Name: "YARN_NPM_ALWAYS_AUTH", Value: "true"},
		{Name: "COREPACK_NPM_REGISTRY", Value: first.BaseURL.String()},
		{Name: "COREPACK_NPM_TOKEN", Value: request.Token},
	}
	if first.BaseURL.Scheme == "http" {
		set = append(set, clientconfig.Var{Name: "YARN_UNSAFE_HTTP_WHITELIST", Value: first.BaseURL.Hostname()})
	}
	// An inherited registry variable outranks every npmrc file.
	return clientconfig.Contribution{Set: set, Clear: []string{"NPM_CONFIG_REGISTRY"}}, nil
}

// userConfig holds no Token: each Feed's line references TokenEnv, which
// npm and pnpm expand when they read a user config. replace-registry-host
// stays at npm's own default, npmjs, which routes npmjs.com lockfile tarball
// URLs through the registry; PackageMaze never recommends always.
func userConfig(feeds []clientconfig.Feed) string {
	lines := []string{"registry=" + registryURL(feeds[0]), "replace-registry-host=npmjs"}
	for _, feed := range feeds {
		lines = append(lines, "//"+authPath(feed)+":_authToken=${"+clientconfig.TokenEnv+"}")
	}
	return strings.Join(lines, "\n") + "\n"
}

func registryURL(feed clientconfig.Feed) string {
	return feed.BaseURL.String() + "/"
}

// authPath scopes a credential to one Feed's URL, never to the host every
// Feed shares.
func authPath(feed clientconfig.Feed) string {
	return feed.BaseURL.Host + feed.BaseURL.EscapedPath() + "/"
}
