package doctor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/login"
)

func TestDoctorUsesTheTokenMazeAuthLoginSaved(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".npmrc":       "registry=" + fixtureFeedBaseURL + "\nreplace-registry-host=npmjs\n",
		"package.json": `{"name":"demo"}`,
	})
	configDir := t.TempDir()
	if err := login.SaveProfile(login.CredentialsPath(configDir), "acme", login.Profile{Organization: "acme", Token: "pm_profile_secret"}); err != nil {
		t.Fatal(err)
	}
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeAccepted, StatusCode: 200, Username: "kko"}}}
	report, _, err := Run(context.Background(), Config{Dir: dir}, Dependencies{
		ConfigDir: func() (string, error) { return configDir, nil },
		Env:       mapEnv(nil),
		Prober:    prober,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prober.requests) != 1 || prober.requests[0].Token != "pm_profile_secret" {
		t.Fatalf("requests = %#v", prober.requests)
	}
	present := requireCode(t, report, "token_env_present", StatusPass)
	if !strings.Contains(present.Summary, "profile acme") {
		t.Fatalf("summary = %q", present.Summary)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "pm_profile_secret") {
		t.Fatal("the report contains the Token")
	}
	if report.Token.Source != "profile acme" {
		t.Fatalf("token source = %q", report.Token.Source)
	}
}
