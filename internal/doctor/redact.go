package doctor

import (
	"net/url"
	"regexp"
	"strings"
)

const redacted = "[redacted]"

var tokenSecretPattern = regexp.MustCompile(`\bpm_[A-Za-z0-9_-]+\b`)

// redactURL drops the userinfo from a URL so a credential embedded the way pip
// expects (https://__token__:<secret>@host/...) never reaches a report.
func redactURL(value string) string {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.User == nil {
		return redactText(trimmed, "")
	}
	parsed.User = nil
	return parsed.String()
}

// redactText removes every PackageMaze Token Secret shape and the exact
// configured Token value from text destined for stdout or stderr.
func redactText(value string, tokenValue string) string {
	result := tokenSecretPattern.ReplaceAllString(value, redacted)
	if secret := strings.TrimSpace(tokenValue); secret != "" {
		result = strings.ReplaceAll(result, secret, redacted)
	}
	return result
}

func containsLiteralTokenSecret(value string) bool {
	return tokenSecretPattern.MatchString(value)
}

func urlHasCredentials(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.User != nil
}
