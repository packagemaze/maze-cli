package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/auth"
)

var retrySecretField = regexp.MustCompile(`"retry_secret":"([^"]*)"`)

// retrySecretOf is the request body's retry secret, after checking it is 32
// bytes in base64url without padding.
func retrySecretOf(t *testing.T, body string) string {
	t.Helper()
	matches := retrySecretField.FindAllStringSubmatch(body, -1)
	if len(matches) != 1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(matches[0][1]) {
		t.Fatalf("request body carries no single 43-character base64url retry secret: %s", body)
	}
	return matches[0][1]
}

// maskRetrySecret replaces the random retry secret with <retry-secret>.
func maskRetrySecret(t *testing.T, body string) string {
	t.Helper()
	if body == "" {
		return ""
	}
	return strings.Replace(body, retrySecretOf(t, body), "<retry-secret>", 1)
}

// testClock advances only when maze sleeps.
type testClock struct {
	now   time.Time
	waits []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(_ context.Context, wait time.Duration) error {
	c.waits = append(c.waits, wait)
	c.now = c.now.Add(wait)
	return nil
}

// scriptedBackend answers each exchange with the next answer, and with the
// last one after that, and records every request body and Authorization.
type scriptedBackend struct {
	answers        []func(http.ResponseWriter)
	bodies         []string
	authorizations []string
}

func (b *scriptedBackend) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		b.bodies = append(b.bodies, string(content))
		b.authorizations = append(b.authorizations, request.Header.Get("Authorization"))
		b.answers[min(len(b.bodies), len(b.answers))-1](writer)
	}))
	t.Cleanup(server.Close)
	return server
}

func dropConnection(writer http.ResponseWriter) {
	connection, _, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		panic(err)
	}
	_ = connection.Close()
}

