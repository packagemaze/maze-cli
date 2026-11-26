package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

type receivedExchange struct {
	body          map[string]json.RawMessage
	authorization string
}

// exchangeBackend answers every exchange with response and hands each request
// it received to the returned channel.
func exchangeBackend(t *testing.T, response string) (*httptest.Server, chan receivedExchange) {
	t.Helper()
	received := make(chan receivedExchange, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		var body map[string]json.RawMessage
		if request.URL.Path != "/v1/auth/workload-token" || json.Unmarshal(content, &body) != nil {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		received <- receivedExchange{body: body, authorization: request.Header.Get("Authorization")}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, response)
	}))
	t.Cleanup(server.Close)
	return server, received
}

// jobEnvironment is this process's environment without anything that would
// make maze detect a built-in platform or find an identity token, plus extra.
func jobEnvironment(extra ...string) []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch {
		case name == "GITHUB_ACTIONS", name == "CIRCLECI", strings.HasPrefix(name, "MAZE_"), strings.HasPrefix(name, "PACKAGEMAZE_"):
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

func runMaze(t *testing.T, env []string, stdin string, args ...string) (string, string) {
	t.Helper()
	command := exec.Command("go", append([]string{"-C", packageMazeCLIDir(t), "run", "./cmd/maze"}, args...)...)
	command.Env = env
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("maze failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func requireReceived(t *testing.T, received chan receivedExchange) receivedExchange {
	t.Helper()
	select {
	case request := <-received:
		return request
	default:
		t.Fatalf("the backend received no exchange request")
		return receivedExchange{}
	}
}

// requireRetrySecret is the exchange's retry secret: 32 random bytes in
// base64url without padding.
func requireRetrySecret(t *testing.T, request receivedExchange) string {
	t.Helper()
	var secret string
	if err := json.Unmarshal(request.body["retry_secret"], &secret); err != nil || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(secret) {
		t.Fatalf("retry_secret = %s, want 43 base64url characters", request.body["retry_secret"])
	}
	return secret
}

func TestExchangeOIDCSuppliedIdentityTokenLetsTheIssuerSelectAgainstLocalBackendAPI(t *testing.T) {
	t.Parallel()
	const identityToken = "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJodHRwczovL2dpdGxhYi5leGFtcGxlIn0.signature"
	fixture := loadCITokenContractFixture(t)
	server, received := exchangeBackend(t, string(fixture.Success))

	stdout, stderr := runMaze(t, jobEnvironment("GITLAB_CI=true", "MAZE_IDENTITY_TOKEN="+identityToken), "",
		"auth", "exchange-oidc", "--api-url", server.URL+"/v1", "--allow-insecure-localhost",
		"--feed", "your-org/npm", "--purpose", "install", "--format", "json", "--verbose")

	request := requireReceived(t, received)
	if _, named := request.body["provider"]; named {
		t.Fatalf("provider = %s, want it omitted", request.body["provider"])
	}
	if string(request.body["oidc_token"]) != `"`+identityToken+`"` {
		t.Fatalf("oidc_token was not forwarded")
	}
	if strings.Contains(stdout+stderr, identityToken) {
		t.Fatalf("identity token leaked into output")
	}
	if secret := requireRetrySecret(t, request); strings.Contains(stdout+stderr, secret) {
		t.Fatalf("retry secret leaked into output")
	}
	if stderr != "PackageMaze OIDC exchange provider: none named; the supplied identity token's issuer selects it\n" {
		t.Fatalf("stderr = %q", stderr)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout)
	}
	if _, named := output["provider"]; named || output["token"] != "maze_ci_fixture_token" {
		t.Fatalf("output = %s", stdout)
	}
}

func TestExchangeOIDCInjectedIdentityAgainstLocalBackendAPI(t *testing.T) {
	t.Parallel()
	fixture := loadCITokenContractFixture(t)
	server, received := exchangeBackend(t, string(fixture.Success))

	stdout, _ := runMaze(t, jobEnvironment(), "",
		"auth", "exchange-oidc", "--api-url", server.URL+"/v1", "--allow-insecure-localhost",
		"--identity-token-injected", "--feed", "your-org/npm", "--purpose", "install")

	request := requireReceived(t, received)
	for _, field := range []string{"provider", "oidc_token"} {
		if _, sent := request.body[field]; sent {
			t.Fatalf("%s = %s, want it omitted", field, request.body[field])
		}
	}
	if request.authorization != "" {
		t.Fatalf("maze sent Authorization %q; the platform adds it", request.authorization)
	}
	if string(request.body["audience"]) != `"https://api.packagemaze.com"` {
		t.Fatalf("audience = %s", request.body["audience"])
	}
	requireRetrySecret(t, request)
	if stdout != "maze_ci_fixture_token\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExchangeOIDCSeveralFeedsShellAgainstLocalBackendAPI(t *testing.T) {
	t.Parallel()
	const response = `{"build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feeds":[{"artifact_protocol":"npm","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm"},{"artifact_protocol":"pypi","feed":"acme/pypi","feed_base_url":"https://pkg.packagemaze.com/acme/pypi"}],"purpose":"install","scopes":["read"],"token":"maze_ci_several","token_type":"Bearer"}`
	server, received := exchangeBackend(t, response)

	stdout, stderr := runMaze(t, jobEnvironment(), "platform-identity-token\n",
		"auth", "exchange-oidc", "--api-url", server.URL+"/v1", "--allow-insecure-localhost",
		"--oidc-token-stdin", "--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install", "--format", "shell")

	request := requireReceived(t, received)
	if string(request.body["feeds"]) != `["acme/npm","acme/pypi"]` {
		t.Fatalf("feeds = %s", request.body["feeds"])
	}
	want := "export MAZE_TOKEN='maze_ci_several'\n" +
		"export MAZE_TOKEN_EXPIRES_AT='2026-11-10T10:00:00Z'\n" +
		"export MAZE_FEEDS='acme/npm acme/pypi'\n" +
		"export MAZE_PURPOSE='install'\n" +
		"export MAZE_BUILD_NUMBER='7'\n" +
		"export MAZE_BUILD_URL='https://www.packagemaze.com/acme/builds/7'\n"
	if stdout != want || stderr != "" {
		t.Fatalf("stdout =\n%s\nstderr = %q", stdout, stderr)
	}
}
