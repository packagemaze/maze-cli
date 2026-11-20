package login

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/httpretry"
	"github.com/packagemaze/maze-cli/internal/version"
)

const maxResponseBytes = 1024 * 1024

// errUnanswered is a request PackageMaze may or may not have handled: the connection failed,
// or a 2xx answer could not be read.
var errUnanswered = errors.New("PackageMaze did not answer")

// StartRequest is what maze asks PackageMaze for; a person narrows it in the browser.
type StartRequest struct {
	Organization string `json:"organization"`
	Access       string `json:"access"`
	Feeds        any    `json:"feeds"`
	TokenName    string `json:"token_name"`
}

// Started names the flow. CollectSecret proves this process started it and never leaves it.
type Started struct {
	FlowID          string `json:"flow_id"`
	CollectSecret   string `json:"collect_secret"`
	ApprovalURL     string `json:"approval_url"`
	ExpiresAt       string `json:"expires_at"`
	IntervalSeconds int    `json:"interval_seconds"`
	UserCode        string `json:"user_code"`
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Feed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Collected carries the Token Secret. It is written to the profile and nowhere else.
type Collected struct {
	TokenSecret  string       `json:"token_secret"`
	TokenID      string       `json:"token_id"`
	TokenName    string       `json:"token_name"`
	Access       string       `json:"access"`
	Feeds        []Feed       `json:"feeds"`
	ExpiresAt    string       `json:"expires_at"`
	Organization Organization `json:"organization"`
}

// RefusedError is any PackageMaze answer of HTTP 300 or above. Code, Message and Recovery come
// from the body when it has them; RetryAfter is the Retry-After header, or 0 when absent.
type RefusedError struct {
	StatusCode int
	Code       string
	Message    string
	Recovery   string
	RetryAfter time.Duration
}

func (e *RefusedError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = fmt.Sprintf("PackageMaze answered HTTP %d", e.StatusCode)
	}
	if e.Recovery != "" {
		return message + " " + e.Recovery
	}
	return message
}

// Client speaks the Token Flow endpoints under the PackageMaze API root.
type Client struct {
	APIURL     string
	HTTPClient *http.Client
}

func (c Client) Start(ctx context.Context, request StartRequest) (Started, error) {
	var started Started
	status, err := c.post(ctx, "/auth/token-flows", request, &started)
	if err != nil {
		return Started{}, err
	}
	if _, err := time.Parse(time.RFC3339, started.ExpiresAt); err != nil || status != http.StatusCreated || started.FlowID == "" || started.CollectSecret == "" || started.ApprovalURL == "" || started.UserCode == "" {
		return Started{}, errors.New("PackageMaze did not start a Token request")
	}
	return started, nil
}

// Collect answers the Token once approved, or (nil, nil) while the person has not decided.
func (c Client) Collect(ctx context.Context, started Started) (*Collected, error) {
	var collected Collected
	status, err := c.post(ctx, "/auth/token-flows/"+started.FlowID+"/collect", map[string]string{"collect_secret": started.CollectSecret}, &collected)
	if err != nil {
		return nil, err
	}
	if status == http.StatusAccepted {
		return nil, nil
	}
	if collected.TokenSecret == "" {
		return nil, fmt.Errorf("%w: the answer carried no Token", errUnanswered)
	}
	return &collected, nil
}

// Verify proves PackageMaze accepts the Token for reads on one of its Feeds.
func (c Client) Verify(ctx context.Context, tokenSecret string, feedID string) error {
	var result struct {
		Verified bool `json:"verified"`
	}
	if _, err := c.post(ctx, "/tokens/verify", map[string]string{"feed_id": feedID, "required_scope": "read", "token_secret": tokenSecret}, &result); err != nil {
		return err
	}
	if !result.Verified {
		return errors.New("PackageMaze did not verify the Token")
	}
	return nil
}

func (c Client) post(ctx context.Context, child string, body any, out any) (int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.APIURL, "/")+child, bytes.NewReader(encoded))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(version.PackageMazeClientVersionHeader, version.PackageMazeClientVersion())
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, requestFailed(ctx, err)
	}
	defer response.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if response.StatusCode >= 300 {
		return 0, refused(response, content)
	}
	if readErr != nil {
		return 0, fmt.Errorf("%w: read HTTP %d answer: %w", errUnanswered, response.StatusCode, readErr)
	}
	if err := json.Unmarshal(content, out); err != nil {
		return 0, fmt.Errorf("%w: HTTP %d answer was not valid JSON: %w", errUnanswered, response.StatusCode, err)
	}
	return response.StatusCode, nil
}

// requestFailed keeps a certificate failure final: it will not fix itself and may mean interception.
func requestFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if certificate := new(tls.CertificateVerificationError); errors.As(err, &certificate) {
		return fmt.Errorf("PackageMaze request failed: %w", err)
	}
	return fmt.Errorf("%w: %w", errUnanswered, err)
}

func refused(response *http.Response, content []byte) error {
	var payload struct {
		Detail struct {
			Code     string `json:"code"`
			Message  string `json:"message"`
			Recovery string `json:"recovery"`
		} `json:"detail"`
	}
	_ = json.Unmarshal(content, &payload)
	retryAfter, _ := httpretry.RetryAfter(response.Header.Get("Retry-After"), time.Now())
	return &RefusedError{StatusCode: response.StatusCode, Code: payload.Detail.Code, Message: payload.Detail.Message, Recovery: payload.Detail.Recovery, RetryAfter: max(retryAfter, 0)}
}
