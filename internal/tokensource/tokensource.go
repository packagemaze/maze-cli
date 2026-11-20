// Package tokensource finds the PackageMaze Token a command uses: the
// environment first, then a profile `maze auth login` saved. Inside CI a saved
// profile is used only when the job names one, so a job never picks up a
// developer's Token by accident.
package tokensource

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/login"
)

// ErrNotFound means no source holds a Token.
var ErrNotFound = errors.New("no PackageMaze Token found")

type Lookup struct {
	Env ci.LookupEnv
	// TokenEnv is the environment variable to read first.
	TokenEnv string
	// Profile is --profile; empty picks the profile named after Organization, else the only one.
	Profile      string
	Organization string
	ConfigDir    func() (string, error)
}

// Source is a Token and where it came from, for messages; Origin never contains the Token.
type Source struct {
	Token  string
	Origin string
}

func Resolve(lookup Lookup) (Source, error) {
	env := lookup.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	if value, ok := env(lookup.TokenEnv); ok && strings.TrimSpace(value) != "" {
		return Source{Token: strings.TrimSpace(value), Origin: "env " + lookup.TokenEnv}, nil
	}
	profile := strings.TrimSpace(lookup.Profile)
	if profile == "" && inCI(env) {
		return Source{}, ErrNotFound
	}
	if lookup.ConfigDir == nil {
		return Source{}, ErrNotFound
	}
	dir, err := lookup.ConfigDir()
	if err != nil {
		return Source{}, ErrNotFound
	}
	path := login.CredentialsPath(dir)
	profiles, err := login.LoadProfiles(path)
	if err != nil {
		return Source{}, err
	}
	name, err := chooseProfile(profiles, profile, strings.TrimSpace(lookup.Organization), path)
	if err != nil {
		return Source{}, err
	}
	token := strings.TrimSpace(profiles[name].Token)
	if token == "" {
		return Source{}, fmt.Errorf("profile %s in %s holds no Token; run maze auth login again", name, path)
	}
	return Source{Token: token, Origin: "profile " + name}, nil
}

func chooseProfile(profiles map[string]login.Profile, requested string, organization string, path string) (string, error) {
	if requested != "" {
		if _, ok := profiles[requested]; !ok {
			return "", fmt.Errorf("profile %s is not in %s; run maze auth login --profile %s", requested, path, requested)
		}
		return requested, nil
	}
	if _, ok := profiles[organization]; organization != "" && ok {
		return organization, nil
	}
	if len(profiles) == 1 {
		for name := range profiles {
			return name, nil
		}
	}
	if len(profiles) > 1 && organization == "" {
		names := make([]string, 0, len(profiles))
		for name := range profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("several saved profiles (%s); pass --profile", strings.Join(names, ", "))
	}
	return "", ErrNotFound
}

// inCI is true on a detected CI platform or wherever the conventional CI variable is set.
func inCI(env ci.LookupEnv) bool {
	if value, ok := env("CI"); ok && value != "" && value != "false" && value != "0" {
		return true
	}
	_, found, err := ci.DetectProvider(env)
	return err == nil && found
}
