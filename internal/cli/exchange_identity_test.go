package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/auth"
)

const oneFeedResponse = `{"artifact_protocol":"npm","build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm","purpose":"install","scopes":["read"],"token":"maze_ci_real","token_type":"Bearer"}`

type exchangeRun struct {
	stdout, stderr, body, authorization string
	called                              bool
	err                                 error
}

// runIdentityExchange runs exchange-oidc with no --provider against a server
// answering with response, and records what the server received, its retry
// secret masked.
func runIdentityExchange(t *testing.T, env map[string]string, stdin string, response string, args ...string) exchangeRun {
	t.Helper()
	var run exchangeRun
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		run.called = true
		run.body = string(content)
		run.authorization = request.Header.Get("Authorization")
		_, _ = io.WriteString(writer, response)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	command := NewRootCommand(auth.Dependencies{Env: mapLookup(env), HTTPClient: server.Client()})
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(stdin))
	command.SetArgs(append([]string{"auth", "exchange-oidc", "--api-url", server.URL, "--allow-insecure-localhost"}, args...))
	run.err = command.Execute()
	run.stdout, run.stderr = stdout.String(), stderr.String()
	run.body = maskRetrySecret(t, run.body)
	return run
}

func TestExchangeOIDCSuppliedTokenNamesNoProvider(t *testing.T) {
	run := runIdentityExchange(t, map[string]string{"MAZE_OIDC_TOKEN": "platform-oidc", "GITLAB_CI": "true"}, "", oneFeedResponse,
		"--feed", "acme/npm", "--purpose", "install", "--verbose")
	if run.err != nil {
		t.Fatalf("command returned error: %v", run.err)
	}
	want := `{"feed":"acme/npm","purpose":"install","package":null,"audience":"https://api.packagemaze.com","oidc_token":"platform-oidc","retry_secret":"<retry-secret>"}`
	if run.body != want {
		t.Fatalf("request body = %s\nwant           %s", run.body, want)
	}
	if run.stderr != "PackageMaze OIDC exchange provider: none named; the supplied identity token's issuer selects it\n" {
		t.Fatalf("stderr = %q", run.stderr)
	}
	if run.stdout != "maze_ci_real\n" {
		t.Fatalf("stdout = %q", run.stdout)
	}
}

func TestExchangeOIDCInjectedIdentitySendsNoTokenProviderOrAuthorization(t *testing.T) {
	run := runIdentityExchange(t, nil, "", oneFeedResponse,
		"--identity-token-injected", "--feed", "acme/npm", "--purpose", "install", "--verbose")
	if run.err != nil {
		t.Fatalf("command returned error: %v", run.err)
	}
	want := `{"feed":"acme/npm","purpose":"install","package":null,"audience":"https://api.packagemaze.com","retry_secret":"<retry-secret>"}`
	if run.body != want {
		t.Fatalf("request body = %s\nwant           %s", run.body, want)
	}
	if run.authorization != "" {
		t.Fatalf("Authorization = %q, want none from maze", run.authorization)
	}
	if !strings.Contains(run.stderr, "none named; the platform added the identity token to the request") {
		t.Fatalf("stderr = %q", run.stderr)
	}
}

func TestExchangeOIDCInjectedIdentityRefusesASuppliedToken(t *testing.T) {
	run := runIdentityExchange(t, nil, "token", oneFeedResponse,
		"--identity-token-injected", "--identity-token-stdin", "--feed", "acme/npm", "--purpose", "install")
	if run.err == nil || !strings.Contains(run.err.Error(), "remove --identity-token-stdin") || run.called {
		t.Fatalf("err = %v, called = %v", run.err, run.called)
	}
}