func answer(status int, retryAfter string, body string) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		if retryAfter != "" {
			writer.Header().Set("Retry-After", retryAfter)
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

func truncatedToken(writer http.ResponseWriter) {
	writer.Header().Set("Content-Length", "4096")
	_, _ = io.WriteString(writer, `{"token":"maze_ci_lost`)
}

const replayedRefusal = `{"detail":{"code":"oidc_assertion_replayed","message":"This identity token was already exchanged."}}`

var suppliedIdentity = map[string]string{"MAZE_OIDC_TOKEN": "platform-identity"}

// runScriptedExchange runs exchange-oidc against backend.
func runScriptedExchange(t *testing.T, backend *scriptedBackend, clock *testClock, env map[string]string, args ...string) (string, string, error) {
	t.Helper()
	server := backend.serve(t)
	command := NewRootCommand(auth.Dependencies{
		Env:        mapLookup(env),
		HTTPClient: server.Client(),
		Now:        clock.Now,
		Sleep:      clock.Sleep,
	})
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(append([]string{"auth", "exchange-oidc", "--api-url", server.URL, "--allow-insecure-localhost", "--feed", "acme/npm", "--purpose", "install"}, args...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func assertOneRequestResent(t *testing.T, bodies []string, attempts int) string {
	t.Helper()
	if len(bodies) != attempts {
		t.Fatalf("PackageMaze got %d requests, want %d", len(bodies), attempts)
	}
	for _, body := range bodies {
		if body != bodies[0] {
			t.Fatalf("a retry changed the request:\n%s\n%s", bodies[0], body)
		}
	}
	return retrySecretOf(t, bodies[0])
}

func TestExchangeOIDCResendsTheIdenticalRequestThroughRetryableFailures(t *testing.T) {
	backend := &scriptedBackend{answers: []func(http.ResponseWriter){
		answer(http.StatusTooManyRequests, "7", `{"detail":"Slow down."}`),
		dropConnection,
		answer(http.StatusBadGateway, "", ""),
		answer(http.StatusServiceUnavailable, "", ""),
		answer(http.StatusGatewayTimeout, "", ""),
		truncatedToken,
		answer(http.StatusOK, "", oneFeedResponse),
	}}
	clock := newTestClock()
	stdout, stderr, err := runScriptedExchange(t, backend, clock, suppliedIdentity, "--json", "--verbose")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	secret := assertOneRequestResent(t, backend.bodies, 7)
	if !strings.Contains(backend.bodies[0], `"oidc_token":"platform-identity"`) {
		t.Fatalf("request body = %s", backend.bodies[0])
	}
	want := []time.Duration{7 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second}
	if !reflect.DeepEqual(clock.waits, want) {
		t.Fatalf("waits = %v, want %v", clock.waits, want)
	}
	wantStderr := "PackageMaze answered HTTP 429; retrying the token exchange in 7s\n" +
		"PackageMaze gave no readable answer; retrying the token exchange in 2s\n" +
		"PackageMaze answered HTTP 502; retrying the token exchange in 4s\n" +
		"PackageMaze answered HTTP 503; retrying the token exchange in 8s\n" +
		"PackageMaze answered HTTP 504; retrying the token exchange in 15s\n" +
		"PackageMaze gave no readable answer; retrying the token exchange in 15s\n" +
		"PackageMaze OIDC exchange provider: none named; the supplied identity token's issuer selects it\n"
	if stderr != wantStderr {
		t.Fatalf("stderr =\n%s\nwant\n%s", stderr, wantStderr)
	}
	if !strings.Contains(stdout, `"token": "maze_ci_real"`) || strings.Contains(stdout+stderr, secret) || strings.Contains(stdout+stderr, "platform-identity") {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestExchangeOIDCStopsAtARefusal(t *testing.T) {
	for name, refusal := range map[string]func(http.ResponseWriter){
		"replayed, 409":   answer(http.StatusConflict, "", replayedRefusal),
		"replayed, 401":   answer(http.StatusUnauthorized, "", replayedRefusal),
		"older server":    answer(http.StatusUnprocessableEntity, "", `{"detail":[{"type":"extra_forbidden","loc":["body","retry_secret"],"msg":"Extra inputs are not permitted"}]}`),
		"500 is no retry": answer(http.StatusInternalServerError, "", `{"detail":"boom"}`),
	} {
		t.Run(name, func(t *testing.T) {
			backend := &scriptedBackend{answers: []func(http.ResponseWriter){refusal, answer(http.StatusOK, "", oneFeedResponse)}}
			clock := newTestClock()
			stdout, stderr, err := runScriptedExchange(t, backend, clock, suppliedIdentity)
			secret := assertOneRequestResent(t, backend.bodies, 1)
			if err == nil || stdout != "" || stderr != "" || len(clock.waits) != 0 || strings.Contains(err.Error(), secret) {
				t.Fatalf("err = %v, stdout = %q, stderr = %q", err, stdout, stderr)
			}
		})
	}
	backend := &scriptedBackend{answers: []func(http.ResponseWriter){answer(http.StatusConflict, "", replayedRefusal)}}
	_, _, err := runScriptedExchange(t, backend, newTestClock(), suppliedIdentity)
	if err == nil || !strings.Contains(err.Error(), "This identity token was already exchanged.\nError code:\n  oidc_assertion_replayed") {
		t.Fatalf("err = %v, want the server's refusal", err)
	}
}

func TestExchangeOIDCInjectedIdentityResendsTheSameBody(t *testing.T) {
	backend := &scriptedBackend{answers: []func(http.ResponseWriter){dropConnection, answer(http.StatusOK, "", oneFeedResponse)}}
	stdout, stderr, err := runScriptedExchange(t, backend, newTestClock(), nil, "--identity-token-injected")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	secret := assertOneRequestResent(t, backend.bodies, 2)
	if strings.Contains(backend.bodies[0], "oidc_token") || !reflect.DeepEqual(backend.authorizations, []string{"", ""}) {
		t.Fatalf("maze sent an identity: %s, Authorization %q", backend.bodies[0], backend.authorizations)
	}
	if stdout != "maze_ci_real\n" || stderr != "PackageMaze gave no readable answer; retrying the token exchange in 1s\n" || strings.Contains(stdout+stderr, secret) {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}
