package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/httpretry"
	"github.com/packagemaze/maze-cli/internal/identity"
)

const (
	firstRetryDelay = time.Second
	maxRetryDelay   = 15 * time.Second
	// retryWindow ends retries 2 minutes after the first attempt, or sooner
	// when the identity token expires first.
	retryWindow = 2 * time.Minute
)

// newRetrySecret is 32 random bytes, base64url without padding.
func newRetrySecret() string {
	var random [32]byte
	_, _ = rand.Read(random[:])
	return base64.RawURLEncoding.EncodeToString(random[:])
}

type retrier struct {
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
	stderr io.Writer
}

func newRetrier(deps Dependencies) retrier {
	r := retrier{now: deps.Now, sleep: deps.Sleep, stderr: deps.Stderr}
	if r.now == nil {
		r.now = time.Now
	}
	if r.sleep == nil {
		r.sleep = sleepContext
	}
	if r.stderr == nil {
		r.stderr = io.Discard
	}
	return r
}

// exchange resends the identical request, with the same identity token and
// retry secret, while PackageMaze's answer is lost or PackageMaze asks maze to
// come back. PackageMaze answers such a retry by revoking the Token the
// earlier attempt minted and minting a new one; it refuses any other repeat as
// oidc_assertion_replayed, which is final.
func (r retrier) exchange(ctx context.Context, exchanger TokenExchanger, request api.CITokenRequest) (api.CITokenResponse, error) {
	bound := retryBoundFor(request.OIDCToken, r.now())
	for delay := firstRetryDelay; ; delay = min(2*delay, maxRetryDelay) {
		response, err := exchanger.ExchangeCI(ctx, request)
		if err == nil {
			return response, nil
		}
		wait, retry := retryWait(err, delay, r.now())
		if !retry {
			return api.CITokenResponse{}, err
		}
		if wait >= bound.deadline.Sub(r.now()) {
			return api.CITokenResponse{}, fmt.Errorf("maze stopped retrying the PackageMaze token exchange because the next attempt would come %s; the last attempt failed: %w", bound.reason, err)
		}
		_, _ = fmt.Fprintf(r.stderr, "%s; retrying the token exchange in %s\n", retryReason(err), wait.Round(time.Second))
		if err := r.sleep(ctx, wait); err != nil {
			return api.CITokenResponse{}, err
		}
	}
}

// retryWait is how long to wait before resending: delay, or a longer
// Retry-After, after a request with no readable answer, or 429, 502, 503 and
// 504. Any other answer, and a certificate failure, is final.
func retryWait(err error, delay time.Duration, now time.Time) (time.Duration, bool) {
	if errors.Is(err, api.ErrUnanswered) {
		return delay, true
	}
	var status *api.StatusError
	if !errors.As(err, &status) {
		return 0, false
	}
	switch status.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		retryAfter, _ := httpretry.RetryAfter(status.RetryAfter, now)
		return max(delay, retryAfter), true
	default:
		return 0, false
	}
}

func retryReason(err error) string {
	var status *api.StatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("PackageMaze answered HTTP %d", status.StatusCode)
	}
	return "PackageMaze gave no readable answer"
}

type retryBound struct {
	deadline time.Time
	reason   string
}

// retryBoundFor ends retries at the retry window, or sooner when the identity
// token expires: PackageMaze would refuse it after that anyway.
func retryBoundFor(identityToken string, firstAttempt time.Time) retryBound {
	window := retryBound{deadline: firstAttempt.Add(retryWindow), reason: "more than 2 minutes after the first attempt"}
	if expiresAt, ok := identityExpiry(identityToken); ok && expiresAt.Before(window.deadline) {
		return retryBound{deadline: expiresAt, reason: "after the identity token expires"}
	}
	return window
}

// identityExpiry reads the identity token's exp claim without verifying the
// token. It only bounds retries; it is no trust decision.
func identityExpiry(identityToken string) (time.Time, bool) {
	token, err := identity.Decode(identityToken)
	if err != nil {
		return time.Time{}, false
	}
	exp, ok := token.Claims["exp"].(json.Number)
	if !ok {
		return time.Time{}, false
	}
	seconds, err := exp.Int64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func sleepContext(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
