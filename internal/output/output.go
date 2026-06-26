package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type Format string

const (
	FormatToken        Format = "token"
	FormatJSON         Format = "json"
	FormatShell        Format = "shell"
	FormatGitHubOutput Format = "github-output"
)

type Result struct {
	Token            string
	ExpiresAt        time.Time
	TokenType        string
	Feed             string
	FeedBaseURL      string
	Purpose          string
	Package          string
	Scopes           []string
	Provider         string
	ArtifactProtocol string
}

type WriteConfig struct {
	Format           Format
	OutputName       string
	GitHubOutputPath string
	Stdout           io.Writer
	Stderr           io.Writer
}

func ParseFormat(value string) (Format, error) {
	normalized := Format(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", FormatToken:
		return FormatToken, nil
	case FormatJSON, FormatShell, FormatGitHubOutput:
		return normalized, nil
	default:
		return "", fmt.Errorf("format must be token, json, shell, or github-output")
	}
}

func Write(result Result, config WriteConfig) error {
	stdout := config.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := config.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	switch config.Format {
	case "", FormatToken:
		_, err := fmt.Fprintln(stdout, result.Token)
		return err
	case FormatJSON:
		return writeJSON(stdout, result)
	case FormatShell:
		return writeShell(stdout, result)
	case FormatGitHubOutput:
		return writeGitHubOutput(stdout, result, config.OutputName, config.GitHubOutputPath)
	default:
		_, _ = stderr.Write([]byte{})
		return fmt.Errorf("format must be token, json, shell, or github-output")
	}
}

func writeJSON(writer io.Writer, result Result) error {
	payload := struct {
		Token            string   `json:"token"`
		ExpiresAt        string   `json:"expires_at"`
		TokenType        string   `json:"token_type"`
		Feed             string   `json:"feed"`
		FeedBaseURL      string   `json:"feed_base_url,omitempty"`
		Purpose          string   `json:"purpose"`
		Package          string   `json:"package,omitempty"`
		Scopes           []string `json:"scopes"`
		Provider         string   `json:"provider"`
		ArtifactProtocol string   `json:"artifact_protocol,omitempty"`
	}{
		Token:            result.Token,
		ExpiresAt:        result.ExpiresAt.UTC().Format(time.RFC3339),
		TokenType:        result.TokenType,
		Feed:             result.Feed,
		FeedBaseURL:      result.FeedBaseURL,
		Purpose:          result.Purpose,
		Package:          result.Package,
		Scopes:           result.Scopes,
		Provider:         result.Provider,
		ArtifactProtocol: result.ArtifactProtocol,
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func writeShell(writer io.Writer, result Result) error {
	_, err := fmt.Fprintf(
		writer,
		"export MAZE_TOKEN=%s\nexport MAZE_TOKEN_EXPIRES_AT=%s\nexport MAZE_FEED=%s\nexport MAZE_FEED_BASE_URL=%s\nexport MAZE_PURPOSE=%s\nexport MAZE_ARTIFACT_PROTOCOL=%s\n",
		shellQuote(result.Token),
		shellQuote(result.ExpiresAt.UTC().Format(time.RFC3339)),
		shellQuote(result.Feed),
		shellQuote(result.FeedBaseURL),
		shellQuote(result.Purpose),
		shellQuote(result.ArtifactProtocol),
	)
	return err
}

func writeGitHubOutput(writer io.Writer, result Result, outputName string, outputPath string) error {
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("github-output format requires GITHUB_OUTPUT to be set")
	}
	if strings.TrimSpace(outputName) == "" {
		outputName = "token"
	}
	file, err := os.OpenFile(outputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open GITHUB_OUTPUT: %w", err)
	}
	defer file.Close()
	if err := writeGitHubOutputValue(file, outputName, result.Token); err != nil {
		return fmt.Errorf("write GITHUB_OUTPUT: %w", err)
	}
	if strings.TrimSpace(result.ArtifactProtocol) != "" {
		if err := writeGitHubOutputValue(file, "artifact_protocol", result.ArtifactProtocol); err != nil {
			return fmt.Errorf("write GITHUB_OUTPUT: %w", err)
		}
	}
	if strings.TrimSpace(result.FeedBaseURL) != "" {
		if err := writeGitHubOutputValue(file, "feed_base_url", result.FeedBaseURL); err != nil {
			return fmt.Errorf("write GITHUB_OUTPUT: %w", err)
		}
	}
	_, err = fmt.Fprintf(writer, "::add-mask::%s\n", result.Token)
	return err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeGitHubOutputValue(writer io.Writer, name string, value string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "=\r\n") {
		return fmt.Errorf("invalid GitHub output name")
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("GitHub output %s contains a newline", name)
	}
	_, err := fmt.Fprintf(writer, "%s=%s\n", name, value)
	return err
}
