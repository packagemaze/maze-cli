package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ciTokenContractFixture struct {
	Request         json.RawMessage `json:"request"`
	Success         json.RawMessage `json:"success"`
	Rejected        json.RawMessage `json:"rejected"`
	ValidationError json.RawMessage `json:"validation_error"`
}

func TestCITokenRequestMatchesPackageMazeContractFixture(t *testing.T) {
	fixture := loadCITokenContractFixture(t)
	var expected map[string]any
	if err := json.Unmarshal(fixture.Request, &expected); err != nil {
		t.Fatalf("decode fixture request: %v", err)
	}

	actualJSON, err := json.Marshal(CITokenRequest{
		Provider:          "manual",
		Feed:              "your-org/npm",
		Purpose:           "install",
		Audience:          "https://api.packagemaze.com",
		OIDCToken:         "fixture-oidc-token",
		SetupInvocationID: "setup-maze_0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var actual map[string]any
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		t.Fatalf("decode encoded request: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("request contract mismatch:\nactual:   %#v\nexpected: %#v", actual, expected)
	}
}

func TestClientExchangeCISendsRequestAndParsesResponse(t *testing.T) {
	fixture := loadCITokenContractFixture(t)
	var gotPath string
	var gotRequest CITokenRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(request.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(fixture.Success)
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", server.Client())
	response, err := client.ExchangeCI(context.Background(), CITokenRequest{
		Provider:          "github",
		Feed:              "your-org/npm",
		Purpose:           "install",
		Audience:          "https://api.packagemaze.com",
		OIDCToken:         "oidc-secret",
		SetupInvocationID: "setup-maze_0123456789abcdef0123456789abcdef",
		Client: map[string]any{
			"ci": map[string]any{"sha": "abcdef123456"},
		},
	})
	if err != nil {
		t.Fatalf("ExchangeCI returned error: %v", err)
	}
	if gotPath != "/v1/auth/ci-token" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotRequest.OIDCToken != "oidc-secret" {
		t.Fatalf("oidc_token = %q", gotRequest.OIDCToken)
	}
	if gotRequest.Client["ci"].(map[string]any)["sha"] != "abcdef123456" {
		t.Fatalf("client = %#v", gotRequest.Client)
	}
	if gotRequest.SetupInvocationID != "setup-maze_0123456789abcdef0123456789abcdef" {
		t.Fatalf("setup_invocation_id = %q", gotRequest.SetupInvocationID)
	}
	if response.Token != "maze_ci_fixture_token" {
		t.Fatalf("token = %q", response.Token)
	}
	if response.ExpiresAt.Format(time.RFC3339) != "2026-06-08T12:30:00Z" {
		t.Fatalf("expires_at = %s", response.ExpiresAt.Format(time.RFC3339))
	}
	if strings.Join(response.Scopes, ",") != "read" {
		t.Fatalf("scopes = %#v", response.Scopes)
	}
	if response.ArtifactProtocol != "npm" {
		t.Fatalf("artifact_protocol = %q", response.ArtifactProtocol)
	}
	if response.FeedBaseURL != "https://pkg.packagemaze.com/your-org/npm" {
		t.Fatalf("feed_base_url = %q", response.FeedBaseURL)
	}
	if response.ExchangePurpose != "install" {
		t.Fatalf("exchange purpose = %q", response.ExchangePurpose)
	}
	if response.BuildNumber != 482 || response.BuildURL != "https://www.packagemaze.com/your-org/builds/482" {
		t.Fatalf("Build reference = %#v", response)
	}
}

func TestClientExchangeCIPrefersExplicitExchangePurpose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"purpose":"cicd",
			"exchange_purpose":"publish",
			"scopes":["publish"]
		}`))
	}))
	defer server.Close()

	response, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "publish", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err != nil {
		t.Fatalf("ExchangeCI returned error: %v", err)
	}
	if response.ExchangePurpose != "publish" {
		t.Fatalf("exchange purpose = %q", response.ExchangePurpose)
	}
}

func TestClientExchangeCIDoesNotExposeStoredTokenPurposeAsExchangePurpose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"purpose":"cicd",
			"scopes":["read"]
		}`))
	}))
	defer server.Close()

	response, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err != nil {
		t.Fatalf("ExchangeCI returned error: %v", err)
	}
	if response.ExchangePurpose != "install" {
		t.Fatalf("exchange purpose = %q", response.ExchangePurpose)
	}
}

func TestClientExchangeCIAcceptsBuildReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"purpose":"install",
			"build_number":482,
			"build_url":"https://www.packagemaze.com/your-org/builds/482"
		}`))
	}))
	defer server.Close()

	response, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err != nil {
		t.Fatalf("ExchangeCI returned error: %v", err)
	}
	if response.BuildNumber != 482 || response.BuildURL != "https://www.packagemaze.com/your-org/builds/482" {
		t.Fatalf("Build reference = %#v", response)
	}
}

func TestClientExchangeCIRejectsIncompleteBuildReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"purpose":"install",
			"build_number":482
		}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "must be returned together") {
		t.Fatalf("expected incomplete Build reference error, got %v", err)
	}
}

func TestClientExchangeCIRejectsInvalidBuildURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"purpose":"install",
			"build_number":482,
			"build_url":"/your-org/builds/482"
		}`))
	}))
	defer server.Close()

	_, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute URL") {
		t.Fatalf("expected invalid Build URL error, got %v", err)
	}
	var contractError *ContractResponseError
	if !errors.As(err, &contractError) {
		t.Fatalf("expected ContractResponseError, got %T", err)
	}
}

func TestClientExchangeCIStatusErrors(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: http.StatusBadRequest, want: "HTTP 400"},
		{status: http.StatusUnauthorized, want: "rejected this CI identity"},
		{status: http.StatusForbidden, want: "rejected this CI identity"},
		{status: http.StatusNotFound, want: "HTTP 404"},
		{status: http.StatusNotImplemented, want: "server returned HTTP 501"},
		{status: http.StatusInternalServerError, want: "server returned HTTP 500"},
	}

	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(`{"detail":"No OIDC trust rule matched this workflow."}`))
			}))
			defer server.Close()

			client := NewClient(server.URL+"/v1", server.Client())
			_, err := client.ExchangeCI(context.Background(), CITokenRequest{
				Provider:  "github",
				Feed:      "your-org/npm",
				Purpose:   "install",
				Audience:  "https://api.packagemaze.com",
				OIDCToken: "oidc-secret",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestClientExchangeCIStructuredWorkerError(t *testing.T) {
	fixture := loadCITokenContractFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write(fixture.Rejected)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "gitlab", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err == nil {
		t.Fatal("expected status error")
	}
	for _, want := range []string{
		"This CI OIDC provider is not supported yet.",
		"unsupported_provider",
		"Use GitHub Actions access or a scoped static CI Token fallback for this Feed.",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error containing %q, got %v", want, err)
		}
	}
}

func TestClientExchangeCIValidationErrorDoesNotPrintRawJSON(t *testing.T) {
	fixture := loadCITokenContractFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write(fixture.ValidationError)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, server.Client()).ExchangeCI(context.Background(), CITokenRequest{
		Provider: "manual", Feed: "your-org/npm", Purpose: "install", Audience: "https://api.packagemaze.com", OIDCToken: "oidc-secret",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "setup_invocation_id: String should have at most 160 characters") {
		t.Fatalf("validation detail = %v", err)
	}
	if strings.Contains(err.Error(), `\"input\"`) || strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("raw validation payload leaked into error: %v", err)
	}
}

func TestClientExchangeCIStatusErrorRedactsOIDCToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"detail":"echoed oidc-secret in a validation error"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", server.Client())
	_, err := client.ExchangeCI(context.Background(), CITokenRequest{
		Provider:  "github",
		Feed:      "your-org/npm",
		Purpose:   "install",
		Audience:  "https://api.packagemaze.com",
		OIDCToken: "oidc-secret",
	})
	if err == nil {
		t.Fatal("expected status error")
	}
	if strings.Contains(err.Error(), "oidc-secret") {
		t.Fatalf("OIDC token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("expected redacted marker, got %v", err)
	}
}

func TestClientExchangeCIMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", server.Client())
	_, err := client.ExchangeCI(context.Background(), CITokenRequest{
		Provider:  "github",
		Feed:      "your-org/npm",
		Purpose:   "install",
		Audience:  "https://api.packagemaze.com",
		OIDCToken: "oidc-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("expected malformed JSON error, got %v", err)
	}
}

func TestClientExchangeCINetworkError(t *testing.T) {
	client := NewClient("https://api.packagemaze.com/v1", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network unavailable")
		}),
	})
	_, err := client.ExchangeCI(context.Background(), CITokenRequest{
		Provider:  "github",
		Feed:      "your-org/npm",
		Purpose:   "install",
		Audience:  "https://api.packagemaze.com",
		OIDCToken: "oidc-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("expected network error, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func loadCITokenContractFixture(t *testing.T) ciTokenContractFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "contracts", "ci-token-exchange.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CI token contract fixture: %v", err)
	}
	var fixture ciTokenContractFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode CI token contract fixture: %v", err)
	}
	return fixture
}
