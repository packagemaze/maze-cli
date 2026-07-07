package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type Provider string

const (
	ProviderAuto     Provider = "auto"
	ProviderGitHub   Provider = "github"
	ProviderGitLab   Provider = "gitlab"
	ProviderCircleCI Provider = "circleci"
	ProviderManual   Provider = "manual"
)

type LookupEnv func(string) (string, bool)

type LookPath func(string) (string, error)

type CommandRunner func(context.Context, string, ...string) ([]byte, error)

func DefaultLookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

func DefaultLookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func DefaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func ParseProvider(value string) (Provider, error) {
	normalized := Provider(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", ProviderAuto:
		return ProviderAuto, nil
	case ProviderGitHub, ProviderGitLab, ProviderCircleCI, ProviderManual:
		return normalized, nil
	default:
		return "", fmt.Errorf("provider must be auto, github, gitlab, circleci, or manual")
	}
}

func DetectProvider(env LookupEnv) (Provider, bool, error) {
	if env == nil {
		env = DefaultLookupEnv
	}
	detected := make([]Provider, 0, 3)
	if envEqualsTrue(env, "GITHUB_ACTIONS") {
		detected = append(detected, ProviderGitHub)
	}
	if envEqualsTrue(env, "GITLAB_CI") {
		detected = append(detected, ProviderGitLab)
	}
	if envEqualsTrue(env, "CIRCLECI") {
		detected = append(detected, ProviderCircleCI)
	}
	if len(detected) == 0 {
		return "", false, nil
	}
	if len(detected) > 1 {
		names := make([]string, 0, len(detected))
		for _, provider := range detected {
			names = append(names, string(provider))
		}
		return "", true, fmt.Errorf("multiple CI providers were detected (%s); pass --provider to choose one", strings.Join(names, ", "))
	}
	return detected[0], true, nil
}

func ClientContext(provider Provider, env LookupEnv) map[string]any {
	if env == nil {
		env = DefaultLookupEnv
	}
	ciContext := map[string]any{}
	switch provider {
	case ProviderCircleCI:
		addEnv(ciContext, env, "branch", "CIRCLE_BRANCH")
		addEnv(ciContext, env, "build_num", "CIRCLE_BUILD_NUM")
		addEnv(ciContext, env, "build_url", "CIRCLE_BUILD_URL")
		addEnv(ciContext, env, "job", "CIRCLE_JOB")
		addEnv(ciContext, env, "organization_id", "CIRCLE_ORGANIZATION_ID")
		addEnv(ciContext, env, "pipeline_id", "CIRCLE_PIPELINE_ID")
		addEnv(ciContext, env, "project_id", "CIRCLE_PROJECT_ID")
		addEnv(ciContext, env, "project_reponame", "CIRCLE_PROJECT_REPONAME")
		addEnv(ciContext, env, "project_username", "CIRCLE_PROJECT_USERNAME")
		addEnv(ciContext, env, "pull_request_url", "CIRCLE_PULL_REQUEST")
		addEnvList(ciContext, env, "pull_requests", "CIRCLE_PULL_REQUESTS")
		addEnv(ciContext, env, "repository_url", "CIRCLE_REPOSITORY_URL")
		addEnv(ciContext, env, "sha", "CIRCLE_SHA1")
		addEnv(ciContext, env, "workflow_id", "CIRCLE_WORKFLOW_ID")
		addEnv(ciContext, env, "workflow_job_id", "CIRCLE_WORKFLOW_JOB_ID")
	case ProviderGitHub:
		addEnv(ciContext, env, "event_name", "GITHUB_EVENT_NAME")
		addEnv(ciContext, env, "head_ref", "GITHUB_HEAD_REF")
		addEnv(ciContext, env, "ref", "GITHUB_REF")
		addEnv(ciContext, env, "ref_name", "GITHUB_REF_NAME")
		addEnv(ciContext, env, "ref_type", "GITHUB_REF_TYPE")
		addEnv(ciContext, env, "repository", "GITHUB_REPOSITORY")
		addEnv(ciContext, env, "run_attempt", "GITHUB_RUN_ATTEMPT")
		addEnv(ciContext, env, "run_id", "GITHUB_RUN_ID")
		addEnv(ciContext, env, "sha", "GITHUB_SHA")
		addEnv(ciContext, env, "workflow", "GITHUB_WORKFLOW")
		addEnv(ciContext, env, "workflow_ref", "GITHUB_WORKFLOW_REF")
		addGitHubURLs(ciContext, env)
		addGitHubPullRequestEvent(ciContext, env)
	case ProviderAuto, ProviderGitLab, ProviderManual:
	}
	if len(ciContext) == 0 {
		return nil
	}
	return map[string]any{"ci": ciContext}
}

func addEnv(context map[string]any, env LookupEnv, key string, envName string) {
	value, ok := env(envName)
	value = strings.TrimSpace(value)
	if ok && value != "" {
		context[key] = value
	}
}

func addEnvList(context map[string]any, env LookupEnv, key string, envName string) {
	value, ok := env(envName)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return
	}
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			values = append(values, trimmed)
		}
	}
	if len(values) > 0 {
		context[key] = values
	}
}