func TestExchangeOIDCIdentityTokenFlagsAndTheirEarlierNames(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		env       map[string]string
		stdin     string
		args      []string
		wantToken string
		wantErr   string
	}{
		{name: "identity-token-stdin", stdin: "stdin-token", args: []string{"--identity-token-stdin"}, wantToken: "stdin-token"},
		{name: "oidc-token-stdin", stdin: "stdin-token", args: []string{"--oidc-token-stdin"}, wantToken: "stdin-token"},
		{name: "identity-token-file", args: []string{"--identity-token-file", tokenFile}, wantToken: "file-token"},
		{name: "oidc-token-file", args: []string{"--oidc-token-file", tokenFile}, wantToken: "file-token"},
		{name: "identity-token-env", env: map[string]string{"JOB_TOKEN": "env-token"}, args: []string{"--identity-token-env", "JOB_TOKEN"}, wantToken: "env-token"},
		{name: "oidc-token-env", env: map[string]string{"JOB_TOKEN": "env-token"}, args: []string{"--oidc-token-env", "JOB_TOKEN"}, wantToken: "env-token"},
		{name: "both stdin names", stdin: "x", args: []string{"--identity-token-stdin", "--oidc-token-stdin"}, wantErr: "--oidc-token-stdin is the earlier name of --identity-token-stdin; pass only one of them"},
		{name: "both file names", args: []string{"--identity-token-file", tokenFile, "--oidc-token-file", tokenFile}, wantErr: "--oidc-token-file is the earlier name of --identity-token-file"},
		{name: "both env names", args: []string{"--identity-token-env", "A", "--oidc-token-env", "B"}, wantErr: "--oidc-token-env is the earlier name of --identity-token-env"},
		{name: "file and stdin", stdin: "x", args: []string{"--identity-token-file", tokenFile, "--oidc-token-stdin"}, wantErr: "choose either --identity-token-file or --identity-token-stdin, not both"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runIdentityExchange(t, test.env, test.stdin, oneFeedResponse, append([]string{"--feed", "acme/npm", "--purpose", "install"}, test.args...)...)
			if test.wantErr != "" {
				if run.err == nil || !strings.Contains(run.err.Error(), test.wantErr) || run.called {
					t.Fatalf("err = %v, called = %v, want %q", run.err, run.called, test.wantErr)
				}
				return
			}
			if run.err != nil {
				t.Fatalf("command returned error: %v", run.err)
			}
			if !strings.Contains(run.body, `"oidc_token":"`+test.wantToken+`"`) || strings.Contains(run.body, `"provider"`) {
				t.Fatalf("request body = %s", run.body)
			}
		})
	}
}

func TestExchangeOIDCHelpShowsProductNamesOnly(t *testing.T) {
	for _, subcommand := range []string{"exchange-oidc", "show-identity"} {
		stdout, _, err := runCommand("auth", subcommand, "--help")
		if err != nil {
			t.Fatalf("%s --help: %v", subcommand, err)
		}
		for _, want := range []string{"--identity-token-env", "--identity-token-file", "--identity-token-stdin"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("%s help lacks %s:\n%s", subcommand, want, stdout)
			}
		}
		if strings.Contains(stdout, "--oidc-token") || strings.Contains(strings.ToLower(stdout), "gitlab") {
			t.Fatalf("%s help shows a hidden alias or GitLab:\n%s", subcommand, stdout)
		}
	}
}

func TestExchangeOIDCSeveralFeedsShellOutput(t *testing.T) {
	run := runIdentityExchange(t, map[string]string{"MAZE_OIDC_TOKEN": "platform-oidc"}, "", severalFeedsResponse,
		"--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install", "--format", "shell")
	if run.err != nil {
		t.Fatalf("command returned error: %v", run.err)
	}
	want := "export MAZE_TOKEN='maze_ci_real'\n" +
		"export MAZE_TOKEN_EXPIRES_AT='2026-11-10T10:00:00Z'\n" +
		"export MAZE_FEEDS='acme/npm acme/pypi'\n" +
		"export MAZE_PURPOSE='install'\n" +
		"export MAZE_BUILD_NUMBER='7'\n" +
		"export MAZE_BUILD_URL='https://www.packagemaze.com/acme/builds/7'\n"
	if run.stdout != want {
		t.Fatalf("stdout =\n%s\nwant\n%s", run.stdout, want)
	}
}

func TestExchangeOIDCSeveralFeedsGitHubOutput(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "github-output")
	run := runIdentityExchange(t, map[string]string{"MAZE_OIDC_TOKEN": "platform-oidc", "GITHUB_OUTPUT": outputPath}, "", severalFeedsResponse,
		"--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install", "--format", "github-output", "--allow-github-output-outside-actions")
	if run.err != nil {
		t.Fatalf("command returned error: %v", run.err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "token=maze_ci_real\n" +
		`token-feeds=["acme/npm","acme/pypi"]` + "\n" +
		"build_number=7\n" +
		"build_url=https://www.packagemaze.com/acme/builds/7\n"
	if string(content) != want {
		t.Fatalf("GITHUB_OUTPUT =\n%s\nwant\n%s", content, want)
	}
	if run.stdout != "::add-mask::maze_ci_real\n" || strings.Contains(run.stderr, "maze_ci_real") {
		t.Fatalf("stdout = %q, stderr = %q", run.stdout, run.stderr)
	}
}
