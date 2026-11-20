package publish

import (
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/login"
)

func TestPublishReadsTheTokenMazeAuthLoginSaved(t *testing.T) {
	configDir := t.TempDir()
	for _, name := range []string{"acme", "beta"} {
		if err := login.SaveProfile(login.CredentialsPath(configDir), name, login.Profile{Organization: name, Token: "pm_" + name}); err != nil {
			t.Fatal(err)
		}
	}
	deps := Dependencies{ConfigDir: func() (string, error) { return configDir, nil }, Env: func(string) (string, bool) { return "", false }}

	token, err := acquireToken(Config{Feed: "beta/npm", TokenEnv: DefaultTokenEnv}, deps)
	if err != nil || token != "pm_beta" {
		t.Fatalf("token = %q, err = %v; want the profile named after the Feed's Organization", token, err)
	}

	deps.Env = func(name string) (string, bool) { return "pm_env", name == DefaultTokenEnv }
	if token, err := acquireToken(Config{Feed: "beta/npm", TokenEnv: DefaultTokenEnv}, deps); err != nil || token != "pm_env" {
		t.Fatalf("token = %q, err = %v; MAZE_TOKEN must win over a profile", token, err)
	}

	deps.Env = func(name string) (string, bool) { return "true", name == "CI" }
	_, err = acquireToken(Config{Feed: "beta/npm", TokenEnv: DefaultTokenEnv}, deps)
	if err == nil || !strings.Contains(err.Error(), "run maze auth login") {
		t.Fatalf("err = %v; CI must not read a saved profile it doesn't name", err)
	}
}
