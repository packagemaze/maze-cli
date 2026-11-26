package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
)

// scriptedExchanger fails each attempt with the next of errs, or every
// attempt with always, and records every request.
type scriptedExchanger struct {
	errs     []error
	always   error
	requests []api.CITokenRequest
}

func (s *scriptedExchanger) ExchangeCI(_ context.Context, request api.CITokenRequest) (api.CITokenResponse, error) {
	s.requests = append(s.requests, request)
	if s.always != nil {
		return api.CITokenResponse{}, s.always
	}
	if attempt := len(s.requests); attempt <= len(s.errs) {
		return api.CITokenResponse{}, s.errs[attempt-1]
	}
	return api.CITokenResponse{Token: "maze_ci_real"}, nil
}

// testClock advances only when maze sleeps.
type testClock struct {
	now   time.Time
	waits []time.Duration
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)}
}

func (c *testClock) sleep(_ context.Context, wait time.Duration) error {
	c.waits = append(c.waits, wait)
	c.now = c.now.Add(wait)
	return nil
}

func (c *testClock) waited() time.Duration {
	var total time.Duration
	for _, wait := range c.waits {
		total += wait
	}
	return total
}

type retryRun struct {
	exchanger *scriptedExchanger
	clock     *testClock
	stderr    string
	result    string
	err       error
}

// runRetry exchanges the identity token in MAZE_OIDC_TOKEN, or an injected
// identity when it is empty, against exchanger.
func runRetry(exchanger *scriptedExchanger, identityToken string, clock *testClock) retryRun {
	config := Config{Feeds: []string{"your-org/npm"}, Purpose: "install", IdentityInjected: identityToken == ""}
	env := map[string]string{}
	if identityToken != "" {
		env["MAZE_OIDC_TOKEN"] = identityToken
	}
	var stderr bytes.Buffer
	result, _, err := Exchange(context.Background(), config, Dependencies{
		Env:       mapLookup(env),
		Exchanger: exchanger,
		Now:       func() time.Time { return clock.now },
		Sleep:     clock.sleep,
		Stderr:    &stderr,
	})
	return retryRun{exchanger: exchanger, clock: clock, stderr: stderr.String(), result: result.Token, err: err}
}

