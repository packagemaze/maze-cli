package doctor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/endpoint"
	"github.com/packagemaze/maze-cli/internal/version"
)

type ProbeOutcome string

const (
	ProbeAccepted      ProbeOutcome = "accepted"
	ProbeTokenRejected ProbeOutcome = "token_rejected"
	ProbeFeedNotFound  ProbeOutcome = "feed_not_found"
	ProbeThrottled     ProbeOutcome = "throttled"
	ProbeUnavailable   ProbeOutcome = "unavailable"
	ProbeUnreachable   ProbeOutcome = "unreachable"
	ProbeUnexpected    ProbeOutcome = "unexpected"
)

type ProbeRequest struct {
	Feed     Feed
	Protocol ArtifactProtocol
	Token    string
}

type ProbeResult struct {
	Outcome    ProbeOutcome
	StatusCode int
	Username   string
	Err        error
}

// Prober makes the one read-only request a package client would make with the
// Token, against the Feed the repository is configured for, and nothing else.
type Prober interface {
	Probe(context.Context, ProbeRequest) ProbeResult
}

type HTTPProber struct {
	allowInsecureLocalhost bool
	httpClient             *http.Client
}

// NewHTTPProber copies the client so that redirects are never followed: a
// redirect could carry the Token to another host or to plain http, and a 3xx
// answer is reported as the unexpected response it is.
func NewHTTPProber(httpClient *http.Client, allowInsecureLocalhost bool) *HTTPProber {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPProber{allowInsecureLocalhost: allowInsecureLocalhost, httpClient: &client}
}

// probeURL is the npm whoami control or the PyPI Simple API root: both need a
// Token with Read access to the Feed and neither changes anything.
func probeURL(feed Feed, protocol ArtifactProtocol) string {
	if protocol == ProtocolPyPI {
		return feed.SimpleURL()
	}
	return feed.BaseURL + "-/whoami"
}

func (p *HTTPProber) Probe(ctx context.Context, request ProbeRequest) ProbeResult {
	target := probeURL(request.Feed, request.Protocol)
	if err := endpoint.ValidateURL("package-client-url", target, p.allowInsecureLocalhost); err != nil {
		return ProbeResult{Outcome: ProbeUnexpected, Err: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ProbeResult{Outcome: ProbeUnexpected, Err: err}
	}
	if request.Protocol == ProtocolPyPI {
		httpRequest.Header.Set("Accept", "application/vnd.pypi.simple.v1+html, text/html;q=0.9")
		credentials := base64.StdEncoding.EncodeToString([]byte("__token__:" + request.Token))
		httpRequest.Header.Set("Authorization", "Basic "+credentials)
	} else {
		httpRequest.Header.Set("Accept", "application/json")
		httpRequest.Header.Set("Authorization", "Bearer "+request.Token)
	}
	httpRequest.Header.Set("User-Agent", version.PackageMazeClientVersion()+" doctor")
	httpRequest.Header.Set(version.PackageMazeClientVersionHeader, version.PackageMazeClientVersion())

	response, err := p.httpClient.Do(httpRequest)
	if err != nil {
		return ProbeResult{Outcome: ProbeUnreachable, Err: errors.New(redactText(err.Error(), request.Token))}
	}
	defer response.Body.Close()
	result := ProbeResult{StatusCode: response.StatusCode}
	body := io.LimitReader(response.Body, 64*1024)
	switch {
	case response.StatusCode == http.StatusOK:
		result.Outcome = ProbeAccepted
		if request.Protocol != ProtocolPyPI {
			var payload struct {
				Username string `json:"username"`
			}
			if json.NewDecoder(body).Decode(&payload) == nil {
				result.Username = strings.TrimSpace(payload.Username)
			}
		}
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		result.Outcome = ProbeTokenRejected
	case response.StatusCode == http.StatusNotFound:
		result.Outcome = ProbeFeedNotFound
	case response.StatusCode == http.StatusTooManyRequests:
		result.Outcome = ProbeThrottled
	case response.StatusCode >= http.StatusInternalServerError:
		result.Outcome = ProbeUnavailable
	default:
		result.Outcome = ProbeUnexpected
	}
	_, _ = io.Copy(io.Discard, body)
	return result
}

// probeCheck turns one probe into the check a person or agent acts on. A
// rejected Token is reported as exactly that: PackageMaze deliberately does not
// tell package clients whether a Token is revoked, expired, or for another Feed.
func probeCheck(result ProbeResult, feed Feed, protocol ArtifactProtocol, tokenEnv string) Check {
	target := probeURL(feed, protocol)
	switch result.Outcome {
	case ProbeAccepted:
		as := ""
		if result.Username != "" {
			as = fmt.Sprintf(" as %s", result.Username)
		}
		return check(
			"read_token_ready",
			StatusPass,
			fmt.Sprintf("PackageMaze accepts %s for reads from Feed %s%s (%s answered HTTP 200).", tokenEnv, feed.Slug(), as, target),
			"No action needed.",
		).forFeed(feed)
	case ProbeTokenRejected:
		return check(
			"read_token_rejected",
			StatusFail,
			fmt.Sprintf("PackageMaze rejected %s for Feed %s (HTTP %d from %s). PackageMaze does not say why: the Token may be revoked, expired, created for another Feed, or without Read access.", tokenEnv, feed.Slug(), result.StatusCode, target),
			fmt.Sprintf("Create a new Token with Read access to Feed %s, or rotate the existing one, and set %s to its Secret. See %s", feed.Slug(), tokenEnv, DocsFixAFailingInstall),
		).forFeed(feed)
	case ProbeFeedNotFound:
		return check(
			"feed_not_found",
			StatusFail,
			fmt.Sprintf("No %s Feed answers at %s (HTTP 404).", protocolLabel(protocol), target),
			fmt.Sprintf("Check the Organization and Feed names against PackageMaze. npm clients use the Feed Base URL %s; pip, uv, and Poetry use %s. See %s", feed.BaseURL, feed.SimpleURL(), DocsFixAFailingInstall),
		).forFeed(feed)
	case ProbeThrottled:
		return check(
			"auth_attempts_throttled",
			StatusWarning,
			fmt.Sprintf("PackageMaze is throttling authentication attempts from this client (HTTP 429 from %s).", target),
			"Wait a minute before retrying; repeated rejected Tokens trigger the throttle.",
		).forFeed(feed)
	case ProbeUnavailable:
		return check(
			"packagemaze_unavailable",
			StatusWarning,
			fmt.Sprintf("PackageMaze answered HTTP %d from %s.", result.StatusCode, target),
			"Retry in a moment; this is not a repository configuration problem.",
		).forFeed(feed)
	case ProbeUnreachable:
		return check(
			"packagemaze_unreachable",
			StatusWarning,
			fmt.Sprintf("%s could not be reached: %v.", target, result.Err),
			"Check network access and proxy settings, then run maze doctor again or pass --offline.",
		).forFeed(feed)
	default:
		detail := fmt.Sprintf("HTTP %d", result.StatusCode)
		if result.Err != nil {
			detail = result.Err.Error()
		}
		return check(
			"unexpected_response",
			StatusWarning,
			fmt.Sprintf("%s answered unexpectedly (%s).", target, detail),
			"Run maze doctor again; if it persists, report the response to PackageMaze support.",
		).forFeed(feed)
	}
}
