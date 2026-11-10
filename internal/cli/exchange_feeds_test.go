package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/packagemaze/maze-cli/internal/auth"
)

func TestExchangeOIDCOneFeedRequestBodyIsUnchanged(t *testing.T) {
	_, body, err := runExchange(t, http.StatusOK, `{"token":"maze_ci_real","expires_at":"2026-06-08T12:30:00Z"}`, "--feed", "your-org/npm")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{"provider":"manual","feed":"your-org/npm","purpose":"install","package":null,"audience":"https://api.packagemaze.com","oidc_token":"manual-oidc"}`
	if body != want {
		t.Fatalf("request body = %s\nwant           %s", body, want)
	}
}

// runExchange runs an install exchange against a server that answers with the
// given status and body, and returns stdout and the request body it received.
func runExchange(t *testing.T, status int, response string, args ...string) (string, string, error) {
	t.Helper()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		body = string(content)
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, response)
	}))
	defer server.Close()
	stdout, _, err := runCommandWithDeps(
		auth.Dependencies{Env: mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}), HTTPClient: server.Client()},
		append([]string{"auth", "exchange-oidc", "--provider", "manual", "--purpose", "install", "--api-url", server.URL, "--allow-insecure-localhost"}, args...)...,
	)
	return stdout, body, err
}
