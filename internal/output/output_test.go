package output

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testResult() Result {
	return Result{
		Token:            "maze_ci_secret",
		ExpiresAt:        time.Date(2026, 6, 8, 12, 30, 0, 0, time.UTC),
		TokenType:        "Bearer",
		Feed:             "your-org/npm",
		FeedBaseURL:      "https://pkg.packagemaze.com/your-org/npm",
		Purpose:          "install",
		Scopes:           []string{"read"},
		Provider:         "github",
		ArtifactProtocol: "npm",
	}
}

func TestWriteToken(t *testing.T) {
	var stdout bytes.Buffer
	if err := Write(testResult(), WriteConfig{Format: FormatToken, Stdout: &stdout}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if stdout.String() != "maze_ci_secret\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWriteJSON(t *testing.T) {
	var stdout bytes.Buffer
	if err := Write(testResult(), WriteConfig{Format: FormatJSON, Stdout: &stdout}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json output was invalid: %v", err)
	}
	if payload["token"] != "maze_ci_secret" {
		t.Fatalf("token = %v", payload["token"])
	}
	if payload["expires_at"] != "2026-06-08T12:30:00Z" {
		t.Fatalf("expires_at = %v", payload["expires_at"])
	}
	if payload["provider"] != "github" {
		t.Fatalf("provider = %v", payload["provider"])
	}
	if payload["artifact_protocol"] != "npm" {
		t.Fatalf("artifact_protocol = %v", payload["artifact_protocol"])
	}
	if payload["feed_base_url"] != "https://pkg.packagemaze.com/your-org/npm" {
		t.Fatalf("feed_base_url = %v", payload["feed_base_url"])
	}
}

func TestWriteShell(t *testing.T) {
	var stdout bytes.Buffer
	result := testResult()
	result.Token = "maze_ci_secret'quoted"
	if err := Write(result, WriteConfig{Format: FormatShell, Stdout: &stdout}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `export MAZE_TOKEN='maze_ci_secret'\''quoted'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_TOKEN_EXPIRES_AT='2026-06-08T12:30:00Z'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWriteGitHubOutputMasksAndWritesOutputFile(t *testing.T) {
	var stdout bytes.Buffer
	outputPath := filepath.Join(t.TempDir(), "github-output")
	if err := Write(testResult(), WriteConfig{
		Format:           FormatGitHubOutput,
		OutputName:       "package_token",
		GitHubOutputPath: outputPath,
		Stdout:           &stdout,
	}); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(content) != "package_token=maze_ci_secret\nartifact_protocol=npm\nfeed_base_url=https://pkg.packagemaze.com/your-org/npm\n" {
		t.Fatalf("output file = %q", string(content))
	}
	if stdout.String() != "::add-mask::maze_ci_secret\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestWriteGitHubOutputRequiresOutputPath(t *testing.T) {
	err := Write(testResult(), WriteConfig{Format: FormatGitHubOutput})
	if err == nil || !strings.Contains(err.Error(), "GITHUB_OUTPUT") {
		t.Fatalf("expected GITHUB_OUTPUT error, got %v", err)
	}
}

func TestWriteGitHubOutputRejectsUnsafeOutputValues(t *testing.T) {
	result := testResult()
	result.FeedBaseURL = "https://pkg.packagemaze.com/your-org/npm\nmalicious=value"
	err := Write(result, WriteConfig{
		Format:           FormatGitHubOutput,
		GitHubOutputPath: filepath.Join(t.TempDir(), "github-output"),
	})
	if err == nil || !strings.Contains(err.Error(), "contains a newline") {
		t.Fatalf("expected newline error, got %v", err)
	}
}