func addGitHubURLs(context map[string]any, env LookupEnv) {
	serverURL, ok := env("GITHUB_SERVER_URL")
	if !ok || strings.TrimSpace(serverURL) == "" {
		serverURL = "https://github.com"
	}
	repository, hasRepository := env("GITHUB_REPOSITORY")
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if !hasRepository || repository == "" {
		return
	}
	repositoryURL := strings.TrimRight(strings.TrimSpace(serverURL), "/") + "/" + repository
	context["repository_url"] = repositoryURL
	if runID, ok := env("GITHUB_RUN_ID"); ok && strings.TrimSpace(runID) != "" {
		runURL := repositoryURL + "/actions/runs/" + strings.TrimSpace(runID)
		if attempt, ok := env("GITHUB_RUN_ATTEMPT"); ok && strings.TrimSpace(attempt) != "" && strings.TrimSpace(attempt) != "1" {
			runURL += "/attempts/" + strings.TrimSpace(attempt)
		}
		context["run_url"] = runURL
	}
	ref, ok := env("GITHUB_REF")
	if ok {
		if number := pullRequestNumberFromGitHubRef(ref); number != "" {
			context["pull_request_number"] = number
			context["pull_request_url"] = repositoryURL + "/pull/" + number
		}
	}
}

func addGitHubPullRequestEvent(context map[string]any, env LookupEnv) {
	eventPath, ok := env("GITHUB_EVENT_PATH")
	if !ok || strings.TrimSpace(eventPath) == "" {
		return
	}
	content, err := os.ReadFile(strings.TrimSpace(eventPath))
	if err != nil {
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(io.LimitReader(strings.NewReader(string(content)), 1024*1024)).Decode(&payload); err != nil {
		return
	}
	pullRequest, ok := payload["pull_request"].(map[string]any)
	if !ok {
		return
	}
	if title, ok := pullRequest["title"].(string); ok && strings.TrimSpace(title) != "" {
		context["pull_request_title"] = strings.TrimSpace(title)
	}
	if htmlURL, ok := pullRequest["html_url"].(string); ok && strings.TrimSpace(htmlURL) != "" {
		context["pull_request_url"] = strings.TrimSpace(htmlURL)
	}
	if number := pullRequestNumberValue(pullRequest["number"]); number != "" {
		context["pull_request_number"] = number
	}
}

func pullRequestNumberFromGitHubRef(ref string) string {
	match := regexp.MustCompile(`^refs/pull/([0-9]+)(?:/|$)`).FindStringSubmatch(strings.TrimSpace(ref))
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func pullRequestNumberValue(value any) string {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed == float64(int64(typed)) {
			return fmt.Sprintf("%.0f", typed)
		}
	case string:
		return strings.TrimSpace(typed)
	}
	return ""
}

func ReadTokenFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read OIDC token file: %w", err)
	}
	return cleanToken(string(content), "OIDC token file")
}

func ReadTokenStdin(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", errors.New("stdin is not available")
	}
	content, err := io.ReadAll(io.LimitReader(stdin, 1024*1024))
	if err != nil {
		return "", fmt.Errorf("read OIDC token from stdin: %w", err)
	}
	return cleanToken(string(content), "stdin")
}

func ReadTokenEnv(env LookupEnv, names ...string) (string, string, bool) {
	if env == nil {
		env = DefaultLookupEnv
	}
	for _, name := range names {
		trimmedName := strings.TrimSpace(name)
		if trimmedName == "" {
			continue
		}
		if value, ok := env(trimmedName); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), trimmedName, true
		}
	}
	return "", "", false
}

func RequestGitHubOIDCToken(
	ctx context.Context,
	audience string,
	env LookupEnv,
	httpClient *http.Client,
) (string, error) {
	if env == nil {
		env = DefaultLookupEnv
	}
	requestURL, hasURL := env("ACTIONS_ID_TOKEN_REQUEST_URL")
	requestToken, hasToken := env("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if !hasURL || strings.TrimSpace(requestURL) == "" || !hasToken || strings.TrimSpace(requestToken) == "" {
		return "", errors.New(`GitHub Actions was detected, but this job cannot request an OIDC token.
Add this to your workflow:
permissions:
  contents: read
  id-token: write`)
	}

	oidcURL, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("GitHub Actions OIDC request URL is invalid")
	}
	query := oidcURL.Query()
	query.Set("audience", audience)
	oidcURL.RawQuery = query.Encode()

	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, oidcURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("build GitHub Actions OIDC request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "bearer "+strings.TrimSpace(requestToken))

	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request GitHub Actions OIDC token: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub Actions OIDC token request failed with HTTP %d; check permissions.id-token: write", response.StatusCode)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("GitHub Actions OIDC token response was not valid JSON")
	}
	return cleanToken(payload.Value, "GitHub Actions OIDC response")
}

func RequestCircleCIToken(
	ctx context.Context,
	audience string,
	lookPath LookPath,
	runner CommandRunner,
) (string, error) {
	if lookPath == nil {
		lookPath = DefaultLookPath
	}
	if runner == nil {
		runner = DefaultCommandRunner
	}
	if _, err := lookPath("circleci"); err != nil {
		return "", fmt.Errorf("CircleCI OIDC token was not found. Set MAZE_OIDC_TOKEN or install the CircleCI CLI so `circleci run oidc get` is available")
	}
	claims, err := json.Marshal(map[string]string{"aud": audience})
	if err != nil {
		return "", fmt.Errorf("build CircleCI OIDC claims: %w", err)
	}
	output, err := runner(ctx, "circleci", "run", "oidc", "get", "--claims", string(claims))
	if err != nil {
		return "", fmt.Errorf("CircleCI OIDC token request failed. Set MAZE_OIDC_TOKEN or check CircleCI OIDC availability")
	}
	return cleanToken(string(output), "CircleCI OIDC response")
}

func cleanToken(value string, source string) (string, error) {
	token := strings.TrimSpace(value)
	if token == "" {
		return "", fmt.Errorf("%s did not contain an OIDC token", source)
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("%s contained multiple lines; expected one OIDC token", source)
	}
	return token, nil
}

func envEqualsTrue(env LookupEnv, key string) bool {
	value, ok := env(key)
	return ok && strings.EqualFold(strings.TrimSpace(value), "true")
}
