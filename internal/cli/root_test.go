package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/auth"
)

func TestExchangeOIDCCommandTokenOutput(t *testing.T) {
	exchanger := &recordingExchanger{}
	stdout, stderr, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: exchanger,
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
	)
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "maze_ci_real\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !exchanger.called {
		t.Fatalf("backend was not called")
	}
	if exchanger.request.OIDCToken != "manual-oidc" {
		t.Fatalf("oidc_token was not forwarded")
	}
}

func TestExchangeOIDCCommandJSONAlias(t *testing.T) {
	stdout, _, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: &recordingExchanger{},
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--json",
	)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout)
	}
	if payload["provider"] != "manual" {
		t.Fatalf("provider = %v", payload["provider"])
	}
	if payload["token"] != "maze_ci_real" {
		t.Fatalf("token = %v", payload["token"])
	}
}

func TestExchangeOIDCCommandShellOutput(t *testing.T) {
	stdout, _, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: &recordingExchanger{},
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "publish",
		"--package", "your-package",
		"--format", "shell",
	)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !strings.Contains(stdout, "export MAZE_TOKEN='maze_ci_real'") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "export MAZE_TOKEN_EXPIRES_AT='2026-06-08T13:30:00Z'") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExchangeOIDCJSONAliasConflictsWithFormat(t *testing.T) {
	_, _, err := runCommand(
		"auth", "exchange-oidc",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--json",
		"--format", "shell",
	)
	if err == nil || !strings.Contains(err.Error(), "--json cannot be combined") {
		t.Fatalf("expected --json conflict, got %v", err)
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := runCommand("version")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !strings.HasPrefix(stdout, "maze ") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func runCommand(args ...string) (string, string, error) {
	return runCommandWithDeps(auth.Dependencies{Env: mapLookup(nil)}, args...)
}

func runCommandWithDeps(deps auth.Dependencies, args ...string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := NewRootCommand(deps)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

type recordingExchanger struct {
	called  bool
	request api.CITokenRequest
}

func (f *recordingExchanger) ExchangeCI(_ context.Context, request api.CITokenRequest) (api.CITokenResponse, error) {
	f.called = true
	f.request = request
	return api.CITokenResponse{
		Token:     "maze_ci_real",
		ExpiresAt: time.Date(2026, 6, 8, 13, 30, 0, 0, time.UTC),
		TokenType: "Bearer",
		Feed:      request.Feed,
		Purpose:   request.Purpose,
		Scopes:    []string{"read"},
	}, nil
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
