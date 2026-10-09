package doctor

import (
	"fmt"
	"path"
	"strings"
)

const setupMazeAction = "packagemaze/setup-maze"

func isWorkflowFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// readWorkflows finds GitHub Actions jobs that mint a Token with setup-maze and
// checks the one permission that exchange depends on.
func readWorkflows(p *project, domain packageClientDomain) reading {
	var result reading
	files, _ := p.matching(path.Join(".github", "workflows"), isWorkflowFile)
	for _, file := range files {
		result.merge(readWorkflow(file, domain))
	}
	return result
}

func readWorkflow(file projectFile, domain packageClientDomain) reading {
	var result reading
	workflow := asMap(parseYAML(file.content))
	workflowGrantsIDToken := grantsIDToken(workflow["permissions"])
	jobs := asMap(workflow["jobs"])
	for _, jobName := range sortedKeys(jobs) {
		job := asMap(jobs[jobName])
		steps := asList(job["steps"])
		protocol := workflowJobProtocol(steps)
		usesSetupMaze := false
		for _, stepValue := range steps {
			step := asMap(stepValue)
			uses := asString(step["uses"])
			if uses != setupMazeAction && !strings.HasPrefix(uses, setupMazeAction+"@") {
				continue
			}
			usesSetupMaze = true
			with := asMap(step["with"])
			slug := asString(with["feed"])
			if slug == "" {
				continue
			}
			purpose := asString(with["purpose"])
			if purpose == "" {
				purpose = "install"
			}
			declaration := Declaration{Source: file.path, Client: githubActionsClient, Protocol: protocol, Setting: "setup-maze feed (" + purpose + ")", URL: slug}
			if feed, ok := domain.feedFromSlug(slug); ok {
				declaration.Feed = &feed
				declaration.URL = feed.BaseURL
			}
			result.declare(declaration)
		}
		if usesSetupMaze && !workflowGrantsIDToken && !grantsIDToken(job["permissions"]) {
			result.add(check(
				"ci_missing_id_token_permission",
				StatusFail,
				fmt.Sprintf("%s runs %s without permissions.id-token: write, so GitHub Actions issues no OIDC token and setup-maze cannot mint a PackageMaze Token.", file.path, setupMazeAction),
				fmt.Sprintf("Add permissions with id-token: write (and contents: read) to the workflow or that job, and make sure the Feed has a CI access rule for this repository. See %s", DocsSetUpCI),
			).at(file.path))
		}
	}
	return result
}

func grantsIDToken(permissions any) bool {
	if text, ok := permissions.(string); ok {
		return strings.TrimSpace(text) == "write-all"
	}
	return asString(asMap(permissions)["id-token"]) == "write"
}

func workflowJobProtocol(steps []any) ArtifactProtocol {
	for _, stepValue := range steps {
		step := asMap(stepValue)
		text := strings.ToLower(asString(step["uses"]) + "\n" + asString(step["run"]))
		switch {
		case strings.Contains(text, "setup-node") || strings.Contains(text, "npm ") || strings.Contains(text, "pnpm") || strings.Contains(text, "yarn") || strings.Contains(text, "bun "):
			return ProtocolNpm
		case strings.Contains(text, "setup-python") || strings.Contains(text, "setup-uv") || strings.Contains(text, "pip ") || strings.Contains(text, "uv ") || strings.Contains(text, "poetry") || strings.Contains(text, "pdm ") || strings.Contains(text, "twine"):
			return ProtocolPyPI
		}
	}
	return ProtocolUnknown
}
