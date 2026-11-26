package httpretry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func TestConnectionDroppedOnlyWhenTheRequestMayHaveArrived(t *testing.T) {
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, fmt.Errorf("post: %w", syscall.EPIPE), &net.OpError{Op: "read", Err: timeoutError{}}} {
		if !ConnectionDropped(err) {
			t.Fatalf("%v: not dropped", err)
		}
	}
	for _, err := range []error{context.Canceled, &net.DNSError{Err: "no such host", Name: "x.test"}, x509.UnknownAuthorityError{}, syscall.ECONNREFUSED, errors.New("other")} {
		if ConnectionDropped(err) {
			t.Fatalf("%v: dropped", err)
		}
	}
}

func TestCertificateFailureOnlyForARefusedCertificate(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "https://api.packagemaze.test", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}
	if !CertificateFailure(refused) {
		t.Fatalf("%v: not a certificate failure", refused)
	}
	for _, err := range []error{io.EOF, syscall.ECONNRESET, &net.DNSError{Err: "no such host", Name: "x.test"}, errors.New("tls: handshake failure")} {
		if CertificateFailure(err) {
			t.Fatalf("%v: a certificate failure", err)
		}
	}
}

func TestRetryAfterReadsSecondsAndDates(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"7":                             7 * time.Second,
		" 0 ":                           0,
		"Fri, 20 Nov 2026 10:00:30 GMT": 30 * time.Second,
		"Fri, 20 Nov 2026 09:59:00 GMT": -time.Minute,
	}
	for value, want := range cases {
		if got, ok := RetryAfter(value, now); !ok || got != want {
			t.Fatalf("%q = %v, %v", value, got, ok)
		}
	}
	if got, ok := RetryAfter("18446744073709551615", now); !ok || got <= 0 {
		t.Fatalf("huge seconds = %v, %v", got, ok)
	}
	for _, value := range []string{"", "soon", "-3", "99999999999999999999"} {
		if _, ok := RetryAfter(value, now); ok {
			t.Fatalf("%q parsed", value)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
