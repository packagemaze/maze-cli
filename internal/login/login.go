// Package login gets a person's machine a PackageMaze Token through one
// browser approval: start a Token request, let the person approve it where
// they are signed in, collect the Token once, save it to a profile and verify
// it. The Token Secret is never printed or logged.
package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/endpoint"
)

const (
	defaultInterval = 2 * time.Second
	maxTokenName    = 80
)

type Config struct {
	APIURL                 string
	PackageClientURL       string
	Organization           string
	Feeds                  []string
	Purpose                string
	Profile                string
	TokenName              string
	NoBrowser              bool
	AllowInsecureLocalhost bool
}

type Dependencies struct {
	HTTPClient  *http.Client
	OpenBrowser func(url string) error
	Sleep       func(ctx context.Context, d time.Duration) error
	ConfigDir   func() (string, error)
	Hostname    func() (string, error)
	Stderr      io.Writer
}

type Result struct {
	Organization Organization
	Profile      string
	Path         string
}

// Run starts the request, waits for the person's decision and saves the Token.
func Run(ctx context.Context, config Config, deps Dependencies) (Result, error) {
	request, profile, err := resolve(config, deps)
	if err != nil {
		return Result{}, err
	}
	client := Client{APIURL: config.APIURL, HTTPClient: deps.HTTPClient}
	started, err := client.Start(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if err := endpoint.ValidateURL("approval-url", started.ApprovalURL, config.AllowInsecureLocalhost); err != nil {
		return Result{}, fmt.Errorf("PackageMaze sent an approval link maze will not open: %w", err)
	}
	announce(deps, config, started)
	collected, err := waitForApproval(ctx, client, started, deps)
	if err != nil {
		return Result{}, err
	}
	configDir, err := deps.ConfigDir()
	if err != nil {
		return Result{}, fmt.Errorf("find the user config directory: %w", err)
	}
	path := CredentialsPath(configDir)
	saved := Profile{APIURL: config.APIURL, PackageClientURL: config.PackageClientURL, Organization: collected.Organization.Slug, Token: collected.TokenSecret, TokenID: collected.TokenID, ExpiresAt: collected.ExpiresAt}
	if err := SaveProfile(path, profile, saved); err != nil {
		return Result{}, fmt.Errorf("save profile %s: %w", profile, err)
	}
	if len(collected.Feeds) > 0 {
		if err := client.Verify(ctx, collected.TokenSecret, collected.Feeds[0].ID); err != nil {
			return Result{}, fmt.Errorf("the Token was saved to profile %s but did not verify: %w", profile, err)
		}
	}
	return Result{Organization: collected.Organization, Profile: profile, Path: path}, nil
}

func resolve(config Config, deps Dependencies) (StartRequest, string, error) {
	organization := strings.TrimSpace(config.Organization)
	if !endpoint.ValidFeedSegment(organization) {
		return StartRequest{}, "", errors.New("--org must be an Organization slug")
	}
	access, err := accessFor(config.Purpose)
	if err != nil {
		return StartRequest{}, "", err
	}
	feeds, err := feedNames(organization, config.Feeds)
	if err != nil {
		return StartRequest{}, "", err
	}
	profile := strings.TrimSpace(config.Profile)
	if profile == "" {
		profile = organization
	}
	if !ValidProfileName(profile) {
		return StartRequest{}, "", errors.New("--profile must be 1-64 letters, numbers, dots, underscores or hyphens")
	}
	return StartRequest{Organization: organization, Access: access, Feeds: feeds, TokenName: tokenName(config.TokenName, deps)}, profile, nil
}

func accessFor(purpose string) (string, error) {
	switch strings.TrimSpace(purpose) {
	case "", "install":
		return "read", nil
	case "publish":
		return "read_publish", nil
	default:
		return "", errors.New("--purpose must be install or publish")
	}
}

// feedNames accepts `feed` or `org/feed`; none means every Feed of the Organization.
func feedNames(organization string, values []string) (any, error) {
	if len(values) == 0 {
		return "all", nil
	}
	names := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if org, feed, ok := endpoint.SplitFeedSlug(name); ok {
			if org != organization {
				return nil, fmt.Errorf("--feed %s is not in Organization %s", value, organization)
			}
			name = feed
		}
		if !endpoint.ValidFeedSegment(name) {
			return nil, fmt.Errorf("--feed %s is not a Feed name", value)
		}
		names = append(names, name)
	}
	return names, nil
}

func tokenName(requested string, deps Dependencies) string {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = "maze CLI"
		if deps.Hostname != nil {
			if host, err := deps.Hostname(); err == nil && strings.TrimSpace(host) != "" {
				name = "maze CLI on " + strings.TrimSpace(host)
			}
		}
	}
	if len(name) > maxTokenName {
		name = strings.TrimSpace(name[:maxTokenName])
	}
	return name
}

func announce(deps Dependencies, config Config, started Started) {
	url := started.ApprovalURL
	opened := false
	if !config.NoBrowser && deps.OpenBrowser != nil {
		opened = deps.OpenBrowser(url) == nil
	}
	if opened {
		fmt.Fprintln(deps.Stderr, "Your browser should have opened to approve a PackageMaze Token.")
		fmt.Fprintln(deps.Stderr, "If it didn't, open this URL yourself:")
	} else {
		fmt.Fprintln(deps.Stderr, "Open this URL in a browser where you are signed in to PackageMaze, and approve the Token:")
	}
	fmt.Fprintln(deps.Stderr, url)
	fmt.Fprintf(deps.Stderr, "When the page asks, type this code: %s\n", started.UserCode)
	fmt.Fprintln(deps.Stderr, "Waiting for approval...")
}

func waitForApproval(ctx context.Context, client Client, started Started, deps Dependencies) (*Collected, error) {
	interval := time.Duration(started.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	for {
		collected, err := client.Collect(ctx, started)
		if err != nil || collected != nil {
			return collected, err
		}
		if err := deps.Sleep(ctx, interval); err != nil {
			return nil, err
		}
	}
}

// SleepContext waits for d, or returns early when ctx ends.
func SleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DefaultDependencies are the real browser, clock, config directory and host name.
func DefaultDependencies() Dependencies {
	return Dependencies{
		ConfigDir:   os.UserConfigDir,
		Hostname:    os.Hostname,
		OpenBrowser: OpenBrowser,
		Sleep:       SleepContext,
	}
}
