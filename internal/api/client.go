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
	Provider  string  `json:"provider"`
	Feed      string  `json:"feed"`
	Purpose   string  `json:"purpose"`
	Package   *string `json:"package"`
	Audience  string  `json:"audience"`
	OIDCToken string  `json:"oidc_token"`
}

type CITokenResponse struct {
	Token            string
	ExpiresAt        time.Time
	TokenType        string
	Feed             string
	Purpose          string
	Scopes           []string
	ArtifactProtocol string
}

type StatusError struct {
	StatusCode int
	Endpoint   string
	Detail     string
	Provider   string
	Feed       string
	Purpose    string
}

func (e *StatusError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
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
  %s
Check:
  - the CI trust rule configured for this Feed
  - the workflow, branch, or tag rule
  - the PackageMaze Feed name`, e.Feed, e.Provider, e.Purpose, detail)
	}
	if e.StatusCode >= 500 {
		return fmt.Sprintf("PackageMaze token exchange failed because the server returned HTTP %d: %s", e.StatusCode, detail)
	}
	return fmt.Sprintf("PackageMaze token exchange request was rejected with HTTP %d: %s", e.StatusCode, detail)
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
		return CITokenResponse{}, &StatusError{
			StatusCode: response.StatusCode,
			Endpoint:   endpoint,
			Detail:     redactSecret(responseDetail(response.Body), request.OIDCToken),
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
		Purpose          string   `json:"purpose"`
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
	if payload.Purpose == "" {
		payload.Purpose = request.Purpose
	}
	if payload.Scopes == nil {
		payload.Scopes = []string{}
	}
	return CITokenResponse{
		Token:            payload.Token,
		ExpiresAt:        expiresAt,
		TokenType:        payload.TokenType,
		Feed:             payload.Feed,
		Purpose:          payload.Purpose,
		Scopes:           payload.Scopes,
		ArtifactProtocol: payload.ArtifactProtocol,
	}, nil
}

func joinEndpoint(apiURL string, child string) (string, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("PackageMaze API URL is invalid: %w", err)
	}
	parsed.Path = path.Join(parsed.Path, child)
	return parsed.String(), nil
}

func responseDetail(body io.Reader) string {
	content, err := io.ReadAll(io.LimitReader(body, 64*1024))
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(content, &payload); err == nil {
		for _, key := range []string{"detail", "message", "error_description", "error"} {
			if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return strings.TrimSpace(string(content))
}

func redactSecret(value string, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[redacted]")
}
