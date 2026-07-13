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
		BuildNumber:      482,
		BuildURL:         "https://www.packagemaze.com/your-org/builds/482",
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
	if payload["build_number"] != float64(482) || payload["build_url"] != "https://www.packagemaze.com/your-org/builds/482" {
		t.Fatalf("Build reference = %#v", payload)
	}
}

func TestWriteStructuredOutputOmitsBuildReferenceWhenUnavailable(t *testing.T) {
	result := testResult()
	result.BuildNumber = 0
	result.BuildURL = ""

	var jsonOutput bytes.Buffer
	if err := Write(result, WriteConfig{Format: FormatJSON, Stdout: &jsonOutput}); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(jsonOutput.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if _, exists := payload["build_number"]; exists {
		t.Fatalf("build_number should be omitted: %#v", payload)
	}
	if _, exists := payload["build_url"]; exists {
		t.Fatalf("build_url should be omitted: %#v", payload)
	}

	var shellOutput bytes.Buffer
	if err := Write(result, WriteConfig{Format: FormatShell, Stdout: &shellOutput}); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	if strings.Contains(shellOutput.String(), "MAZE_BUILD_NUMBER") || strings.Contains(shellOutput.String(), "MAZE_BUILD_URL") {
		t.Fatalf("Build exports should be omitted: %q", shellOutput.String())
	}
}

func TestWriteStructuredOutputPassesThroughServerBuildFieldsIndependently(t *testing.T) {
	tests := []struct {
		name              string
		buildNumber       int64
		buildURL          string
		wantShell         string
		wantGitHubOutput  string
		unwantedShellText string
	}{
		{
			name:              "number only",
			buildNumber:       482,
			wantShell:         "export MAZE_BUILD_NUMBER='482'",
			wantGitHubOutput:  "build_number=482\n",
			unwantedShellText: "MAZE_BUILD_URL",
		},
		{
			name:              "url only",
			buildURL:          "/your-org/builds/482",
			wantShell:         "export MAZE_BUILD_URL='/your-org/builds/482'",
			wantGitHubOutput:  "build_url=/your-org/builds/482\n",
			unwantedShellText: "MAZE_BUILD_NUMBER",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := testResult()
			result.BuildNumber = test.buildNumber
			result.BuildURL = test.buildURL

			var shellOutput bytes.Buffer
			if err := Write(result, WriteConfig{Format: FormatShell, Stdout: &shellOutput}); err != nil {
				t.Fatalf("write shell: %v", err)
			}
			if !strings.Contains(shellOutput.String(), test.wantShell) {
				t.Fatalf("shell output = %q", shellOutput.String())
			}
			if strings.Contains(shellOutput.String(), test.unwantedShellText) {
				t.Fatalf("shell output = %q", shellOutput.String())
			}

			outputPath := filepath.Join(t.TempDir(), "github-output")
			if err := Write(result, WriteConfig{
				Format:           FormatGitHubOutput,
				OutputName:       "package_token",
				GitHubOutputPath: outputPath,
			}); err != nil {
				t.Fatalf("write GitHub output: %v", err)
			}
			content, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read GitHub output: %v", err)
			}
			if !strings.Contains(string(content), test.wantGitHubOutput) {
				t.Fatalf("GitHub output = %q", string(content))
			}
		})
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
	if !strings.Contains(stdout.String(), `export MAZE_BUILD_NUMBER='482'`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `export MAZE_BUILD_URL='https://www.packagemaze.com/your-org/builds/482'`) {
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
	if string(content) != "package_token=maze_ci_secret\nartifact_protocol=npm\nfeed_base_url=https://pkg.packagemaze.com/your-org/npm\nbuild_number=482\nbuild_url=https://www.packagemaze.com/your-org/builds/482\n" {
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