func identityTokenWithClaims(claims string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"RS256"}`)) + "." + encode([]byte(claims)) + ".c2lnbmF0dXJl"
}

func identityTokenExpiring(at time.Time) string {
	return identityTokenWithClaims(fmt.Sprintf(`{"iss":"https://ci.example","exp":%d}`, at.Unix()))
}

func unanswered() error { return fmt.Errorf("%w: %w", api.ErrUnanswered, io.EOF) }

func status(code int, retryAfter string) error {
	return &api.StatusError{StatusCode: code, RetryAfter: retryAfter}
}

func TestExchangeSendsAFreshRetrySecretWithEachExchange(t *testing.T) {
	pattern := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	var secrets []string
	for range 2 {
		run := runRetry(&scriptedExchanger{}, "platform-identity", newTestClock())
		if run.err != nil {
			t.Fatalf("Exchange: %v", run.err)
		}
		secret := run.exchanger.requests[0].RetrySecret
		random, err := base64.RawURLEncoding.DecodeString(secret)
		if !pattern.MatchString(secret) || err != nil || len(random) != 32 {
			t.Fatalf("retry secret %q is not 32 random bytes in base64url", secret)
		}
		secrets = append(secrets, secret)
	}
	if secrets[0] == secrets[1] {
		t.Fatalf("two exchanges sent the same retry secret")
	}
}

func TestExchangeResendsTheIdenticalRequestOnARetryableFailure(t *testing.T) {
	clockStart := newTestClock().now
	tests := []struct {
		name     string
		err      error
		wantWait time.Duration
		wantLine string
	}{
		{"no answer", unanswered(), 2 * time.Second, "PackageMaze gave no readable answer; retrying the token exchange in 2s\n"},
		{"unreadable 2xx", &api.MalformedResponseError{Err: io.ErrUnexpectedEOF}, 2 * time.Second, "PackageMaze gave no readable answer; retrying the token exchange in 2s\n"},
		{"502", status(http.StatusBadGateway, ""), 2 * time.Second, "PackageMaze answered HTTP 502; retrying the token exchange in 2s\n"},
		{"503", status(http.StatusServiceUnavailable, ""), 2 * time.Second, "PackageMaze answered HTTP 503; retrying the token exchange in 2s\n"},
		{"504", status(http.StatusGatewayTimeout, ""), 2 * time.Second, "PackageMaze answered HTTP 504; retrying the token exchange in 2s\n"},
		{"429 with Retry-After seconds", status(http.StatusTooManyRequests, "7"), 7 * time.Second, "PackageMaze answered HTTP 429; retrying the token exchange in 7s\n"},
		{"503 with a Retry-After date", status(http.StatusServiceUnavailable, clockStart.Add(30*time.Second).Format(http.TimeFormat)), 30 * time.Second, "PackageMaze answered HTTP 503; retrying the token exchange in 30s\n"},
		{"Retry-After sooner than the delay", status(http.StatusServiceUnavailable, "0"), 2 * time.Second, "PackageMaze answered HTTP 503; retrying the token exchange in 2s\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identityToken := identityTokenExpiring(clockStart.Add(10 * time.Minute))
			run := runRetry(&scriptedExchanger{errs: []error{test.err}}, identityToken, newTestClock())
			if run.err != nil || run.result != "maze_ci_real" {
				t.Fatalf("result = %q, err = %v", run.result, run.err)
			}
			requests := run.exchanger.requests
			if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
				t.Fatalf("requests = %#v", requests)
			}
			if requests[0].OIDCToken != identityToken || requests[0].RetrySecret == "" {
				t.Fatalf("request = %#v", requests[0])
			}
			if !reflect.DeepEqual(run.clock.waits, []time.Duration{test.wantWait}) {
				t.Fatalf("waits = %v", run.clock.waits)
			}
			if run.stderr != test.wantLine {
				t.Fatalf("stderr = %q", run.stderr)
			}
		})
	}
}

func TestExchangeDoesNotRetryARefusal(t *testing.T) {
	for name, refusal := range map[string]error{
		"400":                     status(http.StatusBadRequest, ""),
		"401":                     status(http.StatusUnauthorized, "5"),
		"403":                     status(http.StatusForbidden, ""),
		"404":                     status(http.StatusNotFound, ""),
		"oidc_assertion_replayed": &api.StatusError{StatusCode: http.StatusConflict, Code: "oidc_assertion_replayed"},
		"422 extra_forbidden":     &api.StatusError{StatusCode: http.StatusUnprocessableEntity, Code: "extra_forbidden"},
		"500":                     status(http.StatusInternalServerError, ""),
		"501":                     status(http.StatusNotImplemented, ""),
		"certificate failure":     errors.New("PackageMaze token exchange request failed: x509: certificate signed by unknown authority"),
	} {
		t.Run(name, func(t *testing.T) {
			run := runRetry(&scriptedExchanger{errs: []error{refusal}}, "platform-identity", newTestClock())
			if !errors.Is(run.err, refusal) || len(run.exchanger.requests) != 1 || len(run.clock.waits) != 0 || run.stderr != "" {
				t.Fatalf("err = %v, requests = %d, waits = %v, stderr = %q", run.err, len(run.exchanger.requests), run.clock.waits, run.stderr)
			}
		})
	}
}

func TestExchangeStopsRetryingAtTheIdentityTokenExpiry(t *testing.T) {
	start := newTestClock().now
	tests := []struct {
		name         string
		expiresAt    time.Time
		failure      error
		wantRequests int
		wantWaited   time.Duration
	}{
		{"no time for another attempt", start.Add(5 * time.Second), status(http.StatusServiceUnavailable, ""), 3, 4 * time.Second},
		{"Retry-After past the expiry", start.Add(10 * time.Minute), status(http.StatusServiceUnavailable, "3600"), 1, 0},
		{"already expired", start.Add(-time.Minute), unanswered(), 1, 0},
		{"an expiry later than 60 seconds", start.Add(5 * time.Minute), unanswered(), 150, 298 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runRetry(&scriptedExchanger{always: test.failure}, identityTokenExpiring(test.expiresAt), newTestClock())
			if len(run.exchanger.requests) != test.wantRequests || run.clock.waited() != test.wantWaited {
				t.Fatalf("requests = %d, waited = %v", len(run.exchanger.requests), run.clock.waited())
			}
			if !errors.Is(run.err, test.failure) || !strings.HasPrefix(run.err.Error(), "maze stopped retrying the PackageMaze token exchange because the next attempt would come after the identity token expires; the last attempt failed: ") {
				t.Fatalf("err = %v", run.err)
			}
		})
	}
}

func TestExchangeStopsRetryingAfterSixtySecondsWhenTheExpiryIsUnknown(t *testing.T) {
	for name, identityToken := range map[string]string{
		"not a JWT":         "platform-identity",
		"no exp claim":      identityTokenWithClaims(`{"iss":"https://ci.example"}`),
		"exp not a number":  identityTokenWithClaims(`{"exp":"tomorrow"}`),
		"injected identity": "",
	} {
		t.Run(name, func(t *testing.T) {
			run := runRetry(&scriptedExchanger{always: unanswered()}, identityToken, newTestClock())
			requests := run.exchanger.requests
			if len(requests) != 30 || run.clock.waited() != 58*time.Second {
				t.Fatalf("requests = %d, waited = %v", len(requests), run.clock.waited())
			}
			for _, request := range requests {
				if !reflect.DeepEqual(request, requests[0]) || request.OIDCToken != identityToken {
					t.Fatalf("a retry changed the request: %#v", request)
				}
			}
			if !errors.Is(run.err, api.ErrUnanswered) || !strings.Contains(run.err.Error(), "the next attempt would come more than 60 seconds after the first attempt; the last attempt failed: ") {
				t.Fatalf("err = %v", run.err)
			}
			if strings.Count(run.stderr, "\n") != 29 || strings.Contains(run.stderr, requests[0].RetrySecret) {
				t.Fatalf("stderr = %q", run.stderr)
			}
		})
	}
}

func TestExchangeStopsRetryingWhenCancelledWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exchanger := &scriptedExchanger{always: unanswered()}
	_, _, err := Exchange(ctx, Config{Feeds: []string{"your-org/npm"}, Purpose: "install"}, Dependencies{
		Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "platform-identity"}),
		Exchanger: exchanger,
		Sleep: func(ctx context.Context, wait time.Duration) error {
			cancel()
			return sleepContext(ctx, time.Hour)
		},
	})
	if !errors.Is(err, context.Canceled) || len(exchanger.requests) != 1 {
		t.Fatalf("err = %v, requests = %d", err, len(exchanger.requests))
	}
}
