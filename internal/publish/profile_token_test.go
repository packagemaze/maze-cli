package publish

import (
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/login"
)

var profileNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func profileDeps(t *testing.T, env map[string]string, profiles map[string]login.Profile) Dependencies {
	t.Helper()
	configDir := t.TempDir()
	for name, profile := range profiles {
		if err := login.SaveProfile(login.CredentialsPath(configDir), name, profile); err != nil {
			t.Fatal(err)
		}
	}
	return Dependencies{
		ConfigDir: func() (string, error) { return configDir, nil },
		Env: func(name string) (string, bool) {
			value, ok := env[name]
			return value, ok
		},
		Now: func() time.Time { return profileNow },
	}
}

func publishProfile(organization string) login.Profile {
	return login.Profile{PackageClientURL: DefaultPackageClientURL, Organization: organization, Token: "pm_" + organization, ExpiresAt: profileNow.Add(time.Hour).Format(time.RFC3339)}
}

func TestPublishReadsTheTokenMazeAuthLoginSaved(t *testing.T) {
	expired := publishProfile("beta")
	expired.ExpiresAt = profileNow.Add(-time.Second).Format(time.RFC3339)
	staging := publishProfile("beta")
	staging.PackageClientURL = "https://pkg.staging.packagemaze.com"
	tests := []struct {
		name      string
		env       map[string]string
		feed      string
		profile   string
		profiles  map[string]login.Profile
		wantToken string
		wantErr   string
	}{
		{name: "the profile saved for the Feed's Organization", profiles: map[string]login.Profile{"acme": publishProfile("acme"), "work": publishProfile("beta")}, wantToken: "pm_beta"},
		{name: "MAZE_TOKEN wins over a profile", env: map[string]string{DefaultTokenEnv: "pm_env"}, profiles: map[string]login.Profile{"work": publishProfile("beta")}, wantToken: "pm_env"},
		{name: "an empty MAZE_TOKEN is an error", env: map[string]string{DefaultTokenEnv: ""}, profiles: map[string]login.Profile{"work": publishProfile("beta")}, wantErr: "MAZE_TOKEN is set but empty"},
		{name: "CI does not read a profile it doesn't name", env: map[string]string{"CI": "true"}, profiles: map[string]login.Profile{"work": publishProfile("beta")}, wantErr: "run maze auth login --org beta"},
		{name: "the only profile, for another Organization", profiles: map[string]login.Profile{"beta": publishProfile("acme")}, wantErr: "PackageMaze Token was not found"},
		{name: "--profile for another Organization", profile: "acme", profiles: map[string]login.Profile{"acme": publishProfile("acme")}, wantErr: "profile acme holds a Token for Organization acme, not beta"},
		{name: "an expired profile", profiles: map[string]login.Profile{"work": expired}, wantErr: "maze publish will not use the saved profile: the Token saved in profile work expired"},
		{name: "a profile for another Package Client Domain", profiles: map[string]login.Profile{"work": staging}, wantErr: "profile work was saved for the Package Client Domain https://pkg.staging.packagemaze.com"},
		{name: "a padded --feed still names its Organization", feed: "  beta/npm ", profiles: map[string]login.Profile{"work": publishProfile("beta")}, wantToken: "pm_beta"},
		{name: "a padded --feed names its Organization in the error", feed: "  beta/npm ", wantErr: "run maze auth login --org beta, use"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := profileDeps(t, test.env, test.profiles)
			feed := test.feed
			if feed == "" {
				feed = "beta/npm"
			}
			token, err := acquireToken(Config{Feed: feed, PackageClientURL: DefaultPackageClientURL, Profile: test.profile, TokenEnv: DefaultTokenEnv}, deps)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || token != test.wantToken {
				t.Fatalf("token = %q, err = %v", token, err)
			}
		})
	}
}
