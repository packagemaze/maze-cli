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
	return login.Profile{PackageClientURL: "https://pkg.packagemaze.com", Organization: organization, Token: "pm_" + organization + "_secret", ExpiresAt: profileNow.Add(time.Hour).Format(time.RFC3339)}
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
	if err := Write(report, FormatJSON, &out, resolved.Tokens...); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Token struct {
			Organizations []struct {
				Organization string `json:"organization"`
				Source       struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"source"`
			} `json:"organizations"`
		} `json:"token"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	organizations := payload.Token.Organizations
	if len(organizations) != 1 || organizations[0].Organization != "acme" || organizations[0].Source.Kind != "profile" || organizations[0].Source.Name != "work" {
		t.Fatalf("token organizations = %+v", organizations)
	}
	out.Reset()
	if err := Write(report, FormatMarkdown, &out, resolved.Tokens...); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Organization acme uses the Token saved in profile work") {
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
	prober := acceptingProber()
	report, _ := runWithProfiles(t, Config{}, nil, func() (string, error) { return "", errors.New("$HOME is not defined") }, prober)
	failed := requireCode(t, report, "token_lookup_failed", StatusFail)
	if !strings.Contains(failed.Summary, "$HOME is not defined") || len(prober.requests) != 0 {
		t.Fatalf("check = %#v requests = %#v", failed, prober.requests)
	}
}

func TestDoctorWarnsAboutAnEmptyTokenVariableEvenOffline(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"acme": savedProfile("acme")})
	for _, offline := range []bool{false, true} {
		prober := acceptingProber()
		report, _ := runWithProfiles(t, Config{Offline: offline}, map[string]string{"MAZE_TOKEN": ""}, configDir, prober)
		empty := requireCode(t, report, "token_env_empty", StatusWarning)
		if !strings.Contains(empty.Summary, "MAZE_TOKEN is set but empty") || report.Failed() || len(prober.requests) != 0 {
			t.Fatalf("offline=%v check = %#v status = %q codes = %v", offline, empty, report.Status, codes(report))
		}
		forbidCode(t, report, "token_env_missing")
	}
}

func TestDoctorOfflineReportsAMissingExplicitProfile(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"acme": savedProfile("acme")})
	report, _ := runWithProfiles(t, Config{Offline: true, Profile: "gone"}, nil, configDir, &fakeProber{})
	failed := requireCode(t, report, "token_lookup_failed", StatusFail)
	if !strings.Contains(failed.Summary, "profile gone is not in") {
		t.Fatalf("summary = %q", failed.Summary)
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
	stale.PackageClientURL = "https://pkg.staging.packagemaze.com"
	configDir := profileConfigDir(t, map[string]login.Profile{"work": stale})
	prober := acceptingProber()
	report, _ := runWithProfiles(t, Config{}, nil, configDir, prober)
	expired := requireCode(t, report, "token_profile_expired", StatusWarning)
	if !strings.Contains(expired.Summary, "expired at 2026-11-20T11:00:00Z") || !strings.Contains(expired.Remediation, "maze auth login --org acme --profile work") {
		t.Fatalf("expired = %#v", expired)
	}
	other := requireCode(t, report, "token_profile_other_api", StatusWarning)
	if !strings.Contains(other.Summary, "https://pkg.staging.packagemaze.com") || !strings.Contains(other.Summary, "https://pkg.packagemaze.com") {
		t.Fatalf("other domain = %#v", other)
	}
	if len(prober.requests) != 1 {
		t.Fatalf("the live check still runs: %#v", prober.requests)
	}

	same := savedProfile("acme")
	same.PackageClientURL = "https://pkg.packagemaze.com/"
	report, _ = runWithProfiles(t, Config{}, nil, profileConfigDir(t, map[string]login.Profile{"work": same}), acceptingProber())
	forbidCode(t, report, "token_profile_other_api")
}

const twoOrganizationsPyproject = "[[tool.uv.index]]\nname = \"packagemaze\"\nurl = \"https://pkg.packagemaze.com/beta/pypi/simple/\"\ndefault = true\nauthenticate = \"always\"\n"

