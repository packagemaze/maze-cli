package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/login"
)

var profileNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func savedProfile(organization string) login.Profile {
	return login.Profile{APIURL: "https://api.packagemaze.com/v1", Organization: organization, Token: "pm_" + organization + "_secret", ExpiresAt: profileNow.Add(time.Hour).Format(time.RFC3339)}
}

func profileConfigDir(t *testing.T, profiles map[string]login.Profile) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	for name, profile := range profiles {
		if err := login.SaveProfile(login.CredentialsPath(dir), name, profile); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, error) { return dir, nil }
}

func runWithProfiles(t *testing.T, config Config, env map[string]string, configDir func() (string, error), prober *fakeProber) (Report, ResolvedConfig) {
	t.Helper()
	if config.Dir == "" {
		config.Dir = writeFixture(t, map[string]string{".npmrc": "registry=" + fixtureFeedBaseURL + "\n", "package.json": `{"name":"demo"}`})
	}
	report, resolved, err := Run(context.Background(), config, Dependencies{
		ConfigDir: configDir,
		Env:       mapEnv(env),
		Now:       func() time.Time { return profileNow },
		Prober:    prober,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return report, resolved
}

func acceptingProber() *fakeProber {
	return &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeAccepted, StatusCode: 200, Username: "kko"}}}
}

func TestDoctorUsesTheProfileSavedForTheDetectedFeedsOrganization(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme"), "beta": savedProfile("beta")})
	prober := acceptingProber()
	report, resolved := runWithProfiles(t, Config{}, nil, configDir, prober)
	if len(prober.requests) != 1 || prober.requests[0].Token != "pm_acme_secret" {
		t.Fatalf("requests = %#v", prober.requests)
	}
	present := requireCode(t, report, "token_profile_present", StatusPass)
	if !strings.Contains(present.Summary, "profile work") || present.Source != "profile:work" {
		t.Fatalf("check = %#v", present)
	}
	ready := requireCode(t, report, "read_token_ready", StatusPass)
	if !strings.Contains(ready.Summary, "the Token saved in profile work") {
		t.Fatalf("ready = %q", ready.Summary)
	}
	var out bytes.Buffer
	if err := Write(report, FormatJSON, &out, resolved.Token); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Token struct {
			Source struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"source"`
		} `json:"token"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Token.Source.Kind != "profile" || payload.Token.Source.Name != "work" {
		t.Fatalf("token source = %+v", payload.Token.Source)
	}
	out.Reset()
	if err := Write(report, FormatMarkdown, &out, resolved.Token); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "the Token saved in profile work is used") {
		t.Fatalf("markdown = %s", out.String())
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded)+out.String(), "pm_acme_secret") {
		t.Fatal("the report contains the Token")
	}
}

func TestDoctorTellsAProfileUserToSignInAgainWhenTheTokenIsRejected(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme")})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeTokenRejected, StatusCode: 401}}}
	report, _ := runWithProfiles(t, Config{}, nil, configDir, prober)
	rejected := requireCode(t, report, "read_token_rejected", StatusFail)
	if !strings.Contains(rejected.Summary, "the Token saved in profile work") || !strings.Contains(rejected.Remediation, "maze auth login --org acme --profile work") || strings.Contains(rejected.Remediation, "MAZE_TOKEN") {
		t.Fatalf("check = %#v", rejected)
	}
}

func TestDoctorDoesNotUseAProfileForAnotherOrganization(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"acme": savedProfile("beta")})
	prober := acceptingProber()
	report, _ := runWithProfiles(t, Config{}, nil, configDir, prober)
	requireCode(t, report, "token_env_missing", StatusFail)
	if len(prober.requests) != 0 {
		t.Fatalf("requests = %#v", prober.requests)
	}

	report, _ = runWithProfiles(t, Config{Profile: "acme"}, nil, configDir, prober)
	failed := requireCode(t, report, "token_lookup_failed", StatusFail)
	if !strings.Contains(failed.Summary, "profile acme holds a Token for Organization beta, not acme") {
		t.Fatalf("summary = %q", failed.Summary)
	}
}

func TestDoctorReportsSeveralCandidateProfilesWithoutFailing(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme"), "home": savedProfile("acme")})
	prober := acceptingProber()
	report, _ := runWithProfiles(t, Config{}, nil, configDir, prober)
	ambiguous := requireCode(t, report, "token_profile_ambiguous", StatusWarning)
	if !strings.Contains(ambiguous.Summary, "home, work") || !strings.Contains(ambiguous.Remediation, "--profile home") {
		t.Fatalf("check = %#v", ambiguous)
	}
	forbidCode(t, report, "token_env_missing")
	requireNotChecked(t, report, "live_token_check", "No Token could be chosen")
	if len(prober.requests) != 0 || report.Failed() {
		t.Fatalf("status = %q requests = %#v", report.Status, prober.requests)
	}
}

func TestDoctorReportsTokenLookupErrorsAsChecks(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		configDir func() (string, error)
		want      string
	}{
		{name: "an empty Token variable", env: map[string]string{"MAZE_TOKEN": ""}, configDir: profileConfigDir(t, map[string]login.Profile{"acme": savedProfile("acme")}), want: "MAZE_TOKEN is set but empty"},
		{name: "no user config directory", configDir: func() (string, error) { return "", errors.New("$HOME is not defined") }, want: "$HOME is not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prober := acceptingProber()
			report, _ := runWithProfiles(t, Config{}, test.env, test.configDir, prober)
			failed := requireCode(t, report, "token_lookup_failed", StatusFail)
			if !strings.Contains(failed.Summary, test.want) || len(prober.requests) != 0 {
				t.Fatalf("check = %#v requests = %#v", failed, prober.requests)
			}
		})
	}
}

func TestDoctorOfflineReadsNoProfile(t *testing.T) {
	configDir := func() (string, error) {
		t.Fatal("--offline read the saved profiles")
		return "", nil
	}
	report, _ := runWithProfiles(t, Config{Offline: true}, nil, configDir, &fakeProber{})
	requireCode(t, report, "token_env_missing", StatusFail)
	requireNotChecked(t, report, "live_token_check", "--offline")
}

func TestDoctorWarnsAboutAnExpiredProfileOrOneForAnotherAPI(t *testing.T) {
	stale := savedProfile("acme")
	stale.ExpiresAt = profileNow.Add(-time.Hour).Format(time.RFC3339)
	stale.APIURL = "https://api.staging.packagemaze.com/v1"
	configDir := profileConfigDir(t, map[string]login.Profile{"work": stale})
	prober := acceptingProber()
	report, _ := runWithProfiles(t, Config{}, nil, configDir, prober)
	expired := requireCode(t, report, "token_profile_expired", StatusWarning)
	if !strings.Contains(expired.Summary, "expired at 2026-11-20T11:00:00Z") || !strings.Contains(expired.Remediation, "maze auth login --org acme --profile work") {
		t.Fatalf("expired = %#v", expired)
	}
	other := requireCode(t, report, "token_profile_other_api", StatusWarning)
	if !strings.Contains(other.Summary, "https://api.staging.packagemaze.com/v1") || !strings.Contains(other.Summary, "https://api.packagemaze.com/v1") {
		t.Fatalf("other API = %#v", other)
	}
	if len(prober.requests) != 1 {
		t.Fatalf("the live check still runs: %#v", prober.requests)
	}

	report, _ = runWithProfiles(t, Config{}, map[string]string{"MAZE_BASE_URL": "https://api.staging.packagemaze.com"}, configDir, acceptingProber())
	forbidCode(t, report, "token_profile_other_api")
}
