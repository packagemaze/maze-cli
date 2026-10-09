package doctor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type npmrcEntry struct {
	key   string
	value string
}

var scopedRegistryKey = regexp.MustCompile(`^(@[^\s:=]+):registry$`)

// parseNpmrc follows npm's ini grammar: key=value per line, ; and # comments,
// quoted values, later assignments winning.
func parseNpmrc(content string) []npmrcEntry {
	var entries []npmrcEntry
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimRight(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator < 0 {
			continue
		}
		entries = append(entries, npmrcEntry{
			key:   strings.TrimSpace(line[:separator]),
			value: npmrcValue(line[separator+1:]),
		})
	}
	return entries
}

func npmrcValue(raw string) string {
	var quote byte
	var value strings.Builder
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		switch {
		case quote == 0 && (character == '#' || character == ';'):
			return strings.TrimSpace(value.String())
		case quote == 0 && (character == '"' || character == '\''):
			quote = character
		case character == quote:
			quote = 0
		default:
			value.WriteByte(character)
		}
	}
	return strings.TrimSpace(value.String())
}

func isNpmCredentialKey(key string) bool {
	lower := strings.ToLower(key)
	for _, suffix := range []string{"_authtoken", "_auth", "_password", "username", "email"} {
		if lower == suffix || strings.HasSuffix(lower, ":"+suffix) {
			return true
		}
	}
	return false
}

// npmRegistry records an install registry and, when it names a Feed, checks
// that it is exactly the Feed Base URL.
func (r *reading) npmRegistry(domain packageClientDomain, source string, client string, setting string, rawURL string) Declaration {
	declaration, rest := domain.declaration(source, client, ProtocolNpm, setting, rawURL)
	r.declare(declaration)
	if declaration.Feed != nil {
		r.add(npmRegistryShapeCheck(declaration, rest))
	}
	return declaration
}

func (r *reading) npmPublishRegistry(domain packageClientDomain, source string, client string, setting string, rawURL string) {
	declaration, rest := domain.declaration(source, client, ProtocolNpm, setting, rawURL)
	r.declare(declaration)
	if declaration.Feed != nil && rest != "/" {
		r.add(wrongRegistryCheck(declaration, declaration.Feed.BaseURL, "the publish registry must be the Feed Base URL"))
	}
}

// npmRegistryShapeCheck accepts exactly the Feed Base URL, trailing slash
// included, which is what Feed Doctor and the setup instructions require.
func npmRegistryShapeCheck(declaration Declaration, rest string) Check {
	feed := *declaration.Feed
	switch rest {
	case "/":
		return readyCheck(declaration, fmt.Sprintf("%s points %s at Feed %s.", declaration.Source, declaration.Client, feed.Slug()))
	case "":
		return wrongRegistryCheck(declaration, feed.BaseURL, "the Feed Base URL ends with a slash")
	default:
		return wrongRegistryCheck(declaration, feed.BaseURL, "an npm registry must be the Feed Base URL itself")
	}
}

func npmCredentialCheck(source string, key string, value string) Check {
	switch {
	case containsLiteralTokenSecret(value):
		return literalTokenCheck(source, key)
	case strings.Contains(value, "${"):
		return credentialPlaceholderCheck(source, key)
	default:
		return committedCredentialCheck(source, key)
	}
}

