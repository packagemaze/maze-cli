package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestCircleCIClientContext(t *testing.T) {
	context := ClientContext(ProviderCircleCI, mapLookup(map[string]string{
		"CIRCLE_BRANCH":          "codex/circleci-packagemaze",
		"CIRCLE_BUILD_URL":       "https://app.circleci.com/jobs/circleci/org/project/26",
		"CIRCLE_JOB":             "test-node",
		"CIRCLE_PIPELINE_ID":     "495aad4a-fed2-4e38-801d-9bbab7e57ee5",
		"CIRCLE_PULL_REQUESTS":   "https://github.com/allwhat/marko/pull/1, https://github.com/allwhat/marko/pull/2",
		"CIRCLE_REPOSITORY_URL":  "https://github.com/allwhat/marko",
		"CIRCLE_SHA1":            "6860007fd655fdb7d54b97095dd14113b48dc059",
		"CIRCLE_WORKFLOW_ID":     "820ad10e-75fa-4f66-8891-ea06e3c061b9",
		"CIRCLE_WORKFLOW_JOB_ID": "39652d1f-9b3b-4587-bcd7-8dcd5c9f3415",
	}))

	ciContext, ok := context["ci"].(map[string]any)
	if !ok {
		t.Fatalf("ci context = %#v", context["ci"])
	}
	if ciContext["branch"] != "codex/circleci-packagemaze" {
		t.Fatalf("branch = %#v", ciContext["branch"])
	}
	if ciContext["sha"] != "6860007fd655fdb7d54b97095dd14113b48dc059" {
		t.Fatalf("sha = %#v", ciContext["sha"])
	}
	pullRequests, ok := ciContext["pull_requests"].([]string)
	if !ok || len(pullRequests) != 2 || pullRequests[0] != "https://github.com/allwhat/marko/pull/1" {
		t.Fatalf("pull_requests = %#v", ciContext["pull_requests"])
	}
}

func TestGitHubClientContextReadsPullRequestEvent(t *testing.T) {
	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{
		"pull_request": {
			"number": 12,
			"title": "Show CI Session context",
			"html_url": "https://github.com/packagemaze/packagemaze/pull/12"
		}
	}`), 0o600); err != nil {
		t.Fatalf("write event: %v", err)
	}

	context := ClientContext(ProviderGitHub, mapLookup(map[string]string{
		"GITHUB_EVENT_PATH":  eventPath,
		"GITHUB_REF":         "refs/pull/12/merge",
		"GITHUB_REPOSITORY":  "packagemaze/packagemaze",
		"GITHUB_RUN_ATTEMPT": "2",
		"GITHUB_RUN_ID":      "28769770191",
		"GITHUB_SERVER_URL":  "https://github.com",
		"GITHUB_SHA":         "abcdef1234567890",
	}))

	ciContext, ok := context["ci"].(map[string]any)
	if !ok {
		t.Fatalf("ci context = %#v", context["ci"])
	}
	if ciContext["pull_request_title"] != "Show CI Session context" {
		t.Fatalf("pull_request_title = %#v", ciContext["pull_request_title"])
	}
	if ciContext["pull_request_number"] != "12" {
		t.Fatalf("pull_request_number = %#v", ciContext["pull_request_number"])
	}
	if ciContext["run_url"] != "https://github.com/packagemaze/packagemaze/actions/runs/28769770191/attempts/2" {
		t.Fatalf("run_url = %#v", ciContext["run_url"])
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
