package tokensource

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/login"
)

const apiURL = "https://api.packagemaze.com/v1"

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func savedProfiles(t *testing.T, profiles map[string]login.Profile) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	for name, profile := range profiles {
		if err := login.SaveProfile(login.CredentialsPath(dir), name, profile); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, error) { return dir, nil }
}

func saved(organization string) login.Profile {
	return login.Profile{APIURL: apiURL, Organization: organization, Token: "pm_" + organization, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
}

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func fromProfile(name string, organization string) Source {
	return Source{Origin: Origin{Kind: KindProfile, Name: name}, Token: "pm_" + organization, Organization: organization}
}

func TestResolve(t *testing.T) {
	acme := []string{"acme"}
	tests := []struct {
		name     string
		env      map[string]string
		lookup   Lookup
		profiles map[string]login.Profile
		want     Source
		wantErr  string
	}{
		{name: "env wins over a profile", env: map[string]string{"MAZE_TOKEN": "pm_env"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: Source{Origin: Origin{Kind: KindEnv, Name: "MAZE_TOKEN"}, Token: "pm_env"}},
		{name: "a set but empty variable is an error", env: map[string]string{"MAZE_TOKEN": "  "}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: "MAZE_TOKEN is set but empty"},
		{name: "the profile saved for the Organization, whatever its name", lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"work": saved("acme"), "beta": saved("beta")}, want: fromProfile("work", "acme")},
		{name: "the Organization matches case-insensitively", lookup: Lookup{Organizations: []string{"ACME"}}, profiles: map[string]login.Profile{"work": saved("acme")}, want: fromProfile("work", "acme")},
		{name: "the only profile serves no other Organization", lookup: Lookup{Organizations: []string{"beta"}}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "a profile named after the Organization but saved for another", lookup: Lookup{Organizations: []string{"beta"}}, profiles: map[string]login.Profile{"beta": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "no Organization reads no profile", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "several profiles for the Organization", lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"work": saved("acme"), "acme": saved("acme")}, wantErr: "several saved profiles (acme, work)"},
		{name: "--profile picks one", lookup: Lookup{Profile: "work", Organizations: acme}, profiles: map[string]login.Profile{"work": saved("acme"), "acme": saved("acme")}, want: fromProfile("work", "acme")},
		{name: "--profile for another Organization", lookup: Lookup{Profile: "beta", Organizations: acme}, profiles: map[string]login.Profile{"beta": saved("beta")}, wantErr: "profile beta holds a Token for Organization beta, not acme"},
		{name: "--profile that isn't saved", lookup: Lookup{Profile: "gamma", Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: "profile gamma is not in"},
		{name: "no Token at all", lookup: Lookup{Organizations: acme}, wantErr: ErrNotFound.Error()},
		{name: "CI ignores a saved profile", env: map[string]string{"CI": "true"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI on a detected platform ignores a saved profile", env: map[string]string{"GITHUB_ACTIONS": "true"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI where platform detection fails ignores a saved profile", env: map[string]string{"GITHUB_ACTIONS": "true", "CIRCLECI": "true"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI=False is not CI", env: map[string]string{"CI": "False"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: fromProfile("acme", "acme")},
		{name: "CI=FALSE is not CI", env: map[string]string{"CI": "FALSE"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: fromProfile("acme", "acme")},
		{name: "CI=0 is not CI", env: map[string]string{"CI": "0"}, lookup: Lookup{Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: fromProfile("acme", "acme")},
		{name: "CI reads a profile it names", env: map[string]string{"CI": "true"}, lookup: Lookup{Profile: "acme", Organizations: acme}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: fromProfile("acme", "acme")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := test.lookup
			lookup.Env = env(test.env)
			lookup.TokenEnv = "MAZE_TOKEN"
			lookup.ConfigDir = savedProfiles(t, test.profiles)
			lookup.APIURL = apiURL
			lookup.Now = func() time.Time { return now }
			got, err := Resolve(lookup)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got.Origin != test.want.Origin || got.Token != test.want.Token || got.Organization != test.want.Organization || len(got.Problems) != 0 {
				t.Fatalf("got %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestResolveReportsAProfileThatMayNotWork(t *testing.T) {
	expired := saved("acme")
	expired.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339)
	expired.APIURL = "https://api.staging.packagemaze.com/v1/"
	source, err := Resolve(Lookup{
		Env:           env(nil),
		TokenEnv:      "MAZE_TOKEN",
		Organizations: []string{"acme"},
		ConfigDir:     savedProfiles(t, map[string]login.Profile{"work": expired}),
		APIURL:        apiURL,
		Now:           func() time.Time { return now },
	})
	if err != nil || len(source.Problems) != 2 {
		t.Fatalf("source = %+v, err = %v", source, err)
	}
	if source.Problems[0].Reason != ProblemExpired || !strings.Contains(source.Problems[0].Error(), "maze auth login --org acme --profile work") {
		t.Fatalf("expired = %v", source.Problems[0])
	}
	if source.Problems[1].Reason != ProblemOtherAPI || !strings.Contains(source.Problems[1].Error(), "https://api.staging.packagemaze.com/v1") {
		t.Fatalf("other API = %v", source.Problems[1])
	}
}

func TestResolveSurfacesAConfigDirectoryError(t *testing.T) {
	_, err := Resolve(Lookup{
		Env:           env(nil),
		TokenEnv:      "MAZE_TOKEN",
		Organizations: []string{"acme"},
		ConfigDir:     func() (string, error) { return "", errors.New("$HOME is not defined") },
	})
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "$HOME is not defined") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveWithoutAConfigDirectoryReadsNoProfile(t *testing.T) {
	if _, err := Resolve(Lookup{Env: env(nil), TokenEnv: "MAZE_TOKEN", Organizations: []string{"acme"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
