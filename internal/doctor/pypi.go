package doctor

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	requirementsIndexOption = regexp.MustCompile(`(?i)(?:^|\s)(--index-url|-i|--extra-index-url)(?:=|\s+)(\S+)`)
	pipConfSection          = regexp.MustCompile(`^\[([^\]]+)\]$`)
	pipConfIndexAssignment  = regexp.MustCompile(`(?i)^(extra-index-url|index-url)\s*[=:]\s*(.*)$`)
)

func isRequirementsFile(name string) bool {
	return strings.HasPrefix(name, "requirements") && (strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".in"))
}

type pypiIndexDeclaration struct {
	Declaration
	extra bool
}

// pypiIndex records one index URL with the checks on its shape and embedded
// credentials. Requirements files and Pipfiles may carry ${MAZE_TOKEN} in the
// URL because their clients expand environment variables there; other files
// should not.
func (r *reading) pypiIndex(domain packageClientDomain, source string, client string, setting string, rawURL string, extra bool, placeholdersAllowed bool) pypiIndexDeclaration {
	declaration, rest := domain.declaration(source, client, ProtocolPyPI, setting, rawURL)
	r.declare(declaration)
	r.add(urlCredentialChecks(source, setting, rawURL, placeholdersAllowed)...)
	if declaration.Feed != nil && !extra {
		r.add(pypiSimpleShapeCheck(declaration, rest))
	}
	return pypiIndexDeclaration{Declaration: declaration, extra: extra}
}

func urlCredentialChecks(source string, setting string, rawURL string, placeholdersAllowed bool) []Check {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.User == nil {
		return nil
	}
	password, _ := parsed.User.Password()
	switch {
	case containsLiteralTokenSecret(password) || containsLiteralTokenSecret(parsed.User.Username()):
		return []Check{literalTokenCheck(source, setting)}
	case strings.Contains(password, "${") || strings.HasPrefix(password, "$"):
		if placeholdersAllowed {
			return nil
		}
		return []Check{credentialPlaceholderCheck(source, setting)}
	default:
		return []Check{committedCredentialCheck(source, setting)}
	}
}

// pypiSimpleShapeCheck accepts the Feed's PyPI Simple API URL, with or without
// its trailing slash, matching Feed Doctor's endpoint comparison.
func pypiSimpleShapeCheck(declaration Declaration, rest string) Check {
	feed := *declaration.Feed
	switch strings.TrimSuffix(rest, "/") {
	case "/simple":
		return readyCheck(declaration, fmt.Sprintf("%s points %s at Feed %s through its Simple API URL.", declaration.Source, declaration.Client, feed.Slug()))
	case "", "/":
		return wrongRegistryCheck(declaration, feed.SimpleURL(), "pip, uv, Poetry, and PDM install through the Feed's Simple API URL, not the Feed Base URL itself")
	case "/legacy":
		return wrongRegistryCheck(declaration, feed.SimpleURL(), "that is the Feed's Legacy Upload URL, which is for publishing")
	default:
		return wrongRegistryCheck(declaration, feed.SimpleURL(), "that path is not the Feed's Simple API URL")
	}
}

func pypiPublishShapeChecks(declaration Declaration, rest string) []Check {
	if declaration.Feed == nil || strings.TrimSuffix(rest, "/") == "/legacy" {
		return nil
	}
	return []Check{wrongRegistryCheck(declaration, declaration.Feed.LegacyURL(), "publishing uses the Feed's Legacy Upload URL")}
}

// pypiIndexSetChecks enforces the one-index rule: PackageMaze is the default
// index and nothing sits beside it in the resolver path.
func pypiIndexSetChecks(source string, indexes []pypiIndexDeclaration) []Check {
	var primary *pypiIndexDeclaration
	var extras []pypiIndexDeclaration
	for index := range indexes {
		if indexes[index].extra {
			extras = append(extras, indexes[index])
		} else if primary == nil {
			primary = &indexes[index]
		}
	}
	var checks []Check
	if primary != nil && primary.Feed != nil {
		for _, extra := range extras {
			checks = append(checks, bypassCheck(
				source,
				fmt.Sprintf("%s keeps %s in the resolver path beside Feed %s (%s), so a package can be taken from either index.", source, extra.URL, primary.Feed.Slug(), extra.Setting),
				fmt.Sprintf("Remove %s. A Feed serves the public packages its policy allows, so one index is enough. See %s", extra.Setting, DocsSetUpPipUvPoetry),
			).forFeed(*primary.Feed))
		}
		return checks
	}
	for _, extra := range extras {
		if extra.Feed == nil {
			continue
		}
		checks = append(checks, bypassCheck(
			source,
			fmt.Sprintf("%s lists Feed %s only as %s, so the default index still answers first.", source, extra.Feed.Slug(), extra.Setting),
			fmt.Sprintf("Make the Feed the only index: set the index URL to %s and remove %s. See %s", extra.Feed.SimpleURL(), extra.Setting, DocsSetUpPipUvPoetry),
		).forFeed(*extra.Feed))
	}
	return checks
}