func readNpmrc(file projectFile, domain packageClientDomain, pnpmInUse bool) reading {
	var result reading
	var defaultFeed *Feed
	var scopedFeeds []Declaration
	var otherScopes []Declaration
	for _, entry := range parseNpmrc(file.content) {
		lower := strings.ToLower(entry.key)
		switch {
		case lower == "registry" || scopedRegistryKey.MatchString(entry.key):
			declaration := result.npmRegistry(domain, file.path, "npm", entry.key, entry.value)
			switch {
			case declaration.Feed == nil && lower != "registry":
				otherScopes = append(otherScopes, declaration)
			case declaration.Feed != nil && lower == "registry":
				defaultFeed = declaration.Feed
			case declaration.Feed != nil:
				scopedFeeds = append(scopedFeeds, declaration)
			}
		case isNpmCredentialKey(entry.key):
			credential := npmCredentialCheck(file.path, entry.key, entry.value)
			if pnpmInUse && credential.Code == "client_config_credential_placeholder" {
				credential = pnpmPlaceholderCheck(file.path, entry.key)
			}
			result.add(credential)
		case lower == "always-auth":
			result.add(check(
				"client_config_stale_setting",
				StatusWarning,
				fmt.Sprintf("%s sets always-auth, which current npm ignores and which can mask the registry you configured.", file.path),
				"Delete the always-auth line.",
			).at(file.path))
		}
	}
	if defaultFeed == nil {
		for _, declaration := range scopedFeeds {
			result.add(check(
				"packagemaze_scoped_route_only",
				StatusWarning,
				fmt.Sprintf("%s routes only %s through Feed %s; every other package still installs from the default registry.", file.path, strings.TrimSuffix(declaration.Setting, ":registry"), declaration.Feed.Slug()),
				fmt.Sprintf("Make the Feed the default registry: registry=%s. See %s", declaration.Feed.BaseURL, DocsSetUpNpmAndPnpm),
			).at(file.path).forFeed(*declaration.Feed))
		}
		return result
	}
	for _, declaration := range otherScopes {
		result.add(check(
			"registry_route_bypasses_packagemaze",
			StatusWarning,
			fmt.Sprintf("%s routes %s to %s, outside Feed %s.", file.path, strings.TrimSuffix(declaration.Setting, ":registry"), urlHost(declaration.URL), defaultFeed.Slug()),
			"Prefer linking that source to the Feed and deleting the scope mapping, so every install goes through PackageMaze. Keep a direct mapping only for an intentional gradual migration.",
		).at(file.path).forFeed(*defaultFeed))
	}
	return result
}

func readYarnrc(file projectFile, domain packageClientDomain) reading {
	var result reading
	root := asMap(parseYAML(file.content))
	if root == nil {
		return result
	}
	var registryFeed *Feed
	if registry := asString(root["npmRegistryServer"]); registry != "" {
		registryFeed = result.npmRegistry(domain, file.path, "yarn", "npmRegistryServer", registry).Feed
	}
	if publish := asString(root["npmPublishRegistry"]); publish != "" {
		result.npmPublishRegistry(domain, file.path, "yarn", "npmPublishRegistry", publish)
	}
	scopes := asMap(root["npmScopes"])
	for _, scope := range sortedKeys(scopes) {
		settings := asMap(scopes[scope])
		if registry := asString(settings["npmRegistryServer"]); registry != "" {
			declaration := result.npmRegistry(domain, file.path, "yarn", "npmScopes."+scope+".npmRegistryServer", registry)
			if registryFeed == nil {
				registryFeed = declaration.Feed
			}
		}
		if token := asString(settings["npmAuthToken"]); token != "" {
			result.add(npmCredentialCheck(file.path, "npmScopes."+scope+".npmAuthToken", token))
		}
	}
	if token := asString(root["npmAuthToken"]); token != "" {
		result.add(npmCredentialCheck(file.path, "npmAuthToken", token))
	}
	registries := asMap(root["npmRegistries"])
	for _, registry := range sortedKeys(registries) {
		if token := asString(asMap(registries[registry])["npmAuthToken"]); token != "" {
			result.add(npmCredentialCheck(file.path, "npmRegistries."+redactURL(registry)+".npmAuthToken", token))
		}
	}
	if registryFeed != nil && !asBool(root["npmAlwaysAuth"]) {
		result.add(check(
			"client_config_incomplete",
			StatusWarning,
			fmt.Sprintf("%s does not set npmAlwaysAuth: true, so Yarn may fetch from Feed %s without sending its credential.", file.path, registryFeed.Slug()),
			"Add npmAlwaysAuth: true next to npmRegistryServer, as the Feed's Setup Instructions show.",
		).at(file.path).forFeed(*registryFeed))
	}
	return result
}

