package doctor

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/endpoint"
	"github.com/packagemaze/maze-cli/internal/tokensource"
)

type tokenLookup struct {
	source tokensource.Source
	err    error
}

// lookUpToken reads a saved profile only when a Feed needs a Token and the run may go online.
func lookUpToken(config ResolvedConfig, feeds []FeedReport, deps Dependencies) tokenLookup {
	if len(feeds) == 0 || config.Offline {
		source, err := tokensource.FromEnv(deps.Env, config.TokenEnv)
		return tokenLookup{source: source, err: err}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	source, err := tokensource.Resolve(tokensource.Lookup{
		APIURL:        endpoint.ResolveAPIURL("", deps.Env),
		ConfigDir:     deps.ConfigDir,
		Env:           deps.Env,
		Now:           now,
		Organizations: feedOrganizations(feeds),
		Profile:       config.Profile,
		TokenEnv:      config.TokenEnv,
	})
	return tokenLookup{source: source, err: err}
}

func (l tokenLookup) failed() bool {
	return l.err != nil && !errors.Is(l.err, tokensource.ErrNotFound)
}

func (l tokenLookup) use() tokenUse {
	return tokenUse{origin: l.source.Origin, organization: l.source.Organization}
}

func feedOrganizations(feeds []FeedReport) []string {
	var organizations []string
	for _, feed := range feeds {
		if !slices.Contains(organizations, feed.Organization) {
			organizations = append(organizations, feed.Organization)
		}
	}
	return organizations
}

// tokenUse names the Token maze doctor tested, never its value, and how to replace it.
type tokenUse struct {
	origin       tokensource.Origin
	organization string
}

func (u tokenUse) label() string {
	if u.origin.Kind == tokensource.KindProfile {
		return "the Token saved in profile " + u.origin.Name
	}
	return u.origin.Name
}

func (u tokenUse) rejectedFix(feed Feed) string {
	if u.origin.Kind == tokensource.KindProfile {
		return fmt.Sprintf("Run maze auth login --org %s --profile %s to replace that Token with one that can read Feed %s. See %s", u.organization, u.origin.Name, feed.Slug(), DocsFixAFailingInstall)
	}
	return fmt.Sprintf("Create a new Token with Read access to Feed %s, or rotate the existing one, and set %s to its Secret. See %s", feed.Slug(), u.origin.Name, DocsFixAFailingInstall)
}

func readTokenStatus(env ci.LookupEnv, tokenEnv string, source tokensource.Source) TokenStatus {
	status := TokenStatus{Env: tokenEnv, Present: source.Token != ""}
	if status.Present {
		origin := source.Origin
		status.Source = &origin
	}
	for _, name := range clientCredentialVariables {
		if envValue(env, name) != "" {
			status.ClientCredentials = append(status.ClientCredentials, name)
		}
	}
	for _, variable := range environmentIndexes {
		if variable.protocol == ProtocolPyPI && !variable.extra && urlHasCredentials(envValue(env, variable.name)) {
			status.ClientCredentials = append(status.ClientCredentials, variable.name)
		}
	}
	return status
}

func tokenChecks(status TokenStatus, lookup tokenLookup, feeds []FeedReport) []Check {
	if len(feeds) == 0 {
		return nil
	}
	var ambiguous *tokensource.AmbiguousError
	switch {
	case errors.As(lookup.err, &ambiguous):
		return []Check{check(
			"token_profile_ambiguous",
			StatusWarning,
			fmt.Sprintf("%s is not set, and saved profiles %s all hold a Token for this repository's Organization, so maze doctor did not choose one.", status.Env, strings.Join(ambiguous.Profiles, ", ")),
			fmt.Sprintf("Run maze doctor --profile %s (or another of them), or set %s.", ambiguous.Profiles[0], status.Env),
		)}
	case lookup.failed():
		return []Check{check(
			"token_lookup_failed",
			StatusFail,
			fmt.Sprintf("maze doctor could not read a Token: %v.", lookup.err),
			"Fix the Token source named above, then run maze doctor again.",
		)}
	case status.Present:
		return presentTokenChecks(status, lookup.source)
	}
	if len(status.ClientCredentials) > 0 {
		variables := strings.Join(status.ClientCredentials, ", ")
		return []Check{check(
			"client_credential_present",
			StatusPass,
			fmt.Sprintf("%s carries a package-client credential; %s is not set, so maze doctor cannot test it against the Feed.", variables, status.Env),
			fmt.Sprintf("Set %s to the same Token Secret to let maze doctor verify it.", status.Env),
		).at(environmentSourcePrefix + variables)}
	}
	remediation := fmt.Sprintf(
		"Create a Token with Read access to Feed %s in PackageMaze (the Token Secret is shown once), then run: export %s=\"<Token Secret>\". Package clients read it from there (%s). Get one: %s and %s",
		feeds[0].Slug(), status.Env, tokenWiringHint(feeds[0].Protocol), DocsFixAFailingInstall, DocsAgentQuickstart,
	)
	return []Check{check(
		"token_env_missing",
		StatusFail,
		fmt.Sprintf("%s is not set and no package-client credential variable is set, so installs from Feed %s run unauthenticated and PackageMaze refuses them.", status.Env, feeds[0].Slug()),
		remediation,
	).at(environmentSourcePrefix + status.Env).forFeed(feeds[0].Feed)}
}

func presentTokenChecks(status TokenStatus, source tokensource.Source) []Check {
	if source.Kind == tokensource.KindEnv {
		return []Check{check("token_env_present", StatusPass, tokenPresentSentence(status), "No action needed.").at(environmentSourcePrefix + status.Env)}
	}
	at := "profile:" + source.Name
	checks := []Check{check("token_profile_present", StatusPass, tokenPresentSentence(status), "No action needed.").at(at)}
	for _, problem := range source.Problems {
		code := "token_profile_other_api"
		if problem.Reason == tokensource.ProblemExpired {
			code = "token_profile_expired"
		}
		checks = append(checks, check(code, StatusWarning, sentence(problem.Summary()), sentence(problem.Fix())).at(at))
	}
	return checks
}

func sentence(text string) string {
	return strings.ToUpper(text[:1]) + text[1:] + "."
}

func tokenWiringHint(protocol ArtifactProtocol) string {
	switch protocol {
	case ProtocolNpm:
		return "npm: npm config set \"//<host>/<organization>/<feed>/:_authToken\" \"${MAZE_TOKEN}\" in user-level config; pnpm: pass it as env-config or a CI temp npm config"
	case ProtocolPyPI:
		return "pip: PIP_INDEX_URL=https://__token__:${MAZE_TOKEN}@<host>/<organization>/<feed>/simple/; uv: UV_INDEX_PACKAGEMAZE_USERNAME=__token__ and UV_INDEX_PACKAGEMAZE_PASSWORD=${MAZE_TOKEN}; Poetry: poetry config http-basic.packagemaze __token__ \"${MAZE_TOKEN}\""
	default:
		return "see the Feed's Setup Instructions for the client's credential variable"
	}
}

// tokenPresentSentence names where the Token came from without printing it.
func tokenPresentSentence(status TokenStatus) string {
	if status.Source != nil && status.Source.Kind == tokensource.KindProfile {
		return fmt.Sprintf("%s is not set, so the Token saved in profile %s is used; its value is never printed.", status.Env, status.Source.Name)
	}
	return fmt.Sprintf("%s is set; its value is never printed.", status.Env)
}
