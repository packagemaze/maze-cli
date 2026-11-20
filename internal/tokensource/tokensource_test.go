package tokensource

import (
	"errors"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/login"
)

func savedProfiles(t *testing.T, names ...string) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := login.SaveProfile(login.CredentialsPath(dir), name, login.Profile{Organization: name, Token: "pm_" + name}); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, error) { return dir, nil }
}

func env(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		lookup  Lookup
		profile []string
		want    Source
		wantErr string
	}{
		{name: "env wins over a profile", env: map[string]string{"MAZE_TOKEN": "pm_env"}, profile: []string{"acme"}, want: Source{Token: "pm_env", Origin: "env MAZE_TOKEN"}},
		{name: "the only profile", profile: []string{"acme"}, want: Source{Token: "pm_acme", Origin: "profile acme"}},
		{name: "the profile named after the Organization", lookup: Lookup{Organization: "beta"}, profile: []string{"acme", "beta"}, want: Source{Token: "pm_beta", Origin: "profile beta"}},
		{name: "--profile picks one", lookup: Lookup{Profile: "acme"}, profile: []string{"acme", "beta"}, want: Source{Token: "pm_acme", Origin: "profile acme"}},
		{name: "--profile that isn't saved", lookup: Lookup{Profile: "gamma"}, profile: []string{"acme"}, wantErr: "profile gamma is not in"},
		{name: "several profiles and nothing to choose by", profile: []string{"acme", "beta"}, wantErr: "several saved profiles (acme, beta); pass --profile"},
		{name: "no Token at all", wantErr: ErrNotFound.Error()},
		{name: "CI ignores a saved profile", env: map[string]string{"CI": "true"}, profile: []string{"acme"}, wantErr: ErrNotFound.Error()},
		{name: "CI on a detected platform ignores a saved profile", env: map[string]string{"GITHUB_ACTIONS": "true"}, profile: []string{"acme"}, wantErr: ErrNotFound.Error()},
		{name: "CI reads a profile it names", env: map[string]string{"CI": "true"}, lookup: Lookup{Profile: "acme"}, profile: []string{"acme"}, want: Source{Token: "pm_acme", Origin: "profile acme"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := test.lookup
			lookup.Env = env(test.env)
			lookup.TokenEnv = "MAZE_TOKEN"
			lookup.ConfigDir = savedProfiles(t, test.profile...)
			got, err := Resolve(lookup)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

func TestResolveWithoutAConfigDirectoryReadsNoProfile(t *testing.T) {
	if _, err := Resolve(Lookup{Env: env(nil), TokenEnv: "MAZE_TOKEN"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
