package doctor

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/tokensource"
)

// lookUpTokens finds a Token per Organization of the Feeds. Without a Feed, or
// with --offline and no --profile, it reads the Token variable only.
func lookUpTokens(config ResolvedConfig, feeds []FeedReport, deps Dependencies) []tokensource.Result {
	organizations := feedOrganizations(feeds)
	if len(feeds) == 0 || (config.Offline && strings.TrimSpace(config.Profile) == "") {
		source, err := tokensource.FromEnv(deps.Env, config.TokenEnv)
		results := make([]tokensource.Result, 0, len(organizations))
		for _, organization := range organizations {
			results = append(results, tokensource.Result{Organization: organization, Source: source, Err: err})
		}
		return results
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return tokensource.ResolveEach(tokensource.Lookup{
		ConfigDir:        deps.ConfigDir,
		Env:              deps.Env,
		Now:              now,
		PackageClientURL: config.PackageClientURL,
		Profile:          config.Profile,
		TokenEnv:         config.TokenEnv,
	}, organizations)
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

func tokensByOrganization(results []tokensource.Result) map[string]tokensource.Source {
	tokens := map[string]tokensource.Source{}
	for _, result := range results {
		if result.Source.Token != "" {
			tokens[result.Organization] = result.Source
		}
	}
	return tokens
}

func tokenValues(results []tokensource.Result) []string {
	var values []string
	for _, result := range results {
		if result.Source.Token != "" && !slices.Contains(values, result.Source.Token) {
			values = append(values, result.Source.Token)
		}
	}
	return values
}

func readTokenStatus(env ci.LookupEnv, tokenEnv string, results []tokensource.Result) TokenStatus {
	source, err := tokensource.FromEnv(env, tokenEnv)
	var empty *tokensource.EmptyEnvError
	status := TokenStatus{Env: tokenEnv, Present: source.Token != "", EnvEmpty: errors.As(err, &empty)}
	for _, result := range results {
		entry := OrganizationToken{Organization: result.Organization}
		if result.Source.Token != "" {
			origin := result.Source.Origin
			entry.Source = &origin
			status.Present = true
		}
		status.Organizations = append(status.Organizations, entry)
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

func tokenChecks(status TokenStatus, results []tokensource.Result, feeds []FeedReport) []Check {
	if len(feeds) == 0 {
		return nil
	}
	if status.EnvEmpty {
		return []Check{check(
			"token_env_empty",
			StatusWarning,
			fmt.Sprintf("%s is set but empty, so package clients that read it send no Token and maze doctor tested none.", status.Env),
			fmt.Sprintf("Set %s to a Token Secret, or unset it so maze doctor can use a profile maze auth login saved.", status.Env),
		).at(environmentSourcePrefix + status.Env)}
	}
	if len(results) > 0 && results[0].Source.Kind == tokensource.KindEnv {
		return []Check{check("token_env_present", StatusPass, fmt.Sprintf("%s is set; its value is never printed.", status.Env), "No action needed.").at(environmentSourcePrefix + status.Env)}
	}
	var checks []Check
	var missing []string
	reported := map[string]bool{}
	for _, result := range results {
		var ambiguous *tokensource.AmbiguousError
		switch {
		case result.Source.Token != "":
			checks = append(checks, profileTokenChecks(status.Env, result.Source)...)
		case errors.As(result.Err, &ambiguous):
			checks = append(checks, check(
				"token_profile_ambiguous",
				StatusWarning,
				fmt.Sprintf("%s is not set, and saved profiles %s all hold a Token for Organization %s, so maze doctor did not choose one.", status.Env, strings.Join(ambiguous.Profiles, ", "), ambiguous.Organization),
				fmt.Sprintf("Run maze doctor --profile %s (or another of them), or set %s.", ambiguous.Profiles[0], status.Env),
			))
		case errors.Is(result.Err, tokensource.ErrNotFound):
			missing = append(missing, result.Organization)
		case !reported[result.Err.Error()]:
			reported[result.Err.Error()] = true
			checks = append(checks, check(
				"token_lookup_failed",
				StatusFail,
				fmt.Sprintf("maze doctor could not read a Token: %v.", result.Err),
				"Fix the Token source named above, then run maze doctor again.",
			))
		}
	}
	return append(checks, missingTokenChecks(status, missing, feeds)...)
}

func profileTokenChecks(tokenEnv string, source tokensource.Source) []Check {
	at := "profile:" + source.Name
	checks := []Check{check(
		"token_profile_present",
		StatusPass,
		fmt.Sprintf("%s is not set, so Organization %s uses %s; its value is never printed.", tokenEnv, source.Organization, source.Label()),
		"No action needed.",
	).at(at)}
	for _, problem := range source.Problems {
		code := "token_profile_other_api"
		if problem.Reason == tokensource.ProblemExpired {
			code = "token_profile_expired"
		}
		checks = append(checks, check(code, StatusWarning, sentence(problem.Summary()), sentence(problem.Fix())).at(at))
	}
	return checks
}

func missingTokenChecks(status TokenStatus, organizations []string, feeds []FeedReport) []Check {
	if len(organizations) == 0 {
		return nil
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
	var checks []Check
	for _, organization := range organizations {
		feed := feeds[slices.IndexFunc(feeds, func(feed FeedReport) bool { return feed.Organization == organization })]
		remediation := fmt.Sprintf(
			"Create a Token with Read access to Feed %s in PackageMaze (the Token Secret is shown once), then run: export %s=\"<Token Secret>\". Package clients read it from there (%s). Get one: %s and %s",
			feed.Slug(), status.Env, tokenWiringHint(feed.Protocol), DocsFixAFailingInstall, DocsAgentQuickstart,
		)
		checks = append(checks, check(
			"token_env_missing",
			StatusFail,
			fmt.Sprintf("%s is not set and no package-client credential variable is set, so installs from Feed %s run unauthenticated and PackageMaze refuses them.", status.Env, feed.Slug()),
			remediation,
		).at(environmentSourcePrefix+status.Env).forFeed(feed.Feed))
	}
	return checks
}

// noTokenReason says why no live request was made, matching the lookup.
func noTokenReason(status TokenStatus, results []tokensource.Result) string {
	if status.EnvEmpty {
		return fmt.Sprintf("%s is set but empty, so no request was made to PackageMaze.", status.Env)
	}
	for _, result := range results {
		if !errors.Is(result.Err, tokensource.ErrNotFound) {
			return "No Token could be chosen, so no request was made to PackageMaze."
		}
	}
	return fmt.Sprintf("%s is not set, so no request was made to PackageMaze.", status.Env)
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

// tokenSourceSentence says which Token each Organization uses, exactly as the lookup decided, never its value.
func tokenSourceSentence(status TokenStatus) string {
	var parts []string
	for _, entry := range status.Organizations {
		switch {
		case entry.Source == nil:
			parts = append(parts, fmt.Sprintf("Organization %s has no Token", entry.Organization))
		case entry.Source.Kind == tokensource.KindProfile:
			parts = append(parts, fmt.Sprintf("Organization %s uses %s", entry.Organization, entry.Source.Label()))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s is set; its value is never printed.", status.Env)
	}
	return fmt.Sprintf("%s is not set. %s; Token values are never printed.", status.Env, strings.Join(parts, "; "))
}
