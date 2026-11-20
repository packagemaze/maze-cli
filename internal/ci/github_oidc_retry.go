package ci

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/httpretry"
)

const (
	gitHubOIDCRetryDelay   = 250 * time.Millisecond
	gitHubOIDCMaxRetryWait = time.Second
)

func requestGitHubOIDCWithRetry(ctx context.Context, client *http.Client, request *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		response, err := client.Do(request)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == 1 {
			return response, err
		}
		delay, retry := gitHubOIDCRetryWait(response, err)
		if !retry {
			return response, err
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		request = request.Clone(ctx)
	}
}

func gitHubOIDCRetryWait(response *http.Response, err error) (time.Duration, bool) {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, false
		}
		return gitHubOIDCRetryDelay, httpretry.ConnectionDropped(err)
	}
	switch response.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return 0, false
	}
	return gitHubOIDCWaitForRetryAfter(response.Header.Get("Retry-After"))
}

func gitHubOIDCWaitForRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return gitHubOIDCRetryDelay, true
	}
	wait, ok := httpretry.RetryAfter(value, time.Now())
	if !ok {
		return 0, false
	}
	if wait > gitHubOIDCMaxRetryWait {
		return 0, false
	}
	return max(wait, gitHubOIDCRetryDelay), true
}

type safeOIDCRequestError struct {
	cause  error
	secret string
}

func (err safeOIDCRequestError) Error() string {
	return strings.ReplaceAll(err.cause.Error(), err.secret, "[redacted]")
}

func (err safeOIDCRequestError) Unwrap() error { return err.cause }

func safeGitHubOIDCError(err error, secret string) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		err = urlError.Err
	}
	return safeOIDCRequestError{cause: err, secret: secret}
}
