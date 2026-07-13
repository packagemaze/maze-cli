package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider Provider
		wantFound    bool
		wantErr      string
	}{
		{
			name:         "github",
			env:          map[string]string{"GITHUB_ACTIONS": "true"},
			wantProvider: ProviderGitHub,
			wantFound:    true,
		},
		{
			name:         "gitlab",
			env:          map[string]string{"GITLAB_CI": "true"},
			wantProvider: ProviderGitLab,
			wantFound:    true,
		},
		{
			name:         "circleci",
			env:          map[string]string{"CIRCLECI": "true"},
			wantProvider: ProviderCircleCI,
			wantFound:    true,
		},
		{
			name:      "none",
			env:       map[string]string{},
			wantFound: false,
		},
		{
			name:    "ambiguous",
			env:     map[string]string{"GITHUB_ACTIONS": "true", "GITLAB_CI": "true"},
			wantErr: "multiple CI providers",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, found, err := DetectProvider(mapLookup(test.env))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectProvider returned error: %v", err)
			}
			if found != test.wantFound {
				t.Fatalf("found = %v, want %v", found, test.wantFound)
			}
			if provider != test.wantProvider {
				t.Fatalf("provider = %q, want %q", provider, test.wantProvider)
			}
		})
	}
}

func TestCircleCIClientContextUsesOnlyTheEnvironmentAllowlist(t *testing.T) {
	context := ClientContext(ProviderCircleCI, mapLookup(map[string]string{
		"CIRCLE_BUILD_NUM":         "42",
		"CIRCLE_BUILD_URL":         "https://app.circleci.com/pipelines/circleci/org/project/1/workflows/workflow/jobs/42",
		"CIRCLE_JOB":               "package-client-smoke",
		"CIRCLE_NODE_INDEX":        "0",
		"CIRCLE_NODE_TOTAL":        "2",
		"CIRCLE_PIPELINE_ID":       "signed-duplicate",
		"CIRCLE_PULL_REQUEST":      "https://github.com/packagemaze/maze-cli/pull/10",
		"CIRCLE_PULL_REQUESTS":     "https://github.com/packagemaze/maze-cli/pull/10, https://github.com/packagemaze/maze-cli/pull/11",
		"CIRCLE_REPOSITORY_URL":    "https://github.com/packagemaze/maze-cli",
		"CIRCLE_SHA1":              "abcdef1234567890abcdef1234567890abcdef12",
		"CIRCLE_USERNAME":          "must-not-be-collected",
		"CIRCLE_WORKING_DIRECTORY": "/must/not/be/collected",
	}))

	environment, ok := context["circleci_environment"].(map[string]any)
	if !ok {
		t.Fatalf("circleci environment = %#v", context)
	}
	if len(environment) != 9 {
		t.Fatalf("circleci allowlist = %#v", environment)
	}
	if _, ok := environment["CIRCLE_PIPELINE_ID"]; ok {
		t.Fatalf("signed duplicate was collected: %#v", environment)
	}
	if _, ok := environment["CIRCLE_USERNAME"]; ok {
		t.Fatalf("generic process context was collected: %#v", environment)
	}
	pullRequests, ok := environment["CIRCLE_PULL_REQUESTS"].([]string)
	if !ok || len(pullRequests) != 2 {
		t.Fatalf("pull requests = %#v", environment["CIRCLE_PULL_REQUESTS"])
	}
	if got := ClientContext(ProviderGitHub, mapLookup(map[string]string{
		"CIRCLE_BUILD_NUM": "42",
	})); got != nil {
		t.Fatalf("non-CircleCI context = %#v", got)
	}
}

func TestRequestGitHubOIDCToken(t *testing.T) {
	var gotAudience string
	var gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAudience = request.URL.Query().Get("audience")
		gotAuthorization = request.Header.Get("Authorization")
		_, _ = writer.Write([]byte(`{"value":"github-oidc-token"}`))
	}))
	defer server.Close()

	token, err := RequestGitHubOIDCToken(
		context.Background(),
		"https://api.packagemaze.com",
		mapLookup(map[string]string{
			"ACTIONS_ID_TOKEN_REQUEST_URL":   server.URL + "?existing=true",
			"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runtime-token",
		}),
		server.Client(),
	)
	if err != nil {
		t.Fatalf("RequestGitHubOIDCToken returned error: %v", err)
	}
	if token != "github-oidc-token" {
		t.Fatalf("token = %q", token)
	}
	if gotAudience != "https://api.packagemaze.com" {
		t.Fatalf("audience = %q", gotAudience)
	}
	if gotAuthorization != "bearer runtime-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
}

func TestRequestGitHubOIDCTokenMissingPermissions(t *testing.T) {
	_, err := RequestGitHubOIDCToken(
		context.Background(),
		"https://api.packagemaze.com",
		mapLookup(map[string]string{"GITHUB_ACTIONS": "true"}),
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "id-token: write") {
		t.Fatalf("expected id-token guidance, got %v", err)
	}
}

func TestRequestCircleCITokenUsesCLIWhenAvailable(t *testing.T) {
	gotName := ""
	gotArgs := []string{}
	token, err := RequestCircleCIToken(
		context.Background(),
		"https://api.packagemaze.com",
		func(name string) (string, error) { return "/usr/bin/" + name, nil },
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName = name
			gotArgs = append([]string{}, args...)
			return []byte("circle-token\n"), nil
		},
	)
	if err != nil {
		t.Fatalf("RequestCircleCIToken returned error: %v", err)
	}
	if token != "circle-token" {
		t.Fatalf("token = %q", token)
	}
	if gotName != "circleci" {
		t.Fatalf("command name = %q", gotName)
	}
	if strings.Join(gotArgs, " ") != `run oidc get --claims {"aud":"https://api.packagemaze.com"}` {
		t.Fatalf("command args = %#v", gotArgs)
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
