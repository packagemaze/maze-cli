package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/auth"
)

func TestExchangeOIDCOneFeedRequestBodyIsUnchanged(t *testing.T) {
	_, body, err := runExchange(t, http.StatusOK, `{"token":"maze_ci_real","expires_at":"2026-06-08T12:30:00Z"}`, "--feed", "your-org/npm", "--purpose", "install")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{"provider":"manual","feed":"your-org/npm","purpose":"install","package":null,"audience":"https://api.packagemaze.com","oidc_token":"manual-oidc"}`
	if body != want {
		t.Fatalf("request body = %s\nwant           %s", body, want)
	}
}

const severalFeedsResponse = `{"build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feeds":[{"artifact_protocol":"npm","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm"},{"artifact_protocol":"pypi","feed":"acme/pypi","feed_base_url":"https://pkg.packagemaze.com/acme/pypi"}],"purpose":"install","scopes":["read"],"token":"maze_ci_real","token_type":"Bearer"}`

func TestExchangeOIDCSeveralFeedsSendsFeedsAndPrintsTheToken(t *testing.T) {
	stdout, body, err := runExchange(t, http.StatusOK, severalFeedsResponse, "--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{"provider":"manual","purpose":"install","package":null,"audience":"https://api.packagemaze.com","oidc_token":"manual-oidc","feeds":["acme/npm","acme/pypi"]}`
	if body != want {
		t.Fatalf("request body = %s\nwant           %s", body, want)
	}
	if stdout != "maze_ci_real\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExchangeOIDCSeveralFeedsJSONCarriesTheServerFeeds(t *testing.T) {
	stdout, _, err := runExchange(t, http.StatusOK, severalFeedsResponse, "--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install", "--json")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	want := `{
  "token": "maze_ci_real",
  "expires_at": "2026-11-10T10:00:00Z",
  "token_type": "Bearer",
  "purpose": "install",
  "build_number": 7,
  "build_url": "https://www.packagemaze.com/acme/builds/7",
  "scopes": [
    "read"
  ],
  "provider": "manual",
  "feeds": [
    {
      "artifact_protocol": "npm",
      "feed": "acme/npm",
      "feed_base_url": "https://pkg.packagemaze.com/acme/npm"
    },
    {
      "artifact_protocol": "pypi",
      "feed": "acme/pypi",
      "feed_base_url": "https://pkg.packagemaze.com/acme/pypi"
    }
  ]
}
`
	if stdout != want {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestExchangeOIDCSeveralFeedsSurfacesAnOlderServersRefusal(t *testing.T) {
	_, _, err := runExchange(t, http.StatusUnprocessableEntity,
		`{"detail":[{"type":"missing","loc":["body","feed"],"msg":"Field required"},{"type":"extra_forbidden","loc":["body","feeds"],"msg":"Extra inputs are not permitted"}]}`,
		"--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install")
	want := "HTTP 422: feed: Field required; feeds: Extra inputs are not permitted"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

func TestExchangeOIDCSeveralFeedsRefusalNamesEveryFeed(t *testing.T) {
	_, _, err := runExchange(t, http.StatusUnauthorized, `{"detail":"No access rule matches."}`, "--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install")
	if err == nil || !strings.Contains(err.Error(), "Feed:\n  acme/npm, acme/pypi\n") {
		t.Fatalf("err = %v", err)
	}
}

// runExchange runs an exchange against a server that answers with the
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
		append([]string{"auth", "exchange-oidc", "--provider", "manual", "--api-url", server.URL, "--allow-insecure-localhost"}, args...)...,
	)
	return stdout, body, err
}
