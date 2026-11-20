// Package tokensource finds the PackageMaze Token a command uses for each
// Organization: the environment first, then the profile `maze auth login`
// saved for that Organization. Inside CI a saved profile is used only when the
// job names one, so a job never picks up a developer's Token by accident.
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

// Label names the Token for a message: the variable, or the profile that saved it.
func (o Origin) Label() string {
	if o.Kind == KindProfile {
		return "the Token saved in profile " + o.Name
	}
	return o.Name
}

type Source struct {
	Origin
	Token string
	// Organization is the saved profile's Organization; empty for the environment.
	Organization string
	// Problems say why a saved profile may not work for this command.
	Problems []*ProfileProblem
}

type Lookup struct {
	Env ci.LookupEnv
	// TokenEnv is the environment variable to read first.
	TokenEnv string
	// Profile is --profile. It serves its own Organization; others use the profile saved for them.
	Profile   string
	ConfigDir func() (string, error)
	// PackageClientURL is the Package Client Domain the command talks to; a profile saved for another is a Problem.
	PackageClientURL string
	Now              func() time.Time
}

// Result is the Token for one Organization's Feeds, or why there is none.
type Result struct {
	Organization string
	Source       Source
	Err          error
}

// AmbiguousError means several saved profiles hold a Token for one Organization.
type AmbiguousError struct {
	Organization string
	Profiles     []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("several saved profiles (%s) hold a Token for Organization %s; pass --profile", strings.Join(e.Profiles, ", "), e.Organization)
}

type ProblemReason string

const (
	ProblemExpired     ProblemReason = "expired"
	ProblemOtherDomain ProblemReason = "other_package_client_domain"
)

type ProfileProblem struct {
	Reason           ProblemReason
	Profile          string
	Organization     string
	ExpiresAt        time.Time
	SavedDomain      string
	PackageClientURL string
}

func (p *ProfileProblem) Error() string {
	return p.Summary() + "; " + p.Fix()
}

func (p *ProfileProblem) Summary() string {
	if p.Reason == ProblemExpired {
		return fmt.Sprintf("the Token saved in profile %s expired at %s", p.Profile, p.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("profile %s was saved for the Package Client Domain %s, but this command uses %s", p.Profile, p.SavedDomain, p.PackageClientURL)
}

func (p *ProfileProblem) Fix() string {
	if p.Reason == ProblemExpired {
		return fmt.Sprintf("run maze auth login --org %s --profile %s", p.Organization, p.Profile)
	}
	return fmt.Sprintf("pass --package-client-url %s, or run maze auth login --org %s --profile %s --package-client-url %s", p.SavedDomain, p.Organization, p.Profile, p.PackageClientURL)
}

// Resolve finds the Token for one Organization's Feeds.
func Resolve(lookup Lookup, organization string) (Source, error) {
	result := ResolveEach(lookup, []string{organization})[0]
	return result.Source, result.Err
}

// ResolveEach finds the Token for each Organization, so one Organization's Token never reaches another's Feeds.
func ResolveEach(lookup Lookup, organizations []string) []Result {
	env := lookup.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	source, err := FromEnv(env, lookup.TokenEnv)
	if !errors.Is(err, ErrNotFound) {
		return same(organizations, source, err)
	}
	requested := strings.TrimSpace(lookup.Profile)
	matchSaved := !inCI(env)
	if (requested == "" && !matchSaved) || lookup.ConfigDir == nil {
		return same(organizations, Source{}, ErrNotFound)
	}
	dir, err := lookup.ConfigDir()
	if err != nil {
		return same(organizations, Source{}, fmt.Errorf("find the user config directory for saved profiles: %w", err))
	}
	path := login.CredentialsPath(dir)
	profiles, err := login.LoadProfiles(path)
	if err != nil {
		return same(organizations, Source{}, err)
	}
	if requested != "" {
		profile, ok := profiles[requested]
		if !ok {
			return same(organizations, Source{}, fmt.Errorf("profile %s is not in %s; run maze auth login --profile %s", requested, path, requested))
		}
		if !servesAny(profile, organizations) {
			return same(organizations, Source{}, fmt.Errorf("profile %s holds a Token for Organization %s, not %s", requested, profile.Organization, strings.Join(organizations, " or ")))
		}
	}
	results := make([]Result, 0, len(organizations))
	for _, organization := range organizations {
		name, err := chooseProfile(profiles, requested, organization, matchSaved)
		result := Result{Organization: organization, Err: err}
		if err == nil {
			result.Source, result.Err = fromProfile(name, profiles[name], path, lookup)
		}
		results = append(results, result)
	}
	return results
}

// FromEnv reads the Token variable alone. A variable that is set but empty is an error, not an absent Token.
func FromEnv(env ci.LookupEnv, name string) (Source, error) {
	value, ok := env(name)
	if !ok {
		return Source{}, ErrNotFound
	}
	token := strings.TrimSpace(value)
	if token == "" {
		return Source{}, &EmptyEnvError{Name: name}
	}
	return Source{Origin: Origin{Kind: KindEnv, Name: name}, Token: token}, nil
}

type EmptyEnvError struct {
	Name string
}

func (e *EmptyEnvError) Error() string {
	return fmt.Sprintf("%s is set but empty; set it to a Token Secret, or unset it to use a profile maze auth login saved", e.Name)
}

func same(organizations []string, source Source, err error) []Result {
	results := make([]Result, 0, len(organizations))
	for _, organization := range organizations {
		results = append(results, Result{Organization: organization, Source: source, Err: err})
	}
	return results
}

func chooseProfile(profiles map[string]login.Profile, requested string, organization string, matchSaved bool) (string, error) {
	if requested != "" && serves(profiles[requested], organization) {
		return requested, nil
	}
	if !matchSaved {
		return "", ErrNotFound
	}
	var candidates []string
	for name, profile := range profiles {
		if serves(profile, organization) {
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
		return "", &AmbiguousError{Organization: organization, Profiles: candidates}
	}
}

func fromProfile(name string, profile login.Profile, path string, lookup Lookup) (Source, error) {
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

func serves(profile login.Profile, organization string) bool {
	return profile.Organization != "" && strings.EqualFold(profile.Organization, strings.TrimSpace(organization))
}

func servesAny(profile login.Profile, organizations []string) bool {
	for _, organization := range organizations {
		if serves(profile, organization) {
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
	saved, wanted := strings.TrimRight(profile.PackageClientURL, "/"), strings.TrimRight(lookup.PackageClientURL, "/")
	if saved != "" && wanted != "" && !strings.EqualFold(saved, wanted) {
		other := base
		other.Reason, other.SavedDomain, other.PackageClientURL = ProblemOtherDomain, saved, wanted
		found = append(found, &other)
	}
	return found
}

// ciVariables mark CI platforms maze has no identity-token support for; ci.DetectProvider covers the rest.
var ciVariables = []string{"JENKINS_URL", "TF_BUILD", "BUILDKITE", "GITLAB_CI", "TEAMCITY_VERSION", "BITBUCKET_BUILD_NUMBER", "CODEBUILD_BUILD_ID"}

// inCI is true on a detected CI platform, where detection itself fails, or wherever a CI variable is set.
func inCI(env ci.LookupEnv) bool {
	if value, ok := env("CI"); ok {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "false", "0":
		default:
			return true
		}
	}
	for _, name := range ciVariables {
		if value, ok := env(name); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	_, found, err := ci.DetectProvider(env)
	return found || err != nil
}
