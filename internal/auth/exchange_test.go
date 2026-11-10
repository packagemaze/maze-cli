package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/ci"
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
			name:    "setup invocation id is too long",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", SetupInvocationID: strings.Repeat("a", 161)},
			wantErr: "--setup-invocation-id",
		},
		{
			name:    "setup invocation id rejects whitespace",
			config:  Config{Provider: "manual", Feed: "your-org/npm", Purpose: "install", SetupInvocationID: "setup maze 1"},
			wantErr: "--setup-invocation-id",
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
		{
			name: "github output name collides with Build metadata",
			config: Config{
				Provider:   "manual",
				Feed:       "your-org/npm",
				Purpose:    "install",
				Format:     string(output.FormatGitHubOutput),
				OutputName: "build_number",
			},
			wantErr: "reserved",
		},
		{
			name: "github output name collides with Build URL metadata",
			config: Config{
				Provider:   "manual",
				Feed:       "your-org/npm",
				Purpose:    "install",
				Format:     string(output.FormatGitHubOutput),
				OutputName: "build_url",
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
	if result.BuildNumber != 482 || result.BuildURL != "https://www.packagemaze.com/your-org/builds/482" {
		t.Fatalf("Build reference = %#v", result)
	}
}

func TestResolveSetupInvocationIDFlagPrecedesEnvironment(t *testing.T) {
	resolved, err := Resolve(Config{
		Feed:              "your-org/npm",
		Purpose:           "install",
		Provider:          "manual",
		SetupInvocationID: "setup-maze_flag-id",
	}, Dependencies{Env: mapLookup(map[string]string{
		"MAZE_SETUP_INVOCATION_ID": "setup-maze_environment-id",
	})})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.SetupInvocationID != "setup-maze_flag-id" {
		t.Fatalf("setup invocation id = %q", resolved.SetupInvocationID)
	}
}

func TestResolveSetupInvocationIDFromEnvironment(t *testing.T) {
	resolved, err := Resolve(Config{
		Feed:     "your-org/npm",
		Purpose:  "install",
		Provider: "manual",
	}, Dependencies{Env: mapLookup(map[string]string{
		"MAZE_SETUP_INVOCATION_ID": "circleci-maze-orb_environment-id",
	})})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.SetupInvocationID != "circleci-maze-orb_environment-id" {
		t.Fatalf("setup invocation id = %q", resolved.SetupInvocationID)
	}
}

func TestExchangeForwardsTypedSetupInvocationID(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{
		Feed:              "your-org/npm",
		Provider:          "manual",
		Purpose:           "install",
		SetupInvocationID: "setup-maze_0123456789abcdef0123456789abcdef",
	}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	if exchanger.request.SetupInvocationID != "setup-maze_0123456789abcdef0123456789abcdef" {
		t.Fatalf("setup invocation id = %q", exchanger.request.SetupInvocationID)
	}
}

func TestExchangeForwardsExplicitLegacyClientContext(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{
		ClientContextJSON: `{"ci":{"run_url":"https://example.com/run/1"}}`,
		Feed:              "your-org/npm",
		Provider:          "manual",
		Purpose:           "install",
	}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	ciContext, ok := exchanger.request.Client["ci"].(map[string]any)
	if !ok {
		t.Fatalf("client ci context = %#v", exchanger.request.Client["ci"])
	}
	if ciContext["run_url"] != "https://example.com/run/1" {
		t.Fatalf("client context = %#v", exchanger.request.Client)
	}
}

func TestExchangeDoesNotCollectCIEnvironmentAsClientMetadata(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{
		Feed:           "your-org/npm",
		OIDCTokenStdin: true,
		Provider:       "github",
		Purpose:        "install",
	}, Dependencies{
		Env: mapLookup(map[string]string{
			"GITHUB_ACTIONS":    "true",
			"GITHUB_REPOSITORY": "packagemaze/maze-cli",
			"GITHUB_RUN_ID":     "12345",
		}),
		Stdin:     strings.NewReader("github-oidc-token"),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	if exchanger.request.Client != nil {
		t.Fatalf("automatic client metadata = %#v, want nil", exchanger.request.Client)
	}
}

func TestExchangeCollectsCircleCIEnvironmentContext(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{
		Feed:           "your-org/npm",
		OIDCTokenStdin: true,
		Provider:       "circleci",
		Purpose:        "install",
	}, Dependencies{
		Env: mapLookup(map[string]string{
			"CIRCLECI":           "true",
			"CIRCLE_BUILD_NUM":   "42",
			"CIRCLE_JOB":         "package-client-smoke",
			"CIRCLE_PIPELINE_ID": "must-not-be-collected",
			"CIRCLE_USERNAME":    "must-not-be-collected",
		}),
		Stdin:     strings.NewReader("circleci-oidc-token"),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	environment, ok := exchanger.request.Client["circleci_environment"].(map[string]any)
	if !ok {
		t.Fatalf("CircleCI environment = %#v", exchanger.request.Client)
	}
	if environment["CIRCLE_BUILD_NUM"] != "42" || environment["CIRCLE_JOB"] != "package-client-smoke" {
		t.Fatalf("CircleCI environment = %#v", environment)
	}
	if len(environment) != 2 {
		t.Fatalf("CircleCI allowlist = %#v", environment)
	}
}

func TestExchangeRejectsInvalidClientContext(t *testing.T) {
	tests := []string{
		`{`,
		`[]`,
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			_, _, err := Exchange(context.Background(), Config{
				ClientContextJSON: value,
				Feed:              "your-org/npm",
				Provider:          "manual",
				Purpose:           "install",
			}, Dependencies{
				Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
				Exchanger: &recordingExchanger{t: t},
			})
			if err == nil || !strings.Contains(err.Error(), "--client-context-json") {
				t.Fatalf("expected client context error, got %v", err)
			}
		})
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
		ExchangePurpose:  request.Purpose,
		BuildNumber:      482,
		BuildURL:         "https://www.packagemaze.com/your-org/builds/482",
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

func TestExchangeOIDCProviderSendsSuppliedTokenAsOIDC(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, resolved, err := Exchange(context.Background(), Config{
		Feed:           "your-org/npm",
		Purpose:        "install",
		Provider:       "oidc",
		OIDCTokenStdin: true,
	}, Dependencies{
		// A detected CI must not override an explicit --provider oidc.
		Env:       mapLookup(map[string]string{"GITHUB_ACTIONS": "true"}),
		Stdin:     strings.NewReader("platform-identity\n"),
		Exchanger: exchanger,
	})
	if err != nil {
		t.Fatalf("Exchange returned error: %v", err)
	}
	if exchanger.request.Provider != "oidc" {
		t.Fatalf("provider = %q", exchanger.request.Provider)
	}
	if exchanger.request.OIDCToken != "platform-identity" {
		t.Fatalf("oidc token = %q", exchanger.request.OIDCToken)
	}
	if exchanger.request.Audience != "https://api.packagemaze.com" {
		t.Fatalf("audience = %q", exchanger.request.Audience)
	}
	if exchanger.request.Client != nil {
		t.Fatalf("client context = %#v", exchanger.request.Client)
	}
	if resolved.ProviderValue != ci.ProviderOIDC {
		t.Fatalf("resolved provider = %q", resolved.ProviderValue)
	}
}

func TestExchangeOIDCProviderWithoutTokenSaysWhatToPass(t *testing.T) {
	exchanger := &recordingExchanger{t: t}
	_, _, err := Exchange(context.Background(), Config{
		Feed:     "your-org/npm",
		Purpose:  "install",
		Provider: "oidc",
	}, Dependencies{
		Env:       mapLookup(map[string]string{}),
		Exchanger: exchanger,
	})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if exchanger.called {
		t.Fatalf("backend was called without a token")
	}
	for _, want := range []string{"https://api.packagemaze.com", "--oidc-token-stdin", "MAZE_OIDC_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestResolveReportedValues(t *testing.T) {
	text := func(value string) *string { return &value }
	tests := []struct {
		name       string
		ref        *string
		commit     *string
		wantErr    string
		wantRef    *string
		wantCommit *string
	}{
		{name: "neither given"},
		{name: "values are trimmed", ref: text("  main \n"), commit: text("\tabc123 "), wantRef: text("main"), wantCommit: text("abc123")},
		{name: "empty ref", ref: text(""), wantErr: "--ref must not be empty"},
		{name: "whitespace commit", commit: text("  \t"), wantErr: "--commit must not be empty"},
		{name: "256 characters", ref: text(strings.Repeat("r", 256)), wantRef: text(strings.Repeat("r", 256))},
		{name: "257 characters", commit: text(strings.Repeat("c", 257)), wantErr: "--commit must be at most 256 characters"},
		{name: "counted as the server counts", ref: text(strings.Repeat("\U0001F600", 129)), wantErr: "--ref must be at most 256 characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(Config{
				Provider: "manual", Feed: "your-org/npm", Purpose: "install",
				ReportedRef: test.ref, ReportedCommit: test.commit,
			}, Dependencies{Env: mapLookup(nil)})
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !equalOptional(resolved.ReportedRef, test.wantRef) || !equalOptional(resolved.ReportedCommit, test.wantCommit) {
				t.Fatalf("reported = %v, %v", resolved.ReportedRef, resolved.ReportedCommit)
			}
		})
	}
}

func equalOptional(got *string, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}
