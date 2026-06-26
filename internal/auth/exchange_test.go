package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/output"
)

func TestResolveValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name:    "missing feed",
			config:  Config{Provider: "manual", Purpose: "install"},
			wantErr: "--feed",
		},
		{
			name:    "invalid feed",
			config:  Config{Provider: "manual", Feed: "missing-slash", Purpose: "install"},
			wantErr: "org/feed",
		},
		{
			name:    "invalid purpose",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "deploy"},
			wantErr: "--purpose",
		},
		{
			name:    "publish missing package",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "publish"},
			wantErr: "--package is required",
		},
		{
			name:    "package rejected for install",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", Package: "pkg"},
			wantErr: "only supported",
		},
		{
			name:    "invalid url",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", BaseURL: "not-a-url"},
			wantErr: "absolute URL",
		},
		{
			name:    "http rejected by default",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", BaseURL: "http://127.0.0.1:8787"},
			wantErr: "must use https",
		},
		{
			name:    "invalid output format",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", Format: "yaml"},
			wantErr: "format",
		},
		{
			name: "github output name collides with artifact protocol metadata",
			config: Config{
				Provider:   "manual",
				Feed:       "your-org/npm",
				Purpose:    "install",
				Format:     string(output.FormatGitHubOutput),
				OutputName: "artifact_protocol",
			},
			wantErr: "reserved",
		},
		{
			name: "github output name collides with feed base url metadata",
			config: Config{
				Provider:   "manual",
				Feed:       "your-org/npm",
				Purpose:    "install",
				Format:     string(output.FormatGitHubOutput),
				OutputName: "feed_base_url",
			},
			wantErr: "reserved",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(test.config, Dependencies{Env: mapLookup(nil)})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestResolveAllowsInsecureLocalhostOnlyWithFlag(t *testing.T) {
	resolved, err := Resolve(Config{
		Feed:                   "your-org/npm",
		Purpose:                "install",
		Provider:               "manual",
		BaseURL:                "http://127.0.0.1:8787",
		AllowInsecureLocalhost: true,
	}, Dependencies{Env: mapLookup(nil)})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.APIURL != "http://127.0.0.1:8787/v1" {
		t.Fatalf("APIURL = %q", resolved.APIURL)
	}
}

func TestExchangeManualEnvTokenCallsBackend(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	result, _, err := Exchange(context.Background(), Config{
		Feed:     "your-org/npm",
		Purpose:  "publish",
		Package:  "your-package",
		Provider: "manual",
	}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	if !exchanger.called {
		t.Fatalf("backend was not called")
	}
	if exchanger.request.OIDCToken != "manual-oidc" {
		t.Fatalf("oidc token = %q", exchanger.request.OIDCToken)
	}
	if exchanger.request.Package == nil || *exchanger.request.Package != "your-package" {
		t.Fatalf("package = %#v", exchanger.request.Package)
	}
	if result.Token != "maze_ci_real" {
		t.Fatalf("token = %q", result.Token)
	}
	if result.ArtifactProtocol != "npm" {
		t.Fatalf("artifact_protocol = %q", result.ArtifactProtocol)
	}
	if result.FeedBaseURL != "https://pkg.packagemaze.com/your-org/npm" {
		t.Fatalf("feed_base_url = %q", result.FeedBaseURL)
	}
}

func TestExchangeGitLabMissingTokenPrintsSnippet(t *testing.T) {
	_, _, err := Exchange(context.Background(), Config{
		Feed:     "your-org/npm",
		Purpose:  "install",
		Provider: "gitlab",
	}, Dependencies{Env: mapLookup(nil)})
	if err == nil || !strings.Contains(err.Error(), "id_tokens") {
		t.Fatalf("expected GitLab snippet, got %v", err)
	}
}

func TestResolveGitHubOutputRequiresActions(t *testing.T) {
	_, err := Resolve(Config{
		Feed:     "your-org/npm",
		Purpose:  "install",
		Provider: "manual",
		Format:   string(output.FormatGitHubOutput),
	}, Dependencies{Env: mapLookup(map[string]string{"GITHUB_OUTPUT": "/tmp/out"})})
	if err == nil || !strings.Contains(err.Error(), "GitHub Actions") {
		t.Fatalf("expected GitHub Actions error, got %v", err)
	}
}

type recordingExchanger struct {
	t       *testing.T
	called  bool
	request api.CITokenRequest
}

func (f *recordingExchanger) ExchangeCI(_ context.Context, request api.CITokenRequest) (api.CITokenResponse, error) {
	f.called = true
	f.request = request
	return api.CITokenResponse{
		Token:            "maze_ci_real",
		ExpiresAt:        fixedClock().Add(time.Hour),
		TokenType:        "Bearer",
		Feed:             request.Feed,
		FeedBaseURL:      "https://pkg.packagemaze.com/" + request.Feed,
		Purpose:          request.Purpose,
		Scopes:           []string{"publish"},
		ArtifactProtocol: "npm",
	}, nil
}

func fixedClock() time.Time {
	return time.Date(2026, 6, 8, 12, 30, 0, 0, time.UTC)
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
