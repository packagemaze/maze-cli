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
	BuildNumber      int64
	BuildURL         string
	Package          string
	Scopes           []string
	Provider         string
	ArtifactProtocol string
	// Feeds is PackageMaze's answer for several Feeds, in place of Feed,
	// FeedBaseURL, and ArtifactProtocol.
	Feeds json.RawMessage
	// FeedReferences names the Feeds of a several-Feed Token, in request
	// order; it is empty for one Feed.
	FeedReferences []string
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
		Token            string          `json:"token"`
		ExpiresAt        string          `json:"expires_at"`
		TokenType        string          `json:"token_type"`
		Feed             string          `json:"feed,omitempty"`
		FeedBaseURL      string          `json:"feed_base_url,omitempty"`
		Purpose          string          `json:"purpose"`
		BuildNumber      int64           `json:"build_number,omitempty"`
		BuildURL         string          `json:"build_url,omitempty"`
		Package          string          `json:"package,omitempty"`
		Scopes           []string        `json:"scopes"`
		Provider         string          `json:"provider,omitempty"`
		ArtifactProtocol string          `json:"artifact_protocol,omitempty"`
		Feeds            json.RawMessage `json:"feeds,omitempty"`
	}{
		Token:            result.Token,
		ExpiresAt:        result.ExpiresAt.UTC().Format(time.RFC3339),
		TokenType:        result.TokenType,
		Feed:             result.Feed,
		FeedBaseURL:      result.FeedBaseURL,
		Purpose:          result.Purpose,
		BuildNumber:      result.BuildNumber,
		BuildURL:         result.BuildURL,
		Package:          result.Package,
		Scopes:           result.Scopes,
		Provider:         result.Provider,
		ArtifactProtocol: result.ArtifactProtocol,
		Feeds:            result.Feeds,
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func writeShell(writer io.Writer, result Result) error {
	several := len(result.FeedReferences) > 1
	exports := []namedValue{
		{name: "MAZE_TOKEN", value: result.Token},
		{name: "MAZE_TOKEN_EXPIRES_AT", value: result.ExpiresAt.UTC().Format(time.RFC3339)},
	}
	if several {
		exports = append(exports, namedValue{name: "MAZE_FEEDS", value: strings.Join(result.FeedReferences, " ")})
	} else {
		exports = append(exports, namedValue{name: "MAZE_FEED", value: result.Feed}, namedValue{name: "MAZE_FEED_BASE_URL", value: result.FeedBaseURL})
	}
	exports = append(exports, namedValue{name: "MAZE_PURPOSE", value: result.Purpose})
	if !several {
		exports = append(exports, namedValue{name: "MAZE_ARTIFACT_PROTOCOL", value: result.ArtifactProtocol})
	}
	if result.BuildNumber != 0 {
		exports = append(exports, namedValue{name: "MAZE_BUILD_NUMBER", value: fmt.Sprintf("%d", result.BuildNumber)})
	}
	if result.BuildURL != "" {
		exports = append(exports, namedValue{name: "MAZE_BUILD_URL", value: result.BuildURL})
	}
	for _, export := range exports {
		if _, err := fmt.Fprintf(writer, "export %s=%s\n", export.name, shellQuote(export.value)); err != nil {
			return err
		}
	}
	return nil
}

func writeGitHubOutput(writer io.Writer, result Result, outputName string, outputPath string) error {
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("github-output format requires GITHUB_OUTPUT to be set")
	}
	if strings.TrimSpace(outputName) == "" {
		outputName = "token"
	}
	outputs := []namedValue{{name: outputName, value: result.Token}}
	if len(result.FeedReferences) > 1 {
		// The hyphen keeps this name apart from every --output-name and every
		// metadata output, which allow only letters, digits, and underscores.
		feeds, err := json.Marshal(result.FeedReferences)
		if err != nil {
			return fmt.Errorf("encode Feed references: %w", err)
		}
		outputs = append(outputs, namedValue{name: outputName + "-feeds", value: string(feeds)})
	}
	if strings.TrimSpace(result.ArtifactProtocol) != "" {
		outputs = append(outputs, namedValue{name: "artifact_protocol", value: result.ArtifactProtocol})
	}
	if strings.TrimSpace(result.FeedBaseURL) != "" {
		outputs = append(outputs, namedValue{name: "feed_base_url", value: result.FeedBaseURL})
	}
	if result.BuildNumber != 0 {
		outputs = append(outputs, namedValue{name: "build_number", value: fmt.Sprintf("%d", result.BuildNumber)})
	}
	if result.BuildURL != "" {
		outputs = append(outputs, namedValue{name: "build_url", value: result.BuildURL})
	}
	for _, output := range outputs {
		if err := validateGitHubOutputValue(output.name, output.value); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(outputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open GITHUB_OUTPUT: %w", err)
	}
	defer file.Close()
	for _, output := range outputs {
		if err := writeGitHubOutputValue(file, output.name, output.value); err != nil {
			return fmt.Errorf("write GITHUB_OUTPUT: %w", err)
		}
	}
	_, err = fmt.Fprintf(writer, "::add-mask::%s\n", result.Token)
	return err
}

type namedValue struct {
	name  string
	value string
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeGitHubOutputValue(writer io.Writer, name string, value string) error {
	if err := validateGitHubOutputValue(name, value); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "%s=%s\n", name, value)
	return err
}

func validateGitHubOutputValue(name string, value string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "=\r\n") {
		return fmt.Errorf("invalid GitHub output name")
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("GitHub output %s contains a newline", name)
	}
	return nil
}
