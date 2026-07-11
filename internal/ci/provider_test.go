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
