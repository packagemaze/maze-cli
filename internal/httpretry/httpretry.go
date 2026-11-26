// Package httpretry classifies HTTP failures for callers that decide on their own whether to retry.
package httpretry

import (
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CertificateFailure reports a server certificate the client refused. Retrying will not fix it,
// and it may mean the connection is intercepted.
func CertificateFailure(err error) bool {
	certificate := new(tls.CertificateVerificationError)
	return errors.As(err, &certificate)
}

// ConnectionDropped reports a connection that timed out, reset or closed mid-exchange: the
// request may have reached the server. DNS, TLS, refused connections and cancellation are not.
func ConnectionDropped(err error) bool {
	var networkError net.Error
	return (errors.As(err, &networkError) && networkError.Timeout()) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		platformConnectionDropped(err)
}

// RetryAfter reads a Retry-After value in seconds or as an HTTP date; ok is false when it is neither.
func RetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > math.MaxInt64/uint64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return date.Sub(now), true
	}
	return 0, false
}
