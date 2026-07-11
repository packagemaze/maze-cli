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
)

type Client struct {
	apiURL     string
	httpClient *http.Client
}

type CITokenRequest struct {
	Provider          string  `json:"provider"`
	Feed              string  `json:"feed"`
	Purpose           string  `json:"purpose"`
	Package           *string `json:"package"`
	Audience          string  `json:"audience"`
	OIDCToken         string  `json:"oidc_token"`
	SetupInvocationID string  `json:"setup_invocation_id,omitempty"`
	// Client is an explicit legacy compatibility field. It is caller-supplied,
	// unverified, and is not PackageMaze Build evidence.
	Client map[string]any `json:"client,omitempty"`
}

type CITokenResponse struct {
	Token            string
	ExpiresAt        time.Time
	TokenType        string
	Feed             string
	FeedBaseURL      string
	ExchangePurpose  string
	BuildID          string
	Scopes           []string
	ArtifactProtocol string
}

type ContractResponseError struct {
	Endpoint string
	Detail   string
}

func (e *ContractResponseError) Error() string {
	return fmt.Sprintf("PackageMaze token exchange response from %s violated the API contract: %s", e.Endpoint, e.Detail)
}

type StatusError struct {
	StatusCode int
	Endpoint   string
	Detail     string
	Code       string
	Recovery   string
	Provider   string
	Feed       string
	Purpose    string
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
		return fmt.Sprintf(`PackageMaze rejected this CI identity.
Feed:
  %s
Provider:
  %s
Purpose:
  %s
The backend said:
  %s%s%s
Check:
  - the CI trust rule configured for this Feed
  - the workflow, branch, or tag rule
  - the PackageMaze Feed name`, e.Feed, e.Provider, e.Purpose, detail, code, recovery)
	}
	if e.StatusCode >= 500 {
		return fmt.Sprintf("PackageMaze token exchange failed because the server returned HTTP %d: %s%s%s", e.StatusCode, detail, code, recovery)
	}
	return fmt.Sprintf("PackageMaze token exchange request was rejected with HTTP %d: %s%s%s", e.StatusCode, detail, code, recovery)
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

func NewClient(apiURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{apiURL: strings.TrimRight(apiURL, "/"), httpClient: httpClient}
}

func (c *Client) ExchangeCI(ctx context.Context, request CITokenRequest) (CITokenResponse, error) {
	endpoint, err := joinEndpoint(c.apiURL, "auth/ci-token")
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

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return CITokenResponse{}, fmt.Errorf("PackageMaze token exchange request failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		detail := responseDetail(response.Body)
		return CITokenResponse{}, &StatusError{
			StatusCode: response.StatusCode,
			Endpoint:   endpoint,
			Detail:     redactSecret(detail.Message, request.OIDCToken),
			Code:       redactSecret(detail.Code, request.OIDCToken),
			Recovery:   redactSecret(detail.Recovery, request.OIDCToken),
			Provider:   request.Provider,
			Feed:       request.Feed,
			Purpose:    request.Purpose,
		}
	}

	var payload struct {
		Token            string   `json:"token"`
		ExpiresAt        string   `json:"expires_at"`
		TokenType        string   `json:"token_type"`
		Feed             string   `json:"feed"`
		FeedBaseURL      string   `json:"feed_base_url"`
		Purpose          string   `json:"purpose"`
		ExchangePurpose  string   `json:"exchange_purpose"`
		BuildID          string   `json:"build_id"`
		CISessionID      string   `json:"ci_session_id"`
		Scopes           []string `json:"scopes"`
		ArtifactProtocol string   `json:"artifact_protocol"`
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
	buildID := strings.TrimSpace(payload.BuildID)
	ciSessionID := strings.TrimSpace(payload.CISessionID)
	if buildID != "" && ciSessionID != "" && buildID != ciSessionID {
		return CITokenResponse{}, &ContractResponseError{
			Endpoint: endpoint,
			Detail:   "build_id and ci_session_id identified different Builds",
		}
	}
	if buildID == "" {
		buildID = ciSessionID
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
		BuildID:          buildID,
		Scopes:           payload.Scopes,
		ArtifactProtocol: payload.ArtifactProtocol,
	}, nil
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
	Code     string
	Message  string
	Recovery string
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
			Code:     firstStringValue(typed, "code", "state", "type"),
			Message:  firstStringValue(typed, "message", "reason", "msg", "error_description", "error"),
			Recovery: firstStringValue(typed, "recovery"),
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

func redactSecret(value string, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}
