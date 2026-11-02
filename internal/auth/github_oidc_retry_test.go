package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/api"
)

type oidcTransport func(*http.Request) (*http.Response, error)

func (transport oidcTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type oidcTimeout struct{}

func (oidcTimeout) Error() string   { return "timeout awaiting headers" }
func (oidcTimeout) Timeout() bool   { return true }
func (oidcTimeout) Temporary() bool { return true }

type countingOIDCExchanger struct {
	calls int
	err   error
}

func (exchanger *countingOIDCExchanger) ExchangeCI(_ context.Context, request api.CITokenRequest) (api.CITokenResponse, error) {
	exchanger.calls++
	if request.OIDCToken != "github-oidc-token" {
		return api.CITokenResponse{}, errors.New("unexpected OIDC token")
	}
	return api.CITokenResponse{Token: "maze-token"}, exchanger.err
}

func TestGitHubOIDCRetryDoesNotReplayExchange(t *testing.T) {
	exchangeError := errors.New("exchange failed")
	tests := []struct {
		name          string
		timeouts      int
		exchangeError error
		wantGETs      int
		wantExchanges int
		wantErr       bool
	}{
		{"success", 0, nil, 1, 1, false},
		{"timeout then success", 1, nil, 2, 1, false},
		{"timeout exhaustion", 2, nil, 2, 0, true},
		{"exchange failure", 0, exchangeError, 1, 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			exchanger := &countingOIDCExchanger{err: test.exchangeError}
			client := &http.Client{Transport: oidcTransport(func(_ *http.Request) (*http.Response, error) {
				attempts++
				if attempts <= test.timeouts {
					return nil, oidcTimeout{}
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"github-oidc-token"}`)), Header: make(http.Header)}, nil
			})}
			_, _, err := Exchange(context.Background(), Config{Feed: "your-org/npm", Purpose: "install", Provider: "github"}, Dependencies{
				Env: mapLookup(map[string]string{
					"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://github.example/token",
					"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runtime-token",
				}),
				HTTPClient: client,
				Exchanger:  exchanger,
			})
			if (err != nil) != test.wantErr || attempts != test.wantGETs || exchanger.calls != test.wantExchanges {
				t.Fatalf("GETs = %d, exchanges = %d, error = %v", attempts, exchanger.calls, err)
			}
			if test.exchangeError != nil && !errors.Is(err, test.exchangeError) {
				t.Fatalf("exchange error was not preserved: %v", err)
			}
		})
	}
}
