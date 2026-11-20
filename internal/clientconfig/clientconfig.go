// Package clientconfig points package clients at PackageMaze Feeds for the
// length of one command. Each Artifact Protocol's Contributor says which
// environment variables and private files its clients read; this core groups
// the Feeds, selects a Contributor by Artifact Protocol, and composes the
// result. It names no Artifact Protocol or package client itself.
package clientconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// TokenEnv carries the PackageMaze Token to the command. Client files
// reference it instead of holding the Token.
const TokenEnv = "MAZE_TOKEN"

// Feed is one Feed the Token was granted for.
type Feed struct {
	Reference        string
	ArtifactProtocol string
	// BaseURL is the Feed URL with no trailing slash.
	BaseURL *url.URL
}

// NewFeed reads a Feed as PackageMaze's exchange answer names it.
func NewFeed(reference string, artifactProtocol string, baseURL string) (Feed, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Feed{}, fmt.Errorf("PackageMaze did not return a usable Feed URL for Feed %s", reference)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return Feed{Reference: reference, ArtifactProtocol: strings.TrimSpace(artifactProtocol), BaseURL: parsed}, nil
}

// Var is one environment variable to set for the command.
type Var struct {
	Name  string
	Value string
}

// Contribution is what one Artifact Protocol's package clients need.
type Contribution struct {
	Set []Var
	// Clear names inherited variables to remove because they would override
	// or widen what Set configures.
	Clear []string
}

// Files writes private files that live only as long as the command.
type Files interface {
	Write(name string, content []byte) (string, error)
}

// Request is what a Contributor configures: the Feeds of its Artifact
// Protocol, in request order, the first being the default.
type Request struct {
	Feeds []Feed
	Token string
	Files Files
}

// Contributor configures one Artifact Protocol's package clients.
type Contributor func(Request) (Contribution, error)

// Registry selects a Contributor by Artifact Protocol.
type Registry map[string]Contributor

// Configure asks each granted Artifact Protocol's Contributor for its
// clients' configuration. A Feed whose Artifact Protocol has no Contributor
// fails the whole configuration, so a command never runs half-configured.
func Configure(feeds []Feed, token string, registry Registry, files Files) (Contribution, error) {
	if len(feeds) == 0 {
		return Contribution{}, errors.New("PackageMaze granted no Feed to configure")
	}
	protocols, grouped, err := groupByProtocol(feeds, registry)
	if err != nil {
		return Contribution{}, err
	}
	combined := Contribution{Set: []Var{{Name: TokenEnv, Value: token}}}
	for _, protocol := range protocols {
		part, err := registry[protocol](Request{Feeds: grouped[protocol], Token: token, Files: files})
		if err != nil {
			return Contribution{}, fmt.Errorf("configure %s package clients: %w", protocol, err)
		}
		combined.Set = append(combined.Set, part.Set...)
		combined.Clear = append(combined.Clear, part.Clear...)
	}
	return combined, distinctNames(combined)
}

func groupByProtocol(feeds []Feed, registry Registry) ([]string, map[string][]Feed, error) {
	var protocols []string
	grouped := map[string][]Feed{}
	for _, feed := range feeds {
		if registry[feed.ArtifactProtocol] == nil {
			return nil, nil, fmt.Errorf("Feed %s uses Artifact Protocol %q, which maze auth exec cannot configure", feed.Reference, feed.ArtifactProtocol)
		}
		if grouped[feed.ArtifactProtocol] == nil {
			protocols = append(protocols, feed.ArtifactProtocol)
		}
		grouped[feed.ArtifactProtocol] = append(grouped[feed.ArtifactProtocol], feed)
	}
	return protocols, grouped, nil
}

// distinctNames refuses two Contributors claiming one variable.
func distinctNames(contribution Contribution) error {
	seen := map[string]bool{}
	names := append(varNames(contribution.Set), contribution.Clear...)
	for _, name := range names {
		key := strings.ToUpper(name)
		if seen[key] {
			return fmt.Errorf("package-client configuration names %s twice", name)
		}
		seen[key] = true
	}
	return nil
}

func varNames(vars []Var) []string {
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, v.Name)
	}
	return names
}

// Environ applies a Contribution to an inherited environment of NAME=value
// entries. Names compare without case: Windows and npm read them that way.
func Environ(inherited []string, contribution Contribution) []string {
	owned := map[string]bool{}
	for _, name := range append(varNames(contribution.Set), contribution.Clear...) {
		owned[strings.ToUpper(name)] = true
	}
	env := make([]string, 0, len(inherited)+len(contribution.Set))
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if !owned[strings.ToUpper(name)] {
			env = append(env, entry)
		}
	}
	for _, v := range contribution.Set {
		env = append(env, v.Name+"="+v.Value)
	}
	return env
}

// Names lists the variables a Contribution sets, for diagnostics that must
// not show their values.
func (contribution Contribution) Names() []string {
	return varNames(contribution.Set)
}
