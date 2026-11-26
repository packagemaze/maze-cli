package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/packagemaze/maze-cli/internal/httpretry"
	"github.com/packagemaze/maze-cli/internal/version"
)

type Client struct {
	apiURL     string
	httpClient *http.Client
}

type CITokenRequest struct {
	// Provider is omitted, never null, when empty: PackageMaze then lets the
	// identity token's issuer select the verifier, and refuses a null.
	Provider string  `json:"provider,omitempty"`
	Feed     string  `json:"feed,omitempty"`
	Purpose  string  `json:"purpose"`
	Package  *string `json:"package"`
	Audience string  `json:"audience"`
	// OIDCToken is omitted when the platform adds the identity token to the
	// request as an Authorization Bearer header; PackageMaze reads it there.
	OIDCToken         string `json:"oidc_token,omitempty"`
	SetupInvocationID string `json:"setup_invocation_id,omitempty"`
	// Client is an explicit legacy compatibility field. It is caller-supplied,
	// unverified, and is not PackageMaze Build evidence.
	Client map[string]any `json:"client,omitempty"`
	// Reported holds values the job states about itself. PackageMaze shows
	// them as reported and never uses them in an access rule.
	Reported *ReportedContext `json:"reported,omitempty"`
	// Feeds names several Feeds of one Organization in place of Feed. It needs
	// a PackageMaze server that accepts it.
	Feeds []string `json:"feeds,omitempty"`
	// RetrySecret proves a resent request is the same exchange: PackageMaze
	// answers the same identity token, secret and request by revoking the
	// Token it minted for them and minting a new one. It is a credential.
	RetrySecret string `json:"retry_secret,omitempty"`
}

type ReportedContext struct {
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
}

type CITokenResponse struct {
	Token            string
	ExpiresAt        time.Time
	TokenType        string
	Feed             string
	FeedBaseURL      string
	ExchangePurpose  string
	BuildNumber      int64
	BuildURL         string
	Scopes           []string
	ArtifactProtocol string
	// Feeds is PackageMaze's answer to a Feeds request, one entry per Feed, in
	// place of Feed, FeedBaseURL, and ArtifactProtocol.
	Feeds json.RawMessage
}

// ErrUnanswered marks an exchange PackageMaze may or may not have handled: the
// request failed in transit, or a 2xx answer could not be read.
var ErrUnanswered = errors.New("PackageMaze did not answer the token exchange")

type StatusError struct {
	StatusCode int
	Endpoint   string
	Detail     string
	Code       string
	Recovery   string
	SignInID   string
	DetailsURL string
	Provider   string
	Feed       string
	Purpose    string
	// RetryAfter is the answer's Retry-After header, as sent.
	RetryAfter string
}

func (e *StatusError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	code := ""
	if strings.TrimSpace(e.Code) != "" {
		code = fmt.Sprintf("\nError code:\n  %s", strings.TrimSpace(e.Code))
	}
	recovery := ""
	if strings.TrimSpace(e.Recovery) != "" {
		recovery = fmt.Sprintf("\nPackageMaze suggests:\n  %s", strings.TrimSpace(e.Recovery))
	}
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		provider := strings.TrimSpace(e.Provider)
		if provider == "" {
			provider = "none named; the identity token's issuer selects it"
		}
		refusal := fmt.Sprintf(`PackageMaze rejected this workload identity.
Feed:
  %s
Provider:
  %s
Purpose:
  %s
The backend said:
  %s%s%s`, e.Feed, provider, e.Purpose, detail, code, recovery)
		if detailsURL, ok := httpsURL(e.DetailsURL); ok {
			return refusal + "\n" + refusalDetails(detailsURL, e.SignInID)
		}
		return refusal + `
Check:
  - the access rule on this Feed and what it requires of the identity
  - the platform that issued the identity token, and its audience
  - the PackageMaze Feed name`
	}
	if e.StatusCode >= 500 {
		return fmt.Sprintf("PackageMaze token exchange failed because the server returned HTTP %d: %s%s%s", e.StatusCode, detail, code, recovery)
	}
	return fmt.Sprintf("PackageMaze token exchange request was rejected with HTTP %d: %s%s%s", e.StatusCode, detail, code, recovery)
}

