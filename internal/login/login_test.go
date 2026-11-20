package login

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const secret = "pm_test_secret_never_printed"

type fakeServer struct {
	collectAnswers []func(http.ResponseWriter)
	collects       atomic.Int32
	started        StartRequest
	verified       string
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/token-flows", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&f.started); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"flow_id":"tfl_1","collect_secret":"collect_1","approval_url":"https://www.packagemaze.test/settings/tokens/approve/tfl_1","expires_at":"2026-11-20T10:10:00Z","interval_seconds":2,"user_code":"BCDF-GHJK"}`))
	})
	mux.HandleFunc("POST /v1/auth/token-flows/tfl_1/collect", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["collect_secret"] != "collect_1" {
			t.Fatalf("collect without the collect secret: %v", body)
		}
		n := int(f.collects.Add(1)) - 1
		f.collectAnswers[min(n, len(f.collectAnswers)-1)](w)
	})
	mux.HandleFunc("POST /v1/tokens/verify", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token_secret"] != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.verified = body["feed_id"]
		_, _ = w.Write([]byte(`{"verified":true}`))
	})
	return mux
}

func pending(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"pending","expires_at":"2026-11-20T10:10:00Z"}`))
}

func collected(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"status":"collected","token_secret":"` + secret + `","token_id":"tok_1","token_name":"maze CLI on laptop","access":"read","feed_selection":{"kind":"selected","feed_ids":["feed_npm"]},"feeds":[{"id":"feed_npm","name":"npm"}],"expires_at":"2027-05-19T10:00:00Z","organization":{"id":"org_1","name":"Acme","slug":"acme"}}`))
}

func refusal(status int, code string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":{"code":"` + code + `","message":"The Token request ended.","recovery":"Run maze auth login again."}}`))
	}
}

func run(t *testing.T, fake *fakeServer, config Config) (Result, string, string, error) {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	var stderr bytes.Buffer
	var opened string
	config.APIURL = server.URL + "/v1"
	result, err := Run(context.Background(), config, Dependencies{
		ConfigDir:   func() (string, error) { return configDir, nil },
		HTTPClient:  server.Client(),
		Hostname:    func() (string, error) { return "laptop", nil },
		OpenBrowser: func(url string) error { opened = url; return nil },
		Sleep:       func(context.Context, time.Duration) error { return nil },
		Stderr:      &stderr,
	})
	if opened != "" && !strings.Contains(stderr.String(), opened) {
		t.Fatalf("the approval URL was opened but not printed: %q", stderr.String())
	}
	return result, configDir, stderr.String(), err
}

func TestLoginSavesTheApprovedTokenToAProfileAndVerifiesIt(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, pending, collected}}
	result, configDir, stderr, err := run(t, fake, Config{Organization: "acme", Feeds: []string{"acme/npm"}, PackageClientURL: "https://pkg.packagemaze.com"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.started.Access != "read" || fake.started.TokenName != "maze CLI on laptop" {
		t.Fatalf("unexpected start request: %+v", fake.started)
	}
	if feeds, ok := fake.started.Feeds.([]any); !ok || len(feeds) != 1 || feeds[0] != "npm" {
		t.Fatalf("feeds: %#v", fake.started.Feeds)
	}
	if !strings.Contains(stderr, "BCDF-GHJK") {
		t.Fatalf("the user code was not printed: %q", stderr)
	}
	if fake.collects.Load() != 3 || fake.verified != "feed_npm" {
		t.Fatalf("collects=%d verified=%q", fake.collects.Load(), fake.verified)
	}
	if result.Profile != "acme" || result.Organization.Name != "Acme" {
		t.Fatalf("result: %+v", result)
	}
	if strings.Contains(stderr, secret) {
		t.Fatal("the Token Secret reached stderr")
	}
	path := CredentialsPath(configDir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("credentials file mode %v is readable by others", info.Mode().Perm())
	}
	content, _ := os.ReadFile(path)
	var file credentialsFile
	if err := json.Unmarshal(content, &file); err != nil || file.Profiles["acme"].Token != secret {
		t.Fatalf("profile not saved: %s", content)
	}
	if saved := file.Profiles["acme"]; saved.PackageClientURL != "https://pkg.packagemaze.com" || saved.Organization != "acme" {
		t.Fatalf("profile = %+v", saved)
	}
}

func TestLoginKeepsOtherProfiles(t *testing.T) {
	dir := t.TempDir()
	path := CredentialsPath(dir)
	if err := SaveProfile(path, "work", Profile{Organization: "work", Token: "pm_other"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, "acme", Profile{Organization: "acme", Token: secret}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	var file credentialsFile
	_ = json.Unmarshal(content, &file)
	if file.Profiles["work"].Token != "pm_other" || file.Profiles["acme"].Token != secret {
		t.Fatalf("profiles: %s", content)
	}
}

func TestLoginStopsOnARefusedFlow(t *testing.T) {
	for _, code := range []string{"token_flow_denied", "token_flow_expired", "token_flow_collected"} {
		fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, refusal(http.StatusGone, code)}}
		_, configDir, _, err := run(t, fake, Config{Organization: "acme"})
		var refused *RefusedError
		if !errors.As(err, &refused) || refused.Code != code {
			t.Fatalf("%s: err = %v", code, err)
		}
		if _, statErr := os.Stat(CredentialsPath(configDir)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: a refused flow wrote a profile", code)
		}
	}
}

func TestLoginRequestsEveryFeedForPublishByDefault(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){collected}}
	if _, _, _, err := run(t, fake, Config{Organization: "acme", Purpose: "publish", Profile: "release"}); err != nil {
		t.Fatal(err)
	}
	if fake.started.Access != "read_publish" || fake.started.Feeds != "all" {
		t.Fatalf("start: %+v", fake.started)
	}
}

func TestLoginRefusesBadInputBeforeAnyRequest(t *testing.T) {
	for _, config := range []Config{
		{Organization: ""},
		{Organization: "acme", Purpose: "admin"},
		{Organization: "acme", Feeds: []string{"other/npm"}},
		{Organization: "acme", Profile: "../escape"},
	} {
		fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){collected}}
		if _, _, _, err := run(t, fake, config); err == nil || fake.started.Organization != "" {
			t.Fatalf("%+v: err=%v started=%+v", config, err, fake.started)
		}
	}
}