func twoOrganizationRepository(t *testing.T) string {
	return writeFixture(t, map[string]string{
		".npmrc":         "registry=" + fixtureFeedBaseURL + "\n",
		"package.json":   `{"name":"demo"}`,
		"pyproject.toml": twoOrganizationsPyproject,
	})
}

func TestDoctorSendsEachFeedOnlyItsOwnOrganizationsToken(t *testing.T) {
	configDir := profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme"), "home": savedProfile("acme"), "beta": savedProfile("beta")})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeAccepted, StatusCode: 200}, ProtocolPyPI: {Outcome: ProbeAccepted, StatusCode: 200}}}
	report, resolved := runWithProfiles(t, Config{Dir: twoOrganizationRepository(t), Profile: "work"}, nil, configDir, prober)
	sent := map[string]string{}
	for _, request := range prober.requests {
		sent[request.Feed.Slug()] = request.Token
	}
	if sent["acme/npm"] != "pm_acme_secret" || sent["beta/pypi"] != "pm_beta_secret" {
		t.Fatalf("tokens sent = %v", sent)
	}
	if len(resolved.Tokens) != 2 {
		t.Fatalf("tokens to redact = %d", len(resolved.Tokens))
	}
	var out bytes.Buffer
	if err := Write(report, FormatMarkdown, &out, resolved.Tokens...); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Organization acme uses the Token saved in profile work; Organization beta uses the Token saved in profile beta") {
		t.Fatalf("markdown = %s", out.String())
	}

	prober = &fakeProber{results: prober.results}
	report, _ = runWithProfiles(t, Config{Dir: twoOrganizationRepository(t)}, nil, profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme")}), prober)
	if len(prober.requests) != 1 || prober.requests[0].Feed.Slug() != "acme/npm" {
		t.Fatalf("requests = %#v", prober.requests)
	}
	missing := requireCode(t, report, "token_env_missing", StatusFail)
	if missing.Feed != "beta/pypi" {
		t.Fatalf("missing = %#v", missing)
	}
	requireNotChecked(t, report, "live_token_check", "no Token was chosen for Organization beta")
}

func TestDoctorTokenSentenceMatchesTheLookup(t *testing.T) {
	acme := profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme")})
	tests := []struct {
		name      string
		dir       func(t *testing.T) string
		env       map[string]string
		configDir func() (string, error)
		want      string
	}{
		{name: "the variable", env: map[string]string{"MAZE_TOKEN": "pm_env_secret"}, configDir: acme, want: "MAZE_TOKEN is set; its value is never printed."},
		{name: "a profile", configDir: acme, want: "MAZE_TOKEN is not set. Organization acme uses the Token saved in profile work; Token values are never printed."},
		{name: "a profile for one Organization only", dir: twoOrganizationRepository, configDir: acme, want: "MAZE_TOKEN is not set. Organization acme uses the Token saved in profile work; Organization beta has no Token; Token values are never printed."},
		{name: "an empty variable", env: map[string]string{"MAZE_TOKEN": " "}, configDir: acme, want: "MAZE_TOKEN is set but empty, so it holds no Token. No package-client credential variable is set."},
		{name: "several candidate profiles", configDir: profileConfigDir(t, map[string]login.Profile{"work": savedProfile("acme"), "home": savedProfile("acme")}), want: "MAZE_TOKEN is not set. No package-client credential variable is set."},
		{name: "nothing", want: "MAZE_TOKEN is not set. No package-client credential variable is set."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Config{}
			if test.dir != nil {
				config.Dir = test.dir(t)
			}
			report, resolved := runWithProfiles(t, config, test.env, test.configDir, &fakeProber{})
			var out bytes.Buffer
			if err := Write(report, FormatMarkdown, &out, resolved.Tokens...); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "## Token\n\n"+test.want+"\n") {
				t.Fatalf("markdown = %s", out.String())
			}
		})
	}
}
