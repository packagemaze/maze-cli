package tokensource

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/login"
)

const packageClientURL = "https://pkg.packagemaze.com"

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
	return login.Profile{PackageClientURL: packageClientURL, Organization: organization, Token: "pm_" + organization, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
}

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func profileSource(name string, organization string) Source {
	return Source{Origin: Origin{Kind: KindProfile, Name: name}, Token: "pm_" + organization, Organization: organization}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		org      string
		lookup   Lookup
		profiles map[string]login.Profile
		want     Source
		wantErr  string
	}{
		{name: "env wins over a profile", env: map[string]string{"MAZE_TOKEN": "pm_env"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, want: Source{Origin: Origin{Kind: KindEnv, Name: "MAZE_TOKEN"}, Token: "pm_env"}},
		{name: "a set but empty variable is an error", env: map[string]string{"MAZE_TOKEN": "  "}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: "MAZE_TOKEN is set but empty"},
		{name: "the profile saved for the Organization, whatever its name", org: "acme", profiles: map[string]login.Profile{"work": saved("acme"), "beta": saved("beta")}, want: profileSource("work", "acme")},
		{name: "the Organization matches case-insensitively", org: "ACME", profiles: map[string]login.Profile{"work": saved("acme")}, want: profileSource("work", "acme")},
		{name: "the only profile serves no other Organization", org: "beta", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "a profile named after the Organization but saved for another", org: "beta", profiles: map[string]login.Profile{"beta": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "no Organization reads no profile", org: "", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "several profiles for the Organization", org: "acme", profiles: map[string]login.Profile{"work": saved("acme"), "acme": saved("acme")}, wantErr: "several saved profiles (acme, work)"},
		{name: "--profile picks one", org: "acme", lookup: Lookup{Profile: "work"}, profiles: map[string]login.Profile{"work": saved("acme"), "acme": saved("acme")}, want: profileSource("work", "acme")},
		{name: "--profile for another Organization", org: "acme", lookup: Lookup{Profile: "beta"}, profiles: map[string]login.Profile{"beta": saved("beta")}, wantErr: "profile beta holds a Token for Organization beta, not acme"},
		{name: "--profile that isn't saved", org: "acme", lookup: Lookup{Profile: "gamma"}, profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: "profile gamma is not in"},
		{name: "no Token at all", org: "acme", wantErr: ErrNotFound.Error()},
		{name: "CI ignores a saved profile", env: map[string]string{"CI": "true"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI on a detected platform ignores a saved profile", env: map[string]string{"GITHUB_ACTIONS": "true"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI where platform detection fails ignores a saved profile", env: map[string]string{"GITHUB_ACTIONS": "true", "CIRCLECI": "true"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI on Jenkins ignores a saved profile", env: map[string]string{"JENKINS_URL": "https://ci.example.test/"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI on Buildkite ignores a saved profile", env: map[string]string{"BUILDKITE": "true"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI on GitLab ignores a saved profile", env: map[string]string{"GITLAB_CI": "true"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, wantErr: ErrNotFound.Error()},
		{name: "CI=False is not CI", env: map[string]string{"CI": "False"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, want: profileSource("acme", "acme")},
		{name: "CI=FALSE is not CI", env: map[string]string{"CI": "FALSE"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, want: profileSource("acme", "acme")},
		{name: "CI=0 is not CI", env: map[string]string{"CI": "0"}, org: "acme", profiles: map[string]login.Profile{"acme": saved("acme")}, want: profileSource("acme", "acme")},
		{name: "CI reads a profile it names", env: map[string]string{"CI": "true"}, org: "acme", lookup: Lookup{Profile: "acme"}, profiles: map[string]login.Profile{"acme": saved("acme")}, want: profileSource("acme", "acme")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := test.lookup
			lookup.Env = env(test.env)
			lookup.TokenEnv = "MAZE_TOKEN"
			lookup.ConfigDir = savedProfiles(t, test.profiles)
			lookup.PackageClientURL = packageClientURL
			lookup.Now = func() time.Time { return now }
			got, err := Resolve(lookup, test.org)
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
	expired.PackageClientURL = "https://pkg.staging.packagemaze.com/"
	source, err := Resolve(Lookup{
		Env:              env(nil),
		TokenEnv:         "MAZE_TOKEN",
		ConfigDir:        savedProfiles(t, map[string]login.Profile{"work": expired}),
		PackageClientURL: packageClientURL,
		Now:              func() time.Time { return now },
	}, "acme")
	if err != nil || len(source.Problems) != 2 {
		t.Fatalf("source = %+v, err = %v", source, err)
	}
	if source.Problems[0].Reason != ProblemExpired || !strings.Contains(source.Problems[0].Error(), "maze auth login --org acme --profile work") {
		t.Fatalf("expired = %v", source.Problems[0])
	}
	if source.Problems[1].Reason != ProblemOtherDomain || !strings.Contains(source.Problems[1].Error(), "--package-client-url https://pkg.staging.packagemaze.com") {
		t.Fatalf("other API = %v", source.Problems[1])
	}
}

func TestResolveSurfacesAConfigDirectoryError(t *testing.T) {
	_, err := Resolve(Lookup{
		Env:       env(nil),
		TokenEnv:  "MAZE_TOKEN",
		ConfigDir: func() (string, error) { return "", errors.New("$HOME is not defined") },
	}, "acme")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "$HOME is not defined") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveWithoutAConfigDirectoryReadsNoProfile(t *testing.T) {
	if _, err := Resolve(Lookup{Env: env(nil), TokenEnv: "MAZE_TOKEN"}, "acme"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveEachGivesEveryOrganizationItsOwnProfile(t *testing.T) {
	profiles := map[string]login.Profile{"work": saved("acme"), "home": saved("acme"), "beta": saved("beta")}
	results := ResolveEach(Lookup{
		Env:       env(nil),
		TokenEnv:  "MAZE_TOKEN",
		Profile:   "work",
		ConfigDir: savedProfiles(t, profiles),
	}, []string{"acme", "beta", "gamma"})
	if results[0].Err != nil || results[0].Source.Name != "work" || results[0].Source.Token != "pm_acme" {
		t.Fatalf("acme = %+v", results[0])
	}
	if results[1].Err != nil || results[1].Source.Name != "beta" || results[1].Source.Token != "pm_beta" {
		t.Fatalf("--profile work must not serve beta's Feeds: %+v", results[1])
	}
	if !errors.Is(results[2].Err, ErrNotFound) || results[2].Source.Token != "" {
		t.Fatalf("gamma = %+v", results[2])
	}

	inCI := ResolveEach(Lookup{
		Env:       env(map[string]string{"CI": "true"}),
		TokenEnv:  "MAZE_TOKEN",
		Profile:   "work",
		ConfigDir: savedProfiles(t, profiles),
	}, []string{"acme", "beta"})
	if inCI[0].Source.Name != "work" || !errors.Is(inCI[1].Err, ErrNotFound) {
		t.Fatalf("CI reads only the profile it names: %+v", inCI)
	}
}
