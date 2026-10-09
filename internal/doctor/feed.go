package doctor

import (
	"net/url"
	"strings"

	"github.com/packagemaze/maze-cli/internal/endpoint"
)

// packageClientDomain is the PackageMaze Package Client Domain a URL must be
// on to count as a Feed: https://pkg.packagemaze.com unless overridden for a
// local or staging stack. Nothing on any other host is ever contacted.
type packageClientDomain struct {
	scheme string
	host   string
}

func newPackageClientDomain(raw string, allowInsecureLocalhost bool) (packageClientDomain, error) {
	if err := endpoint.ValidateURL("package-client-url", raw, allowInsecureLocalhost); err != nil {
		return packageClientDomain{}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return packageClientDomain{}, err
	}
	return packageClientDomain{scheme: strings.ToLower(parsed.Scheme), host: strings.ToLower(parsed.Host)}, nil
}

func (d packageClientDomain) origin() string {
	return d.scheme + "://" + d.host
}

// feedFromURL reads the Feed a package-client URL points at, and the path that
// follows the Feed ("" when the trailing slash is missing, "/" for the Feed
// Base URL itself, "/simple/" for pip's index, and so on).
func (d packageClientDomain) feedFromURL(raw string) (Feed, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(parsed.Scheme, d.scheme) || !strings.EqualFold(parsed.Host, d.host) {
		return Feed{}, "", false
	}
	segments := strings.SplitN(strings.TrimPrefix(parsed.Path, "/"), "/", 3)
	if len(segments) < 2 || !endpoint.ValidFeedSegment(segments[0]) || !endpoint.ValidFeedSegment(segments[1]) {
		return Feed{}, "", false
	}
	rest := ""
	if len(segments) == 3 {
		rest = "/" + segments[2]
	}
	return d.feed(segments[0], segments[1]), rest, true
}

func (d packageClientDomain) feed(organization string, name string) Feed {
	return Feed{
		Organization: organization,
		Name:         name,
		BaseURL:      d.origin() + "/" + organization + "/" + name + "/",
	}
}

func (d packageClientDomain) feedFromSlug(slug string) (Feed, bool) {
	organization, name, ok := endpoint.SplitFeedSlug(slug)
	if !ok {
		return Feed{}, false
	}
	return d.feed(organization, name), true
}

// declaration records one URL a client is pointed at, resolved against the
// Package Client Domain, with credentials stripped before anything is kept.
func (d packageClientDomain) declaration(source string, client string, protocol ArtifactProtocol, setting string, rawURL string) (Declaration, string) {
	declaration := Declaration{Source: source, Client: client, Protocol: protocol, Setting: setting, URL: redactURL(rawURL)}
	feed, rest, ok := d.feedFromURL(rawURL)
	if !ok {
		return declaration, ""
	}
	declaration.Feed = &feed
	return declaration, rest
}

func urlHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return raw
	}
	return parsed.Host
}
