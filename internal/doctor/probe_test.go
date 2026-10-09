package doctor

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPProberSpeaksLikeEachPackageClient(t *testing.T) {
	var seen []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.Clone(context.Background()))
		switch request.URL.Path {
		case "/acme/npm/-/whoami":
			if request.Header.Get("Authorization") != "Bearer pm_good" {
				writer.Header().Set("WWW-Authenticate", `Bearer realm="PackageMaze", error="invalid_token"`)
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"detail":"PackageMaze Token was rejected."}`))
				return
			}
			_, _ = writer.Write([]byte(`{"username":"kko"}`))
		case "/acme/pypi/simple/":
			expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("__token__:pm_good"))
			if request.Header.Get("Authorization") != expected {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = writer.Write([]byte("<html><body><a href=\"demo/\">demo</a></body></html>"))
		case "/acme/missing/-/whoami":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	domain, err := newPackageClientDomain(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	prober := NewHTTPProber(server.Client(), true)

	npm := prober.Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "npm"), Protocol: ProtocolNpm, Token: "pm_good"})
	if npm.Outcome != ProbeAccepted || npm.Username != "kko" || npm.StatusCode != 200 {
		t.Fatalf("npm probe = %#v", npm)
	}
	rejected := prober.Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "npm"), Protocol: ProtocolNpm, Token: "pm_bad"})
	if rejected.Outcome != ProbeTokenRejected || rejected.StatusCode != 401 {
		t.Fatalf("rejected probe = %#v", rejected)
	}
	pypi := prober.Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "pypi"), Protocol: ProtocolPyPI, Token: "pm_good"})
	if pypi.Outcome != ProbeAccepted {
		t.Fatalf("pypi probe = %#v", pypi)
	}
	missing := prober.Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "missing"), Protocol: ProtocolNpm, Token: "pm_good"})
	if missing.Outcome != ProbeFeedNotFound {
		t.Fatalf("missing probe = %#v", missing)
	}
	unavailable := prober.Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "broken"), Protocol: ProtocolNpm, Token: "pm_good"})
	if unavailable.Outcome != ProbeUnavailable || unavailable.StatusCode != 500 {
		t.Fatalf("unavailable probe = %#v", unavailable)
	}
	paths := make([]string, 0, len(seen))
	for _, request := range seen {
		paths = append(paths, request.URL.Path)
		if request.Method != http.MethodGet {
			t.Fatalf("non-GET request %s %s", request.Method, request.URL)
		}
		if !strings.HasPrefix(request.Header.Get("X-PackageMaze-Client-Version"), "maze/") {
			t.Fatalf("client version header missing on %s", request.URL)
		}
	}
	if strings.Join(paths, " ") != "/acme/npm/-/whoami /acme/npm/-/whoami /acme/pypi/simple/ /acme/missing/-/whoami /acme/broken/-/whoami" {
		t.Fatalf("requests = %v", paths)
	}
}

func TestHTTPProberNeverFollowsRedirects(t *testing.T) {
	var elsewhereHits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		elsewhereHits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, elsewhere.URL+"/acme/npm/-/whoami", http.StatusFound)
	}))
	defer server.Close()
	domain, err := newPackageClientDomain(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	result := NewHTTPProber(server.Client(), true).Probe(context.Background(), ProbeRequest{Feed: domain.feed("acme", "npm"), Protocol: ProtocolNpm, Token: "pm_secret"})
	if result.Outcome != ProbeUnexpected || result.StatusCode != http.StatusFound {
		t.Fatalf("redirect result = %#v", result)
	}
	if elsewhereHits.Load() != 0 {
		t.Fatalf("the redirect target was contacted %d times", elsewhereHits.Load())
	}
}

func TestHTTPProberRefusesPlainHTTPOutsideLocalhostAndRedactsErrors(t *testing.T) {
	prober := NewHTTPProber(&http.Client{Transport: failingTransport{}}, false)
	result := prober.Probe(context.Background(), ProbeRequest{Feed: Feed{Organization: "acme", Name: "npm", BaseURL: "http://pkg.example.test/acme/npm/"}, Protocol: ProtocolNpm, Token: "pm_x"})
	if result.Outcome != ProbeUnexpected || result.Err == nil {
		t.Fatalf("result = %#v", result)
	}
	unreachable := prober.Probe(context.Background(), ProbeRequest{Feed: Feed{Organization: "acme", Name: "npm", BaseURL: "https://pkg.example.test/acme/npm/"}, Protocol: ProtocolNpm, Token: "pm_secret"})
	if unreachable.Outcome != ProbeUnreachable || strings.Contains(unreachable.Err.Error(), "pm_secret") {
		t.Fatalf("unreachable = %#v", unreachable)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed for " + request.Header.Get("Authorization"))
}
