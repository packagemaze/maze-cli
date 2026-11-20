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
	return refusalAfter(status, code, "")
}

func refusalAfter(status int, code string, retryAfter string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":{"code":"` + code + `","message":"The Token request ended.","recovery":"Run maze auth login again."}}`))
	}
}

func run(t *testing.T, fake *fakeServer, config Config) (Result, string, string, error) {
	t.Helper()
	return runWith(t, context.Background(), fake, config, func(context.Context, time.Duration) error { return nil })
}

func runWith(t *testing.T, ctx context.Context, fake *fakeServer, config Config, sleep func(context.Context, time.Duration) error) (Result, string, string, error) {
	t.Helper()
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	configDir := t.TempDir()
	var stderr bytes.Buffer
	var opened string
	config.APIURL = server.URL + "/v1"
	result, err := Run(ctx, config, Dependencies{
		ConfigDir:   func() (string, error) { return configDir, nil },
		HTTPClient:  server.Client(),
		Hostname:    func() (string, error) { return "laptop", nil },
		OpenBrowser: func(url string) error { opened = url; return nil },
		Sleep:       sleep,
		Stderr:      &stderr,
	})
	if strings.Contains(stderr.String(), secret) {
		t.Fatal("the Token Secret reached stderr")
	}
	if opened != "" && !strings.Contains(stderr.String(), opened) {
		t.Fatalf("the approval URL was opened but not printed: %q", stderr.String())
	}
	return result, configDir, stderr.String(), err
}

func TestLoginSavesTheApprovedTokenToAProfileAndVerifiesIt(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, pending, collected}}
	result, configDir, stderr, err := run(t, fake, Config{Organization: "acme", Feeds: []string{"acme/npm"}})
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

func dropConnection(w http.ResponseWriter) {
	connection, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(err)
	}
	_ = connection.Close()
}

func truncatedCollected(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "4096")
	_, _ = w.Write([]byte(`{"status":"collected","token_secret":"` + secret))
}

func repeat(answer func(http.ResponseWriter), n int) []func(http.ResponseWriter) {
	answers := make([]func(http.ResponseWriter), n)
	for i := range answers {
		answers[i] = answer
	}
	return answers
}

func TestLoginPollsThroughARetryableCollect(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, refusal(http.StatusServiceUnavailable, "token_flow_retry"), refusal(http.StatusTooManyRequests, "rate_limited"), refusal(http.StatusBadGateway, ""), refusal(http.StatusGatewayTimeout, ""), collected}}
	result, _, stderr, err := run(t, fake, Config{Organization: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.collects.Load() != 6 || result.Profile != "acme" {
		t.Fatalf("collects=%d result=%+v", fake.collects.Load(), result)
	}
	if strings.Count(stderr, "PackageMaze asked maze to try again; retrying...") != 4 {
		t.Fatalf("stderr: %q", stderr)
	}
}

func TestLoginDecidesRetriesOnTheCodeBeforeTheStatus(t *testing.T) {
	for _, answer := range []struct {
		status int
		code   string
		retry  bool
	}{
		{http.StatusConflict, "token_flow_retry", true},
		{http.StatusServiceUnavailable, "", false},
		{http.StatusServiceUnavailable, "maintenance", false},
		{http.StatusTooManyRequests, "token_flow_denied", false},
		{http.StatusInternalServerError, "", false},
	} {
		fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){refusal(answer.status, answer.code), collected}}
		_, _, _, err := run(t, fake, Config{Organization: "acme"})
		if retried := fake.collects.Load() == 2; retried != answer.retry || (err == nil) != answer.retry {
			t.Fatalf("%d %q: collects=%d err=%v", answer.status, answer.code, fake.collects.Load(), err)
		}
	}
}

func TestLoginStopsWhenTheCollectAnswerIsLost(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"dropped connection": dropConnection,
		"unreadable 200":     truncatedCollected,
		"200 without JSON":   func(w http.ResponseWriter) { _, _ = w.Write([]byte("<html>")) },
	} {
		fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, answer, collected}}
		_, configDir, stderr, err := run(t, fake, Config{Organization: "acme"})
		var lost *AnswerLostError
		if !errors.As(err, &lost) || fake.collects.Load() != 2 {
			t.Fatalf("%s: collects=%d err=%v", name, fake.collects.Load(), err)
		}
		for _, text := range []string{stderr, err.Error()} {
			if !strings.Contains(text, "Check the Tokens page in PackageMaze") || !strings.Contains(text, "run maze auth login again") {
				t.Fatalf("%s: no advice in %q", name, text)
			}
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s: the Token Secret reached the error", name)
		}
		if _, statErr := os.Stat(CredentialsPath(configDir)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: a lost answer wrote a profile", name)
		}
	}
}

