package ci

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type oidcRoundTripper func(*http.Request) (*http.Response, error)

func (f oidcRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type oidcTimeoutError struct{}

func (oidcTimeoutError) Error() string   { return "timeout awaiting headers" }
func (oidcTimeoutError) Timeout() bool   { return true }
func (oidcTimeoutError) Temporary() bool { return true }

type trackedOIDCBody struct {
	io.Reader
	closed bool
}

func (body *trackedOIDCBody) Close() error {
	body.closed = true
	return nil
}

func oidcResponse(status int, body *trackedOIDCBody) *http.Response {
	return &http.Response{StatusCode: status, Body: body, Header: make(http.Header)}
}

func oidcTestEnv() LookupEnv {
	return mapLookup(map[string]string{
		"ACTIONS_ID_TOKEN_REQUEST_URL":   "https://github.example/token?existing=true",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "runtime-token",
	})
}

func TestRequestGitHubOIDCTokenRetriesTimeout(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: oidcRoundTripper(func(request *http.Request) (*http.Response, error) {
		attempts++
		if request.Method != http.MethodGet || request.URL.Query().Get("audience") != "https://api.packagemaze.com" || request.URL.Query().Get("existing") != "true" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "bearer runtime-token" || request.Header.Get("Accept") != "application/json" {
			t.Error("request headers were not preserved")
		}
		if attempts == 1 {
			return nil, oidcTimeoutError{}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"value":"github-oidc-token"}`)), Header: make(http.Header)}, nil
	})}
	token, err := RequestGitHubOIDCToken(context.Background(), "https://api.packagemaze.com", oidcTestEnv(), client)
	if err != nil || token != "github-oidc-token" || attempts != 2 {
		t.Fatalf("token = %q, attempts = %d, error = %v", token, attempts, err)
	}
}

func TestRequestGitHubOIDCTokenRetriesTransientResponses(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			attempts := 0
			firstBody := &trackedOIDCBody{Reader: strings.NewReader("runtime-token")}
			lastBody := &trackedOIDCBody{Reader: strings.NewReader(`{"value":"github-oidc-token"}`)}
			client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return oidcResponse(status, firstBody), nil
				}
				if !firstBody.closed {
					t.Error("previous response body was not closed before retry")
				}
				return oidcResponse(http.StatusOK, lastBody), nil
			})}
			token, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
			if err != nil || token != "github-oidc-token" || attempts != 2 || !firstBody.closed || !lastBody.closed {
				t.Fatalf("attempts = %d, bodies closed = %v/%v, error = %v", attempts, firstBody.closed, lastBody.closed, err)
			}
		})
	}
}

func TestRequestGitHubOIDCTokenDoesNotRetryRefusalsOrInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"bad request", 400, "runtime-token"},
		{"unauthorized", 401, "runtime-token"},
		{"forbidden", 403, "runtime-token"},
		{"not found", 404, "runtime-token"},
		{"not implemented", 501, "runtime-token"},
		{"invalid JSON", 200, "runtime-token"},
		{"missing token", 200, `{}`},
		{"multiple lines", 200, `{"value":"secret\nother"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			body := &trackedOIDCBody{Reader: strings.NewReader(test.body)}
			client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
				attempts++
				return oidcResponse(test.status, body), nil
			})}
			_, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
			if err == nil || attempts != 1 || !body.closed {
				t.Fatalf("attempts = %d, body closed = %v, error = %v", attempts, body.closed, err)
			}
			if strings.Contains(err.Error(), "runtime-token") || strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed a token from the response")
			}
		})
	}
}

func TestRequestGitHubOIDCTokenRetriesOnlyTransientTransportErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		attempts int
	}{
		{"timeout", oidcTimeoutError{}, 2},
		{"EOF", io.EOF, 2},
		{"unexpected EOF", io.ErrUnexpectedEOF, 2},
		{"connection reset", syscall.ECONNRESET, 2},
		{"connection aborted", syscall.ECONNABORTED, 2},
		{"broken pipe", syscall.EPIPE, 2},
		{"canceled", context.Canceled, 1},
		{"other transport error", errors.New("runtime-token transport refusal"), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
				attempts++
				return nil, test.err
			})}
			_, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
			if err == nil || attempts != test.attempts || !errors.Is(err, test.err) {
				t.Fatalf("attempts = %d, error = %v", attempts, err)
			}
			if strings.Contains(err.Error(), "runtime-token") || strings.Contains(err.Error(), "github.example") {
				t.Fatal("error exposed request credentials or URL")
			}
		})
	}
}