func readBunfig(file projectFile, domain packageClientDomain) reading {
	var result reading
	install := tables(parseTOML(file.content), "install")
	if len(install) == 0 {
		return result
	}
	result.bunRegistry(file, domain, "install.registry", install[0]["registry"])
	scopes := asMap(install[0]["scopes"])
	for _, scope := range sortedKeys(scopes) {
		result.bunRegistry(file, domain, "install.scopes."+strings.TrimPrefix(scope, "@"), scopes[scope])
	}
	return result
}

// bunRegistry reads bun's registry setting, which is either a URL or a table
// carrying the URL and a credential.
func (r *reading) bunRegistry(file projectFile, domain packageClientDomain, setting string, value any) {
	rawURL := asString(value)
	if table := asMap(value); table != nil {
		rawURL = asString(table["url"])
		for _, credential := range []string{"token", "password", "username"} {
			if secret := asString(table[credential]); secret != "" {
				r.add(npmCredentialCheck(file.path, setting+"."+credential, secret))
			}
		}
	}
	if rawURL != "" {
		r.npmRegistry(domain, file.path, "bun", setting, rawURL)
	}
}

// readPackageJSON reads the publish registry and reports whether the project
// pins pnpm as its package manager.
func readPackageJSON(file projectFile, domain packageClientDomain) (reading, bool) {
	var result reading
	var manifest struct {
		PublishConfig  map[string]any `json:"publishConfig"`
		PackageManager string         `json:"packageManager"`
	}
	if err := json.Unmarshal([]byte(file.content), &manifest); err != nil {
		return result, false
	}
	if registry := asString(manifest.PublishConfig["registry"]); registry != "" {
		result.npmPublishRegistry(domain, file.path, "npm", "publishConfig.registry", registry)
	}
	return result, strings.HasPrefix(strings.TrimSpace(manifest.PackageManager), "pnpm")
}

type pnpmRegistryDescriptor struct {
	serverType        string
	supportsTimeField bool
}

func readPnpmWorkspace(file projectFile, domain packageClientDomain) (reading, map[string]pnpmRegistryDescriptor) {
	var result reading
	descriptors := map[string]pnpmRegistryDescriptor{}
	registries := asMap(asMap(parseYAML(file.content))["registries"])
	for _, registry := range sortedKeys(registries) {
		declaration, _ := domain.declaration(file.path, "pnpm", ProtocolNpm, "registries", registry)
		if declaration.Feed == nil {
			continue
		}
		result.declare(declaration)
		settings := asMap(registries[registry])
		descriptors[declaration.Feed.Slug()] = pnpmRegistryDescriptor{
			serverType:        asString(settings["serverType"]),
			supportsTimeField: asBool(settings["supportsTimeField"]),
		}
	}
	return result, descriptors
}

func pnpmDescriptorCheck(source string, feed Feed, descriptors map[string]pnpmRegistryDescriptor) Check {
	descriptor, described := descriptors[feed.Slug()]
	switch {
	case !described:
		return check(
			"pnpm_packagemaze_registry_description_missing",
			StatusWarning,
			fmt.Sprintf("pnpm is in use, but %s does not describe Feed %s.", source, feed.Slug()),
			fmt.Sprintf("For pnpm 11.23+ or 12, add a registries entry keyed by %s with serverType: npm and supportsTimeField: true. Keep routing through the committed .npmrc. See %s", feed.BaseURL, DocsSetUpNpmAndPnpm),
		).at(source).forFeed(feed)
	case descriptor.serverType != "npm" || !descriptor.supportsTimeField:
		return check(
			"pnpm_packagemaze_registry_description_incomplete",
			StatusWarning,
			fmt.Sprintf("%s describes Feed %s without both pnpm optimizations.", source, feed.Slug()),
			fmt.Sprintf("Set serverType: npm and supportsTimeField: true under the registries entry for %s.", feed.BaseURL),
		).at(source).forFeed(feed)
	default:
		return check(
			"pnpm_packagemaze_registry_description_ready",
			StatusPass,
			fmt.Sprintf("%s lets pnpm reconstruct tarball URLs and use abbreviated metadata for Feed %s.", source, feed.Slug()),
			"No change needed; keep the default registry route in .npmrc.",
		).at(source).forFeed(feed)
	}
}
