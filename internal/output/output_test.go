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
		BuildID:          "cis_0123456789abcdef",
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
	if payload["build_id"] != "cis_0123456789abcdef" || payload["ci_session_id"] != "cis_0123456789abcdef" {
		t.Fatalf("Build identifiers = %#v", payload)
	}
}

func TestWriteStructuredOutputOmitsBuildAliasesWhenUnavailable(t *testing.T) {
	result := testResult()
	result.BuildID = ""

	var jsonOutput bytes.Buffer
	if err := Write(result, WriteConfig{Format: FormatJSON, Stdout: &jsonOutput}); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if _, exists := payload["build_id"]; exists {
		t.Fatalf("build_id should be omitted: %#v", payload)
	}
	if _, exists := payload["ci_session_id"]; exists {
		t.Fatalf("ci_session_id should be omitted: %#v", payload)
	}

	var shellOutput bytes.Buffer
	if err := Write(result, WriteConfig{Format: FormatShell, Stdout: &shellOutput}); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	if strings.Contains(shellOutput.String(), "MAZE_BUILD_ID") || strings.Contains(shellOutput.String(), "MAZE_CI_SESSION_ID") {
		t.Fatalf("Build exports should be omitted: %q", shellOutput.String())
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
	if !strings.Contains(stdout.String(), `export MAZE_FEED='your-org/npm'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_FEED_BASE_URL='https://pkg.packagemaze.com/your-org/npm'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_ARTIFACT_PROTOCOL='npm'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_BUILD_ID='cis_0123456789abcdef'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_CI_SESSION_ID='cis_0123456789abcdef'`) {
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
	if string(content) != "package_token=maze_ci_secret\nartifact_protocol=npm\nfeed_base_url=https://pkg.packagemaze.com/your-org/npm\nbuild_id=cis_0123456789abcdef\nci_session_id=cis_0123456789abcdef\n" {
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
	outputPath := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(outputPath, nil, 0o600); err != nil {
		t.Fatalf("create output file: %v", err)
	}
	err := Write(result, WriteConfig{
		Format:           FormatGitHubOutput,
		GitHubOutputPath: outputPath,
	})
	if err == nil || !strings.Contains(err.Error(), "contains a newline") {
		t.Fatalf("expected newline error, got %v", err)
	}
	content, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("read output file: %v", readErr)
	}
	if len(content) != 0 {
		t.Fatalf("output file = %q, want empty", string(content))
	}
}
