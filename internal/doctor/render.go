package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
)

func ParseFormat(value string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(value))) {
	case "", FormatMarkdown:
		return FormatMarkdown, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("format must be markdown or json")
	}
}

// Write renders the report. Everything passes through redaction once more at
// the boundary so no code path can print a Token Secret.
func Write(report Report, format Format, writer io.Writer, tokenValue string) error {
	if writer == nil {
		writer = io.Discard
	}
	var rendered string
	switch format {
	case "", FormatMarkdown:
		rendered = renderMarkdown(report)
	case FormatJSON:
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		rendered = string(encoded) + "\n"
	default:
		return fmt.Errorf("format must be markdown or json")
	}
	_, err := io.WriteString(writer, redactText(rendered, tokenValue))
	return err
}

func renderMarkdown(report Report) string {
	var out strings.Builder
	out.WriteString("# maze doctor\n\n")
	out.WriteString(fmt.Sprintf("**%s**: %s\n", report.Status, statusSentence(report)))
	if len(report.Feeds) > 0 {
		out.WriteString("\n## Feeds\n\n")
		for _, feed := range report.Feeds {
			out.WriteString(fmt.Sprintf("- %s (%s) at %s, named by %s\n", feed.Slug(), protocolLabel(feed.Protocol), feed.BaseURL, strings.Join(feed.Sources, ", ")))
		}
	}
	if len(report.Checks) > 0 {
		out.WriteString("\n## Checks\n\n")
		for _, item := range report.Checks {
			scope := ""
			if item.Feed != "" {
				scope = " " + item.Feed
			}
			out.WriteString(fmt.Sprintf("- **%s** `%s`%s: %s\n", item.Status, item.Code, scope, item.Summary))
			out.WriteString(fmt.Sprintf("  Next step: %s\n", item.Remediation))
		}
	}
	out.WriteString("\n## Token\n\n")
	out.WriteString(tokenSentence(report.Token) + "\n")
	out.WriteString("\n## Configuration read\n\n")
	if len(report.Configuration) == 0 {
		out.WriteString(fmt.Sprintf("No package-client configuration was found in %s.\n", report.Directory))
	}
	for _, declaration := range report.Configuration {
		out.WriteString(fmt.Sprintf("- %s %s: %s (%s)\n", declaration.Source, declaration.Setting, declaration.URL, declaration.Client))
	}
	if len(report.NotChecked) > 0 {
		out.WriteString("\n## Not checked\n\n")
		for _, item := range report.NotChecked {
			out.WriteString(fmt.Sprintf("- `%s`: %s\n", item.Check, item.Reason))
		}
	}
	out.WriteString("\n## Next step\n\n")
	out.WriteString(report.NextStep + "\n")
	out.WriteString("\nDocs: " + strings.Join(report.Docs, ", ") + "\n")
	return out.String()
}

func statusSentence(report Report) string {
	if report.Status == StatusNotSetUp {
		return "no committed configuration or environment variable points a package client at PackageMaze."
	}
	feeds := make([]string, 0, len(report.Feeds))
	for _, feed := range report.Feeds {
		feeds = append(feeds, feed.Slug())
	}
	failed := countStatus(report.Checks, StatusFail)
	warnings := countStatus(report.Checks, StatusWarning)
	if failed == 0 && warnings == 0 {
		return fmt.Sprintf("PackageMaze is configured (%s); all %d checks passed.", strings.Join(feeds, ", "), len(report.Checks))
	}
	return fmt.Sprintf("PackageMaze is configured (%s); %s, %s.", strings.Join(feeds, ", "), plural(failed, "check failed", "checks failed"), plural(warnings, "warning", "warnings"))
}

func plural(count int, singular string, pluralForm string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, pluralForm)
}

func protocolLabel(protocol ArtifactProtocol) string {
	switch protocol {
	case ProtocolNpm:
		return "npm"
	case ProtocolPyPI:
		return "PyPI"
	default:
		return "protocol not determined"
	}
}

func tokenSentence(token TokenStatus) string {
	credentials := strings.Join(token.ClientCredentials, ", ")
	switch {
	case token.Present && credentials != "":
		return fmt.Sprintf("%s is set; its value is never printed. Package-client credential variables also set: %s.", token.Env, credentials)
	case token.Present:
		return fmt.Sprintf("%s is set; its value is never printed.", token.Env)
	case credentials != "":
		return fmt.Sprintf("%s is not set. Package-client credential variables set: %s.", token.Env, credentials)
	default:
		return fmt.Sprintf("%s is not set, and no package-client credential variable is set.", token.Env)
	}
}