func TestLoginPointsAtTheTokensPageWhenTheRequestWasAlreadyCollected(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){refusal(http.StatusGatewayTimeout, ""), refusal(http.StatusGone, "token_flow_collected")}}
	_, _, stderr, err := run(t, fake, Config{Organization: "acme"})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Code != "token_flow_collected" || !strings.Contains(stderr, "Check the Tokens page in PackageMaze") {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

func TestLoginStopsOnANetworkErrorThatNeverReachedPackageMaze(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){collected}}
	server := httptest.NewServer(fake.handler(t))
	url := server.URL
	server.Close()
	client := Client{APIURL: url + "/v1", HTTPClient: &http.Client{}}
	var stderr bytes.Buffer
	_, err := waitForApproval(context.Background(), client, Started{FlowID: "tfl_1", CollectSecret: "collect_1"}, Dependencies{Stderr: &stderr, Sleep: func(context.Context, time.Duration) error { t.Fatal("retried"); return nil }})
	var lost *AnswerLostError
	if err == nil || errors.As(err, &lost) || stderr.Len() != 0 {
		t.Fatalf("err=%v stderr=%q", err, stderr.String())
	}
}

func TestLoginWaitsAsLongAsRetryAfterAsksWithinACap(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){
		pending,
		refusalAfter(http.StatusServiceUnavailable, "token_flow_retry", "7"),
		refusalAfter(http.StatusTooManyRequests, "rate_limited", "3600"),
		refusalAfter(http.StatusTooManyRequests, "rate_limited", time.Now().Add(20*time.Second).UTC().Format(http.TimeFormat)),
		refusalAfter(http.StatusServiceUnavailable, "token_flow_retry", "1"),
		refusal(http.StatusBadGateway, ""),
		collected,
	}}
	var waits []time.Duration
	_, _, _, err := runWith(t, context.Background(), fake, Config{Organization: "acme"}, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 6 || waits[0] != 2*time.Second || waits[1] != 7*time.Second || waits[2] != maxCollectRetryAfter ||
		waits[3] < 18*time.Second || waits[3] > 20*time.Second || waits[4] != 2*time.Second || waits[5] != 2*time.Second {
		t.Fatalf("waits = %v", waits)
	}
}

func TestLoginStopsWhenCancelledWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){pending, collected}}
	_, _, _, err := runWith(t, ctx, fake, Config{Organization: "acme"}, func(ctx context.Context, d time.Duration) error {
		cancel()
		return SleepContext(ctx, time.Hour)
	})
	if !errors.Is(err, context.Canceled) || fake.collects.Load() != 1 {
		t.Fatalf("collects=%d err=%v", fake.collects.Load(), err)
	}
}

func TestLoginResetsTheRetryCountAfterAPendingAnswer(t *testing.T) {
	retry := refusal(http.StatusServiceUnavailable, "token_flow_retry")
	answers := append(repeat(retry, maxCollectRetries), pending)
	answers = append(answers, repeat(retry, maxCollectRetries)...)
	fake := &fakeServer{collectAnswers: append(answers, collected)}
	if _, _, _, err := run(t, fake, Config{Organization: "acme"}); err != nil {
		t.Fatal(err)
	}
	if got := fake.collects.Load(); got != int32(2*maxCollectRetries+2) {
		t.Fatalf("collects = %d", got)
	}
}

func TestLoginGivesUpAfterMaxCollectRetries(t *testing.T) {
	fake := &fakeServer{collectAnswers: []func(http.ResponseWriter){refusal(http.StatusServiceUnavailable, "token_flow_retry")}}
	_, _, stderr, err := run(t, fake, Config{Organization: "acme"})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Code != "token_flow_retry" {
		t.Fatalf("err = %v", err)
	}
	if got := fake.collects.Load(); got != maxCollectRetries+1 {
		t.Fatalf("collects = %d", got)
	}
	if got := strings.Count(stderr, "retrying"); got != maxCollectRetries {
		t.Fatalf("retries printed = %d", got)
	}
}