// refusalDetails replaces the guessed checklist when PackageMaze names a
// members-only page that holds the real reason for the refusal.
func refusalDetails(detailsURL string, signInID string) string {
	lines := "Details (sign in to PackageMaze): " + escapeControl(detailsURL)
	if signInID = strings.TrimSpace(signInID); signInID != "" {
		lines += "\nSign-in id: " + escapeControl(signInID)
	}
	return lines
}

func httpsURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" {
		return "", false
	}
	return value, true
}

func escapeControl(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch {
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&out, `\u%04x`, r)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

type MalformedResponseError struct {
	Endpoint string
	Err      error
}

func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("PackageMaze token exchange response from %s was not valid JSON", e.Endpoint)
}

func (e *MalformedResponseError) Unwrap() error {
	return e.Err
}

func (e *MalformedResponseError) Is(target error) bool {
	return target == ErrUnanswered
}

func NewClient(apiURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{apiURL: strings.TrimRight(apiURL, "/"), httpClient: httpClient}
}

func (c *Client) ExchangeCI(ctx context.Context, request CITokenRequest) (CITokenResponse, error) {
	endpoint, err := joinEndpoint(c.apiURL, "auth/workload-token")
	if err != nil {
		return CITokenResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return CITokenResponse{}, fmt.Errorf("encode token exchange request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return CITokenResponse{}, fmt.Errorf("build token exchange request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(version.PackageMazeClientVersionHeader, version.PackageMazeClientVersion())

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return CITokenResponse{}, requestFailed(ctx, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return CITokenResponse{}, statusError(response, endpoint, request)
	}

	var payload struct {
		Token            string          `json:"token"`
		ExpiresAt        string          `json:"expires_at"`
		TokenType        string          `json:"token_type"`
		Feed             string          `json:"feed"`
		FeedBaseURL      string          `json:"feed_base_url"`
		Purpose          string          `json:"purpose"`
		ExchangePurpose  string          `json:"exchange_purpose"`
		BuildNumber      int64           `json:"build_number"`
		BuildURL         string          `json:"build_url"`
		Scopes           []string        `json:"scopes"`
		ArtifactProtocol string          `json:"artifact_protocol"`
		Feeds            json.RawMessage `json:"feeds"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&payload); err != nil {
		return CITokenResponse{}, &MalformedResponseError{Endpoint: endpoint, Err: err}
	}
	if strings.TrimSpace(payload.Token) == "" {
		return CITokenResponse{}, &MalformedResponseError{Endpoint: endpoint, Err: errors.New("missing token")}
	}
	expiresAt, err := time.Parse(time.RFC3339, payload.ExpiresAt)
	if err != nil {
		return CITokenResponse{}, &MalformedResponseError{Endpoint: endpoint, Err: err}
	}
	if payload.TokenType == "" {
		payload.TokenType = "Bearer"
	}
	if payload.Feed == "" {
		payload.Feed = request.Feed
	}
	exchangePurpose := strings.TrimSpace(payload.ExchangePurpose)
	if !validCIExchangePurpose(exchangePurpose) {
		exchangePurpose = strings.TrimSpace(payload.Purpose)
	}
	if !validCIExchangePurpose(exchangePurpose) {
		exchangePurpose = request.Purpose
	}
	if payload.Scopes == nil {
		payload.Scopes = []string{}
	}
	return CITokenResponse{
		Token:            payload.Token,
		ExpiresAt:        expiresAt,
		TokenType:        payload.TokenType,
		Feed:             payload.Feed,
		FeedBaseURL:      payload.FeedBaseURL,
		ExchangePurpose:  exchangePurpose,
		BuildNumber:      payload.BuildNumber,
		BuildURL:         payload.BuildURL,
		Scopes:           payload.Scopes,
		ArtifactProtocol: payload.ArtifactProtocol,
		Feeds:            payload.Feeds,
	}, nil
}

// requestFailed marks a request that failed in transit as unanswered, unless
// it was cancelled or the server's certificate was refused.
func requestFailed(ctx context.Context, err error) error {
	if ctx.Err() != nil || httpretry.CertificateFailure(err) {
		return fmt.Errorf("PackageMaze token exchange request failed: %w", err)
	}
	return fmt.Errorf("%w: %w", ErrUnanswered, err)
}

func statusError(response *http.Response, endpoint string, request CITokenRequest) *StatusError {
	detail := responseDetail(response.Body)
	redact := func(value string) string {
		return redactSecrets(value, request.OIDCToken, request.RetrySecret)
	}
	return &StatusError{
		StatusCode: response.StatusCode,
		Endpoint:   endpoint,
		Detail:     redact(detail.Message),
		Code:       redact(detail.Code),
		Recovery:   redact(detail.Recovery),
		SignInID:   redact(detail.SignInID),
		DetailsURL: redact(detail.DetailsURL),
		Provider:   request.Provider,
		Feed:       request.Feed + strings.Join(request.Feeds, ", "),
		Purpose:    request.Purpose,
		RetryAfter: response.Header.Get("Retry-After"),
	}
}

func validCIExchangePurpose(value string) bool {
	switch strings.TrimSpace(value) {
	case "docker-build", "install", "publish", "test":
		return true
	default:
		return false
	}
}

func joinEndpoint(apiURL string, child string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("PackageMaze API URL is invalid: %w", err)
	}
	parsed.Path = path.Join(parsed.Path, child)
	return parsed.String(), nil
}

type errorResponseDetail struct {
	Code       string
	Message    string
	Recovery   string
	SignInID   string
	DetailsURL string
}

func responseDetail(body io.Reader) errorResponseDetail {
	content, err := io.ReadAll(io.LimitReader(body, 64*1024))
	if err != nil {
		return errorResponseDetail{}
	}
	var payload any
	if err := json.Unmarshal(content, &payload); err == nil {
		if object, ok := payload.(map[string]any); ok {
			if detail, exists := object["detail"]; exists {
				return parsedErrorDetail(detail)
			}
			return parsedErrorDetail(object)
		}
	}
	return errorResponseDetail{Message: strings.TrimSpace(string(content))}
}

func parsedErrorDetail(value any) errorResponseDetail {
	switch typed := value.(type) {
	case string:
		return errorResponseDetail{Message: strings.TrimSpace(typed)}
	case map[string]any:
		return errorResponseDetail{
			Code:       firstStringValue(typed, "code", "state", "type"),
			Message:    firstStringValue(typed, "message", "reason", "msg", "error_description", "error"),
			Recovery:   firstStringValue(typed, "recovery"),
			SignInID:   firstStringValue(typed, "sign_in_id"),
			DetailsURL: firstStringValue(typed, "details_url"),
		}
	case []any:
		messages := make([]string, 0, min(len(typed), 5))
		code := ""
		for _, entry := range typed {
			object, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if code == "" {
				code = firstStringValue(object, "code", "type")
			}
			message := firstStringValue(object, "message", "msg", "reason")
			if message == "" {
				continue
			}
			if location := validationLocation(object["loc"]); location != "" {
				message = location + ": " + message
			}
			messages = append(messages, message)
			if len(messages) == 5 {
				break
			}
		}
		if len(messages) == 0 {
			return errorResponseDetail{Message: "PackageMaze returned structured error details."}
		}
		if len(typed) > len(messages) {
			messages = append(messages, fmt.Sprintf("and %d more validation errors", len(typed)-len(messages)))
		}
		return errorResponseDetail{Code: code, Message: strings.Join(messages, "; ")}
	default:
		return errorResponseDetail{Message: "PackageMaze returned structured error details."}
	}
}

func firstStringValue(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := object[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validationLocation(value any) string {
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	location := make([]string, 0, len(parts))
	for _, part := range parts {
		var text string
		switch typed := part.(type) {
		case string:
			text = strings.TrimSpace(typed)
		case float64:
			text = fmt.Sprintf("%.0f", typed)
		}
		if text != "" && !(len(location) == 0 && text == "body") {
			location = append(location, text)
		}
	}
	return strings.Join(location, ".")
}

func redactSecrets(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}
