package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// gitHubActionsEnv is a job that can request its own identity token.
func gitHubActionsEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://github.example/token",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runtime-token",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func gitHubTokenClient(requests *int) *http.Client {
	return &http.Client{Transport: oidcTransport(func(*http.Request) (*http.Response, error) {
		*requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"github-oidc-token"}`)), Header: make(http.Header)}, nil
	})}
}

func TestExchangeNamesAProviderOnlyWhenMazeFetchedTheTokenOrTheCallerNamedOne(t *testing.T) {
	circleCLI := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "circleci" {
			return nil, errors.New("unexpected command")
		}
		return []byte("circleci-oidc-token\n"), nil
	}
	tests := []struct {
		name         string
		config       Config
		env          map[string]string
		stdin        string
		wantProvider string
		wantToken    string
		wantGitHub   int
	}{
		{name: "supplied env token on an unknown platform", env: map[string]string{"MAZE_OIDC_TOKEN": "platform-token"}, wantToken: "platform-token"},
		{name: "supplied stdin token on an unknown platform", config: Config{OIDCTokenStdin: true}, stdin: "platform-token\n", wantToken: "platform-token"},
		{name: "GitLab is an ordinary platform", env: map[string]string{"GITLAB_CI": "true", "MAZE_OIDC_TOKEN": "gitlab-id-token"}, wantToken: "gitlab-id-token"},
		{name: "MAZE_IDENTITY_TOKEN when MAZE_OIDC_TOKEN is empty", env: map[string]string{"MAZE_OIDC_TOKEN": " ", "MAZE_IDENTITY_TOKEN": "identity"}, wantToken: "identity"},
		{name: "MAZE_OIDC_TOKEN before MAZE_IDENTITY_TOKEN", env: map[string]string{"MAZE_OIDC_TOKEN": "oidc", "MAZE_IDENTITY_TOKEN": "identity"}, wantToken: "oidc"},
		{name: "named variable before MAZE_IDENTITY_TOKEN", config: Config{OIDCTokenEnv: "CUSTOM_TOKEN"}, env: map[string]string{"CUSTOM_TOKEN": "custom", "MAZE_IDENTITY_TOKEN": "identity"}, wantToken: "custom"},
		{name: "GitHub Actions request", env: gitHubActionsEnv(nil), wantProvider: "github", wantToken: "github-oidc-token", wantGitHub: 1},
		{name: "GitHub Actions with a supplied stdin token", config: Config{OIDCTokenStdin: true}, env: gitHubActionsEnv(nil), stdin: "other-token", wantToken: "other-token"},
		{name: "CircleCI CLI request", env: map[string]string{"CIRCLECI": "true"}, wantProvider: "circleci", wantToken: "circleci-oidc-token"},
		{name: "CircleCI with a supplied env token", env: map[string]string{"CIRCLECI": "true", "MAZE_OIDC_TOKEN": "supplied"}, wantToken: "supplied"},
		{name: "named github with a supplied token", config: Config{Provider: "github", OIDCTokenStdin: true}, stdin: "github-token", wantProvider: "github", wantToken: "github-token"},
		{name: "named oidc", config: Config{Provider: "oidc"}, env: map[string]string{"MAZE_OIDC_TOKEN": "platform-token"}, wantProvider: "oidc", wantToken: "platform-token"},
		{name: "named manual for local development", config: Config{Provider: "manual"}, env: map[string]string{"MAZE_OIDC_TOKEN": "local-token"}, wantProvider: "manual", wantToken: "local-token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gitHubRequests := 0
			exchanger := &recordingExchanger{t: t}
			config := test.config
			config.Feeds = []string{"your-org/npm"}
			config.Purpose = "install"
			result, _, err := Exchange(context.Background(), config, Dependencies{
				Env:           mapLookup(test.env),
				Stdin:         strings.NewReader(test.stdin),
				HTTPClient:    gitHubTokenClient(&gitHubRequests),
				LookPath:      func(name string) (string, error) { return "/usr/bin/" + name, nil },
				CommandRunner: circleCLI,
				Exchanger:     exchanger,
			})
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if exchanger.request.Provider != test.wantProvider || result.Provider != test.wantProvider {
				t.Fatalf("provider = %q, result provider = %q, want %q", exchanger.request.Provider, result.Provider, test.wantProvider)
			}
			if exchanger.request.OIDCToken != test.wantToken {
				t.Fatalf("oidc_token = %q, want %q", exchanger.request.OIDCToken, test.wantToken)
			}
			if gitHubRequests != test.wantGitHub {
				t.Fatalf("GitHub token requests = %d, want %d", gitHubRequests, test.wantGitHub)
			}
		})
	}
}

func TestExchangeKeepsCircleCIBuildContextWhenTheTokenIsSupplied(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm"}, Purpose: "install"}, Dependencies{
		Env:       mapLookup(map[string]string{"CIRCLECI": "true", "CIRCLE_BUILD_NUM": "42", "MAZE_OIDC_TOKEN": "supplied"}),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if exchanger.request.Provider != "" {
		t.Fatalf("provider = %q, want none", exchanger.request.Provider)
	}
	environment, ok := exchanger.request.Client["circleci_environment"].(map[string]any)
	if !ok || environment["CIRCLE_BUILD_NUM"] != "42" {
		t.Fatalf("client = %#v", exchanger.request.Client)
	}
}

func TestExchangeWithoutAnIdentitySaysWhereToGetOne(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm"}, Purpose: "install"}, Dependencies{
		Env:       mapLookup(map[string]string{"GITLAB_CI": "true"}),
		Exchanger: exchanger,
	})
	if err == nil || exchanger.called {
		t.Fatalf("err = %v, called = %v", err, exchanger.called)
	}
	for _, want := range []string{"No built-in platform was detected", "--identity-token-stdin", "MAZE_OIDC_TOKEN", "--identity-token-injected"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestExchangeRefusesTheGitLabProvider(t *testing.T) {
	_, _, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm"}, Purpose: "install", Provider: "gitlab"}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "token"}),
		Exchanger: &recordingExchanger{t: t},
	})
	if err == nil || err.Error() != "provider must be auto, github, circleci, oidc, or manual" {
		t.Fatalf("err = %v", err)
	}
}

func TestExchangeWithAnInjectedIdentitySendsNoTokenAndNoProvider(t *testing.T) {
	gitHubRequests := 0
	exchanger := &recordingExchanger{t: t}
	result, resolved, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm"}, Purpose: "install", IdentityInjected: true}, Dependencies{
		// Not even a detected built-in platform is asked for a token.
		Env:        mapLookup(map[string]string{"GITHUB_ACTIONS": "true", "ACTIONS_ID_TOKEN_REQUEST_URL": "https://github.example/token", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runtime"}),
		HTTPClient: gitHubTokenClient(&gitHubRequests),
		Exchanger:  exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if exchanger.request.OIDCToken != "" || exchanger.request.Provider != "" || result.Provider != "" {
		t.Fatalf("request = %#v", exchanger.request)
	}
	if exchanger.request.Audience != "https://api.packagemaze.com" {
		t.Fatalf("audience = %q", exchanger.request.Audience)
	}
	if gitHubRequests != 0 || !resolved.IdentityInjected {
		t.Fatalf("GitHub requests = %d, resolved = %#v", gitHubRequests, resolved)
	}
}

func TestExchangeWithAnInjectedIdentityRefusesAnyOtherIdentitySource(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		env     map[string]string
		wantErr string
	}{
		{name: "file", config: Config{OIDCTokenFile: "token.jwt"}, wantErr: "remove --identity-token-file"},
		{name: "stdin", config: Config{OIDCTokenStdin: true}, wantErr: "remove --identity-token-stdin"},
		{name: "default variable", env: map[string]string{"MAZE_OIDC_TOKEN": "token"}, wantErr: "remove MAZE_OIDC_TOKEN"},
		{name: "identity variable", env: map[string]string{"MAZE_IDENTITY_TOKEN": "token"}, wantErr: "remove MAZE_IDENTITY_TOKEN"},
		{name: "named variable", config: Config{OIDCTokenEnv: "CUSTOM"}, env: map[string]string{"CUSTOM": "token"}, wantErr: "remove CUSTOM"},
		{name: "named provider", config: Config{Provider: "oidc"}, wantErr: "--identity-token-injected cannot be combined with --provider oidc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exchanger := &recordingExchanger{t: t}
			config := test.config
			config.Feeds = []string{"your-org/npm"}
			config.Purpose = "install"
			config.IdentityInjected = true
			_, _, err := Exchange(context.Background(), config, Dependencies{Env: mapLookup(test.env), Exchanger: exchanger})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, test.wantErr)
			}
			if exchanger.called {
				t.Fatalf("PackageMaze was called")
			}
		})
	}
}

func TestExchangeSeveralFeedsReportsTheirReferences(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	result, _, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm", "your-org/pypi"}, Purpose: "install", Format: "shell"}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "token"}),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if strings.Join(result.FeedReferences, " ") != "your-org/npm your-org/pypi" {
		t.Fatalf("Feed references = %#v", result.FeedReferences)
	}
	single, _, err := Exchange(context.Background(), Config{Feeds: []string{"your-org/npm"}, Purpose: "install"}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "token"}),
		Exchanger: exchanger,
	})
	if err != nil || single.FeedReferences != nil {
		t.Fatalf("one Feed: references = %#v, err = %v", single.FeedReferences, err)
	}
}
