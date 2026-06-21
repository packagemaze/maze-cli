package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientExchangeCISendsRequestAndParsesResponse(t *testing.T) {
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
		_, _ = writer.Write([]byte(`{
			"token":"maze_ci_token",
			"expires_at":"2026-06-08T12:30:00Z",
			"token_type":"Bearer",
			"feed":"your-org/npm",
			"purpose":"install",
			"scopes":["read"]
		}`))
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", server.Client())
	response, err := client.ExchangeCI(context.Background(), CITokenRequest{
		Provider:  "github",
		Feed:      "your-org/npm",
		Purpose:   "install",
		Audience:  "https://api.packagemaze.com",
		OIDCToken: "oidc-secret",
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
	if response.Token != "maze_ci_token" {
		t.Fatalf("token = %q", response.Token)
	}
	if response.ExpiresAt.Format(time.RFC3339) != "2026-06-08T12:30:00Z" {
		t.Fatalf("expires_at = %s", response.ExpiresAt.Format(time.RFC3339))
	}
	if strings.Join(response.Scopes, ",") != "read" {
		t.Fatalf("scopes = %#v", response.Scopes)
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
