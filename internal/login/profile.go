package login

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Profile is one saved sign-in. The file holding it is readable by its owner only.
type Profile struct {
	APIURL       string `json:"api_url"`
	Organization string `json:"organization"`
	Token        string `json:"token"`
	TokenID      string `json:"token_id"`
	ExpiresAt    string `json:"expires_at"`
}

type credentialsFile struct {
	Profiles map[string]Profile `json:"profiles"`
}

// CredentialsPath is where maze keeps its profiles: the user's config directory, never the working tree.
func CredentialsPath(configDir string) string {
	return filepath.Join(configDir, "packagemaze", "credentials.json")
}

func ValidProfileName(name string) bool {
	return profileNamePattern.MatchString(name)
}

// SaveProfile replaces one profile and keeps the others, writing the file atomically with mode 0600.
func SaveProfile(path string, name string, profile Profile) error {
	file := credentialsFile{Profiles: map[string]Profile{}}
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(content, &file); err != nil {
			return fmt.Errorf("%s is not a maze credentials file: %w", path, err)
		}
		if file.Profiles == nil {
			file.Profiles = map[string]Profile{}
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	file.Profiles[name] = profile
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