func TestRequestGitHubOIDCTokenExhaustsRetryableStatus(t *testing.T) {
	attempts := 0
	var bodies []*trackedOIDCBody
	client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
		attempts++
		body := &trackedOIDCBody{Reader: strings.NewReader("runtime-token")}
		bodies = append(bodies, body)
		return oidcResponse(http.StatusServiceUnavailable, body), nil
	})}
	_, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
	if err == nil || attempts != 2 || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "runtime-token") {
		t.Fatalf("attempts = %d, error = %v", attempts, err)
	}
	for _, body := range bodies {
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	}
}

func TestRequestGitHubOIDCTokenRespectsRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		wantGETs   int
		wantDelay  time.Duration
	}{
		{"zero", 429, "0", 2, 0},
		{"one second", 429, "1", 2, time.Second},
		{"too long", 429, "30", 1, 0},
		{"date too far away", 503, time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), 1, 0},
		{"invalid", 503, "invalid", 1, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attempts := 0
			firstBody := &trackedOIDCBody{Reader: strings.NewReader("")}
			client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					response := oidcResponse(test.status, firstBody)
					response.Header.Set("Retry-After", test.retryAfter)
					return response, nil
				}
				return oidcResponse(200, &trackedOIDCBody{Reader: strings.NewReader(`{"value":"github-oidc-token"}`)}), nil
			})}
			start := time.Now()
			_, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
			if attempts != test.wantGETs || (err != nil) != (test.wantGETs == 1) || !firstBody.closed {
				t.Fatalf("attempts = %d, body closed = %v, error = %v", attempts, firstBody.closed, err)
			}
			if elapsed := time.Since(start); elapsed < test.wantDelay {
				t.Fatalf("retry waited %s, want at least %s", elapsed, test.wantDelay)
			}
		})
	}
}

func TestRequestGitHubOIDCTokenHonorsCancellationAndDeadline(t *testing.T) {
	t.Run("canceled on failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		attempts := 0
		client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
			attempts++
			cancel()
			return nil, oidcTimeoutError{}
		})}
		_, err := RequestGitHubOIDCToken(ctx, "audience", oidcTestEnv(), client)
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("attempts = %d, error = %v", attempts, err)
		}
	})
	t.Run("deadline during delay", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		attempts := 0
		client := &http.Client{Transport: oidcRoundTripper(func(_ *http.Request) (*http.Response, error) {
			attempts++
			return nil, oidcTimeoutError{}
		})}
		_, err := RequestGitHubOIDCToken(ctx, "audience", oidcTestEnv(), client)
		if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
			t.Fatalf("attempts = %d, error = %v", attempts, err)
		}
	})
	t.Run("deadline during request", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		attempts := 0
		client := &http.Client{Transport: oidcRoundTripper(func(request *http.Request) (*http.Response, error) {
			attempts++
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		_, err := RequestGitHubOIDCToken(ctx, "audience", oidcTestEnv(), client)
		if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
			t.Fatalf("attempts = %d, error = %v", attempts, err)
		}
	})
}

func TestRequestGitHubOIDCTokenRetriesClientTimeout(t *testing.T) {
	attempts := 0
	client := &http.Client{Timeout: 25 * time.Millisecond, Transport: oidcRoundTripper(func(request *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return oidcResponse(http.StatusOK, &trackedOIDCBody{Reader: strings.NewReader(`{"value":"github-oidc-token"}`)}), nil
	})}
	token, err := RequestGitHubOIDCToken(context.Background(), "audience", oidcTestEnv(), client)
	if err != nil || token != "github-oidc-token" || attempts != 2 {
		t.Fatalf("attempts = %d, error = %v", attempts, err)
	}
}
