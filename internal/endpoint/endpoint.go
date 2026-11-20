// Package endpoint holds the rules every maze command shares about where
// PackageMaze is and how a Feed is named, so they are written once.
package endpoint

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/packagemaze/maze-cli/internal/ci"
)

const (
	DefaultPackageClientURL  = "https://pkg.packagemaze.com"
	DefaultTokenEnv          = "MAZE_TOKEN"
	PackageClientURLEnv      = "MAZE_PACKAGE_CLIENT_URL"
	PackageClientURLFlagHelp = "PackageMaze Package Client Domain base URL (default: " + PackageClientURLEnv + ", else " + DefaultPackageClientURL + ")"
)

var feedSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateURL admits https, and plain http only for localhost when the caller
// opted in with --allow-insecure-localhost.
func ValidateURL(flag string, value string, allowInsecureLocalhost bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("--%s must be an absolute URL", flag)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && allowInsecureLocalhost && IsLocalhost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("--%s must use https; use --allow-insecure-localhost only for local http endpoints", flag)
}

func IsLocalhost(host string) bool {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if normalized == "localhost" {
		return true
	}
	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
}

// ResolvePackageClientURL applies the flag, then MAZE_PACKAGE_CLIENT_URL, then
// the production default, and drops any trailing slash.
func ResolvePackageClientURL(flagValue string, env ci.LookupEnv) string {
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	for _, candidate := range []string{flagValue, envOrEmpty(env, PackageClientURLEnv), DefaultPackageClientURL} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return strings.TrimRight(trimmed, "/")
		}
	}
	return DefaultPackageClientURL
}

func envOrEmpty(env ci.LookupEnv, key string) string {
	value, _ := env(key)
	return value
}

// SplitFeedSlug reads a Feed named as <organization>/<feed>.
func SplitFeedSlug(slug string) (string, string, bool) {
	organization, name, found := strings.Cut(strings.TrimSpace(slug), "/")
	if !found || !ValidFeedSegment(organization) || !ValidFeedSegment(name) {
		return "", "", false
	}
	return organization, name, true
}

func ValidFeedSegment(value string) bool {
	return feedSegmentPattern.MatchString(value)
}