func readRequirements(file projectFile, domain packageClientDomain) reading {
	var result reading
	var indexes []pypiIndexDeclaration
	for _, rawLine := range strings.Split(file.content, "\n") {
		line := strings.TrimSpace(strings.TrimRight(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, match := range requirementsIndexOption.FindAllStringSubmatch(" "+line, -1) {
			option := strings.ToLower(match[1])
			if option == "-i" {
				option = "--index-url"
			}
			indexes = append(indexes, result.pypiIndex(domain, file.path, "pip", option, match[2], option == "--extra-index-url", true))
		}
	}
	result.add(pypiIndexSetChecks(file.path, indexes)...)
	return result
}

// readPipConf reads index-url and extra-index-url under any section, including
// pip's multi-line form where continuation lines are indented under the key.
func readPipConf(file projectFile, domain packageClientDomain) reading {
	var result reading
	var indexes []pypiIndexDeclaration
	section := "global"
	continuing := ""
	for _, rawLine := range strings.Split(file.content, "\n") {
		rawLine = strings.TrimRight(rawLine, "\r")
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if continuing != "" && rawLine != line {
			for _, value := range strings.Fields(line) {
				indexes = append(indexes, result.pypiIndex(domain, file.path, "pip", continuing, value, strings.HasSuffix(continuing, "extra-index-url"), false))
			}
			continue
		}
		continuing = ""
		if header := pipConfSection.FindStringSubmatch(line); header != nil {
			section = strings.ToLower(strings.TrimSpace(header[1]))
			continue
		}
		match := pipConfIndexAssignment.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		option := strings.ToLower(match[1])
		continuing = section + "." + option
		for _, value := range strings.Fields(match[2]) {
			indexes = append(indexes, result.pypiIndex(domain, file.path, "pip", continuing, value, option == "extra-index-url", false))
		}
	}
	result.add(pypiIndexSetChecks(file.path, indexes)...)
	return result
}

func readPyproject(file projectFile, domain packageClientDomain) reading {
	root := parseTOML(file.content)
	var result reading
	result.merge(uvIndexReading(file, domain, tables(root, "tool", "uv", "index"), "tool.uv.index"))
	result.merge(pipStyleTomlReading(file, domain, tables(root, "tool", "uv"), "tool.uv."))
	result.merge(poetrySourceReading(file, domain, tables(root, "tool", "poetry", "source")))
	for _, source := range tables(root, "tool", "pdm", "source") {
		setting := fmt.Sprintf("tool.pdm.source[%s].url", asString(source["name"]))
		result.pypiIndex(domain, file.path, "pdm", setting, asString(source["url"]), false, false)
	}
	return result
}

func readUvToml(file projectFile, domain packageClientDomain) reading {
	root := parseTOML(file.content)
	var result reading
	result.merge(uvIndexReading(file, domain, tables(root, "index"), "index"))
	result.merge(pipStyleTomlReading(file, domain, []map[string]any{root}, ""))
	return result
}

func uvIndexReading(file projectFile, domain packageClientDomain, indexes []map[string]any, prefix string) reading {
	var result reading
	for _, index := range indexes {
		name := asString(index["name"])
		label := fmt.Sprintf("%s[%s]", prefix, name)
		declaration := result.pypiIndex(domain, file.path, "uv", label+".url", asString(index["url"]), false, false).Declaration
		if publish := asString(index["publish-url"]); publish != "" {
			publishDeclaration, publishRest := domain.declaration(file.path, "uv", ProtocolPyPI, label+".publish-url", publish)
			result.declare(publishDeclaration)
			result.add(pypiPublishShapeChecks(publishDeclaration, publishRest)...)
		}
		if declaration.Feed == nil {
			continue
		}
		feed := *declaration.Feed
		switch {
		case asBool(index["explicit"]):
			result.add(bypassCheck(
				file.path,
				fmt.Sprintf("%s marks uv index %s explicit, so only packages pinned to it install through Feed %s.", file.path, name, feed.Slug()),
				fmt.Sprintf("Remove explicit = true and set default = true so every package resolves through the Feed. See %s", DocsSetUpPipUvPoetry),
			).forFeed(feed))
		case !asBool(index["default"]):
			result.add(bypassCheck(
				file.path,
				fmt.Sprintf("%s does not set default = true on uv index %s, so uv keeps PyPI in the resolver path beside Feed %s.", file.path, name, feed.Slug()),
				fmt.Sprintf("Add default = true to the [[%s]] block named %s. See %s", prefix, name, DocsSetUpPipUvPoetry),
			).forFeed(feed))
		}
		if asString(index["authenticate"]) != "always" {
			result.add(incompleteCheck(declaration,
				fmt.Sprintf("%s does not set authenticate = \"always\" on uv index %s, so uv tries Feed %s unauthenticated first.", file.path, name, feed.Slug()),
				fmt.Sprintf("Add authenticate = \"always\" to the [[%s]] block named %s, as the Feed's Setup Instructions show.", prefix, name)))
		}
		if asString(index["publish-url"]) == "" {
			result.add(incompleteCheck(declaration,
				fmt.Sprintf("%s has no publish-url on uv index %s.", file.path, name),
				fmt.Sprintf("If this project publishes with uv, add publish-url = \"%s\".", feed.LegacyURL())))
		}
	}
	return result
}

func pipStyleTomlReading(file projectFile, domain packageClientDomain, sections []map[string]any, prefix string) reading {
	var result reading
	var indexes []pypiIndexDeclaration
	for _, section := range sections {
		for _, value := range asStrings(section["index-url"]) {
			indexes = append(indexes, result.pypiIndex(domain, file.path, "uv", prefix+"index-url", value, false, false))
		}
		for _, value := range asStrings(section["extra-index-url"]) {
			indexes = append(indexes, result.pypiIndex(domain, file.path, "uv", prefix+"extra-index-url", value, true, false))
		}
	}
	result.add(pypiIndexSetChecks(file.path, indexes)...)
	return result
}

func poetrySourceReading(file projectFile, domain packageClientDomain, sources []map[string]any) reading {
	var result reading
	var packageMaze *Feed
	var otherSources []Declaration
	for _, source := range sources {
		name := asString(source["name"])
		setting := fmt.Sprintf("tool.poetry.source[%s].url", name)
		declaration := result.pypiIndex(domain, file.path, "poetry", setting, asString(source["url"]), false, false).Declaration
		if declaration.Feed == nil {
			if declaration.URL != "" || strings.EqualFold(name, "pypi") {
				otherSources = append(otherSources, declaration)
			}
			continue
		}
		feed := *declaration.Feed
		if packageMaze == nil {
			packageMaze = declaration.Feed
		}
		switch priority := strings.ToLower(asString(source["priority"])); priority {
		case "primary", "default":
		case "":
			result.add(incompleteCheck(declaration,
				fmt.Sprintf("%s does not set a priority on Poetry source %s.", file.path, name),
				"Set priority = \"primary\" so Poetry disables its implicit PyPI source and resolves everything through the Feed."))
		default:
			result.add(bypassCheck(
				file.path,
				fmt.Sprintf("%s makes Poetry source %s %s, so Poetry keeps PyPI ahead of Feed %s.", file.path, name, priority, feed.Slug()),
				fmt.Sprintf("Set priority = \"primary\" on the source and delete other sources. See %s", DocsSetUpPipUvPoetry),
			).forFeed(feed))
		}
	}
	if packageMaze == nil {
		return result
	}
	for _, other := range otherSources {
		result.add(bypassCheck(
			file.path,
			fmt.Sprintf("%s keeps Poetry source %s beside Feed %s.", file.path, other.Setting, packageMaze.Slug()),
			fmt.Sprintf("Delete the extra source; a primary PackageMaze source already replaces PyPI. See %s", DocsSetUpPipUvPoetry),
		).forFeed(*packageMaze))
	}
	return result
}

func readPipfile(file projectFile, domain packageClientDomain) reading {
	var result reading
	var packageMaze *Feed
	var others []Declaration
	for _, source := range tables(parseTOML(file.content), "source") {
		setting := fmt.Sprintf("source[%s].url", asString(source["name"]))
		declaration := result.pypiIndex(domain, file.path, "pipenv", setting, asString(source["url"]), false, true).Declaration
		if declaration.Feed == nil {
			others = append(others, declaration)
		} else if packageMaze == nil {
			packageMaze = declaration.Feed
		}
	}
	if packageMaze == nil {
		return result
	}
	for _, other := range others {
		result.add(bypassCheck(
			file.path,
			fmt.Sprintf("%s keeps source %s beside Feed %s, so Pipenv may take a package from either.", file.path, other.Setting, packageMaze.Slug()),
			"Delete the extra [[source]] block; the Feed serves the public packages its policy allows.",
		).forFeed(*packageMaze))
	}
	return result
}
