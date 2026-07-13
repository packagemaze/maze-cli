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
	if provider != ProviderCircleCI {
		return nil
	}
	if env == nil {
		env = DefaultLookupEnv
	}
	context := map[string]any{}
	for _, name := range []string{
		"CIRCLE_BUILD_NUM",
		"CIRCLE_BUILD_URL",
		"CIRCLE_JOB",
		"CIRCLE_NODE_INDEX",
		"CIRCLE_NODE_TOTAL",
		"CIRCLE_PULL_REQUEST",
		"CIRCLE_REPOSITORY_URL",
		"CIRCLE_SHA1",
	} {
		addEnvironmentValue(context, env, name)
	}
	if raw, ok := env("CIRCLE_PULL_REQUESTS"); ok {
		values := splitEnvironmentList(raw)
		if len(values) > 0 {
			context["CIRCLE_PULL_REQUESTS"] = values
		}
	}
	if len(context) == 0 {
		return nil
	}
	return map[string]any{"circleci_environment": context}
}

func addEnvironmentValue(context map[string]any, env LookupEnv, name string) {
	value, ok := env(name)
	value = strings.TrimSpace(value)
	if ok && value != "" {
		context[name] = value
	}
}

func splitEnvironmentList(value string) []string {
	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
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
