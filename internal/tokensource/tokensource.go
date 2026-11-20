// Package tokensource finds the PackageMaze Token a command uses: the
// environment first, then a profile `maze auth login` saved for the target
// Organization. Inside CI a saved profile is used only when the job names one,
// so a job never picks up a developer's Token by accident.
package tokensource

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/login"
)

// ErrNotFound means no source holds a Token.
var ErrNotFound = errors.New("no PackageMaze Token found")

type Kind string

const (
	KindEnv     Kind = "env"
	KindProfile Kind = "profile"
)

// Origin says where a Token came from without carrying it.
type Origin struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
}

type Source struct {
	Origin
	Token string
	// Organization is the saved profile's Organization; empty for the environment.
	Organization string
	// Problems say why a saved profile may not work for this command: an expired Token or another PackageMaze API.
	Problems []*ProfileProblem
}

type Lookup struct {
	Env ci.LookupEnv
	// TokenEnv is the environment variable to read first.
	TokenEnv string
	// Profile is --profile; empty picks the one profile saved for one of Organizations.
	Profile       string
	Organizations []string
	ConfigDir     func() (string, error)
	// APIURL is the PackageMaze API the command belongs to; a profile saved for another is a Problem.
	APIURL string
	Now    func() time.Time
}

// AmbiguousError means several saved profiles hold a Token for the target Organization.
type AmbiguousError struct {
	Profiles []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("several saved profiles (%s) hold a Token for this Organization; pass --profile", strings.Join(e.Profiles, ", "))
}

type ProblemReason string

const (
	ProblemExpired  ProblemReason = "expired"
	ProblemOtherAPI ProblemReason = "other_api"
)

type ProfileProblem struct {
	Reason       ProblemReason
	Profile      string
	Organization string
	ExpiresAt    time.Time
	SavedAPIURL  string
	APIURL       string
}

func (p *ProfileProblem) Error() string {
	return p.Summary() + "; " + p.Fix()
}

func (p *ProfileProblem) Summary() string {
	if p.Reason == ProblemExpired {
		return fmt.Sprintf("the Token saved in profile %s expired at %s", p.Profile, p.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("profile %s was saved for the PackageMaze API at %s, but this command uses %s", p.Profile, p.SavedAPIURL, p.APIURL)
}

func (p *ProfileProblem) Fix() string {
	login := fmt.Sprintf("run maze auth login --org %s --profile %s", p.Organization, p.Profile)
	if p.Reason == ProblemExpired {
		return login
	}
	return "set MAZE_BASE_URL to match, or " + login
}

func Resolve(lookup Lookup) (Source, error) {
	env := lookup.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	source, err := FromEnv(env, lookup.TokenEnv)
	if !errors.Is(err, ErrNotFound) {
		return source, err
	}
	requested := strings.TrimSpace(lookup.Profile)
	if (requested == "" && inCI(env)) || lookup.ConfigDir == nil {
		return Source{}, ErrNotFound
	}
	dir, err := lookup.ConfigDir()
	if err != nil {
		return Source{}, fmt.Errorf("find the user config directory for saved profiles: %w", err)
	}
	path := login.CredentialsPath(dir)
	profiles, err := login.LoadProfiles(path)
	if err != nil {
		return Source{}, err
	}
	name, err := chooseProfile(profiles, requested, lookup.Organizations, path)
	if err != nil {
		return Source{}, err
	}
	profile := profiles[name]
	token := strings.TrimSpace(profile.Token)
	if token == "" {
		return Source{}, fmt.Errorf("profile %s in %s holds no Token; run maze auth login --org %s --profile %s", name, path, profile.Organization, name)
	}
	return Source{
		Origin:       Origin{Kind: KindProfile, Name: name},
		Token:        token,
		Organization: profile.Organization,
		Problems:     problems(name, profile, lookup),
	}, nil
}

// FromEnv reads the Token variable alone. A variable that is set but empty is an error, not an absent Token.
func FromEnv(env ci.LookupEnv, name string) (Source, error) {
	value, ok := env(name)
	if !ok {
		return Source{}, ErrNotFound
	}
	token := strings.TrimSpace(value)
	if token == "" {
		return Source{}, fmt.Errorf("%s is set but empty; set it to a Token Secret, or unset it to use a profile maze auth login saved", name)
	}
	return Source{Origin: Origin{Kind: KindEnv, Name: name}, Token: token}, nil
}

func chooseProfile(profiles map[string]login.Profile, requested string, organizations []string, path string) (string, error) {
	if requested != "" {
		profile, ok := profiles[requested]
		if !ok {
			return "", fmt.Errorf("profile %s is not in %s; run maze auth login --profile %s", requested, path, requested)
		}
		if len(organizations) > 0 && !servesAny(profile, organizations) {
			return "", fmt.Errorf("profile %s holds a Token for Organization %s, not %s", requested, profile.Organization, strings.Join(organizations, " or "))
		}
		return requested, nil
	}
	var candidates []string
	for name, profile := range profiles {
		if servesAny(profile, organizations) {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 0:
		return "", ErrNotFound
	case 1:
		return candidates[0], nil
	default:
		sort.Strings(candidates)
		return "", &AmbiguousError{Profiles: candidates}
	}
}

func servesAny(profile login.Profile, organizations []string) bool {
	for _, organization := range organizations {
		if profile.Organization != "" && strings.EqualFold(profile.Organization, organization) {
			return true
		}
	}
	return false
}

func problems(name string, profile login.Profile, lookup Lookup) []*ProfileProblem {
	var found []*ProfileProblem
	base := ProfileProblem{Profile: name, Organization: profile.Organization}
	if expiresAt, err := time.Parse(time.RFC3339, profile.ExpiresAt); err == nil && lookup.Now != nil && !lookup.Now().Before(expiresAt) {
		expired := base
		expired.Reason, expired.ExpiresAt = ProblemExpired, expiresAt
		found = append(found, &expired)
	}
	saved, wanted := strings.TrimRight(profile.APIURL, "/"), strings.TrimRight(lookup.APIURL, "/")
	if saved != "" && wanted != "" && !strings.EqualFold(saved, wanted) {
		other := base
		other.Reason, other.SavedAPIURL, other.APIURL = ProblemOtherAPI, saved, wanted
		found = append(found, &other)
	}
	return found
}

// inCI is true on a detected CI platform, where detection itself fails, or wherever the conventional CI variable is set.
func inCI(env ci.LookupEnv) bool {
	if value, ok := env("CI"); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "false", "0":
		default:
			return true
		}
	}
	_, found, err := ci.DetectProvider(env)
	return found || err != nil
}
