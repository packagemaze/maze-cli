package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExchangeOIDCAgainstLocalBackendAPI(t *testing.T) {
	t.Parallel()

	const oidcToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJhdWQiOiJwYWNrYWdlbWF6ZSJ9.signature"
	const packageMazeToken = "maze_ci_01K8PACKAGEMAZEREAL"

	requests := make(chan tokenExchangeRequest, 1)
	backendErrors := make(chan string, 1)
	recordBackendError := func(writer http.ResponseWriter, format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		select {
		case backendErrors <- message:
		default:
		}
		http.Error(writer, message, http.StatusInternalServerError)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			recordBackendError(writer, "method = %s, want POST", request.Method)
			return
		}
		if request.URL.Path != "/v1/auth/ci-token" {
			recordBackendError(writer, "path = %s, want /v1/auth/ci-token", request.URL.Path)
			return
		}
		if request.Header.Get("Content-Type") != "application/json" {
			recordBackendError(writer, "Content-Type = %q", request.Header.Get("Content-Type"))
			return
		}

		var payload tokenExchangeRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			recordBackendError(writer, "decode request: %v", err)
			return
		}
		requests <- payload

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"token": "` + packageMazeToken + `",
			"expires_at": "2026-06-09T16:30:00Z",
			"token_type": "Bearer",
			"feed": "your-org/npm",
			"feed_base_url": "https://pkg.packagemaze.com/your-org/npm",
			"purpose": "install",
			"scopes": ["read"],
			"artifact_protocol": "npm"
		}`))
	}))
	defer server.Close()

	cliDir := packageMazeCLIDir(t)
	command := exec.Command(
		"go",
		"-C",
		cliDir,
		"run",
		"./cmd/maze",
		"auth",
		"exchange-oidc",
		"--base-url",
		server.URL,
		"--api-url",
		server.URL+"/v1",
		"--allow-insecure-localhost",
		"--provider",
		"manual",
		"--feed",
		"your-org/npm",
		"--purpose",
		"install",
		"--format",
		"json",
	)
	command.Env = append(os.Environ(), "MAZE_OIDC_TOKEN="+oidcToken)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		t.Fatalf("command failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	select {
	case message := <-backendErrors:
		t.Fatalf("test backend rejected request: %s", message)
	default:
	}

	select {
	case payload := <-requests:
		if payload.Provider != "manual" {
			t.Fatalf("provider = %q", payload.Provider)
		}
		if payload.Feed != "your-org/npm" {
			t.Fatalf("feed = %q", payload.Feed)
		}
		if payload.Purpose != "install" {
			t.Fatalf("purpose = %q", payload.Purpose)
		}
		if payload.Package != nil {
			t.Fatalf("package = %#v, want nil", payload.Package)
		}
		if payload.Audience != server.URL {
			t.Fatalf("audience = %q, want %q", payload.Audience, server.URL)
		}
		if payload.OIDCToken != oidcToken {
			t.Fatalf("oidc_token was not forwarded to backend")
		}
	default:
		t.Fatalf("test backend did not receive token exchange request")
	}

	if strings.Contains(stdout.String(), oidcToken) || strings.Contains(stderr.String(), oidcToken) {
		t.Fatalf("OIDC token leaked into command output")
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q", stderr.String())
	}

	var output tokenExchangeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout.String())
	}
	if output.Token != packageMazeToken {
		t.Fatalf("token = %q", output.Token)
	}
	if output.ExpiresAt != "2026-06-09T16:30:00Z" {
		t.Fatalf("expires_at = %q", output.ExpiresAt)
	}
	if output.Provider != "manual" {
		t.Fatalf("provider = %q", output.Provider)
	}
	if output.ArtifactProtocol != "npm" {
		t.Fatalf("artifact_protocol = %q", output.ArtifactProtocol)
	}
	if output.FeedBaseURL != "https://pkg.packagemaze.com/your-org/npm" {
		t.Fatalf("feed_base_url = %q", output.FeedBaseURL)
	}
	if strings.Join(output.Scopes, ",") != "read" {
		t.Fatalf("scopes = %#v", output.Scopes)
	}
}

type tokenExchangeRequest struct {
	Provider  string  `json:"provider"`
	Feed      string  `json:"feed"`
	Purpose   string  `json:"purpose"`
	Package   *string `json:"package"`
	Audience  string  `json:"audience"`
	OIDCToken string  `json:"oidc_token"`
}

type tokenExchangeOutput struct {
	Token            string   `json:"token"`
	ExpiresAt        string   `json:"expires_at"`
	Provider         string   `json:"provider"`
	Scopes           []string `json:"scopes"`
	ArtifactProtocol string   `json:"artifact_protocol"`
	FeedBaseURL      string   `json:"feed_base_url"`
}

func packageMazeCLIDir(t *testing.T) string {
	t.Helper()
	cliDir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve CLI directory: %v", err)
	}
	return cliDir
}
