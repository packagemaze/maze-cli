package doctor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/endpoint"
)

const maxProbedFeeds = 4

var ErrChecksFailed = errors.New("maze doctor found failing checks")

type Config struct {
	AllowInsecureLocalhost bool
	Dir                    string
	Feed                   string
	Format                 string
	JSONAlias              bool
	Offline                bool
	PackageClientURL       string
	Timeout                time.Duration
	TokenEnv               string
}

type Dependencies struct {
	Env        ci.LookupEnv
	HTTPClient *http.Client
	Prober     Prober
}

// ResolvedConfig carries the Token for probing and redaction only; it is never
// written to a report.
type ResolvedConfig struct {
	Config
	Domain      packageClientDomain
	FormatValue Format
	TargetFeed  *Feed
	Token       string
}

func Resolve(config Config, deps Dependencies) (ResolvedConfig, error) {
	env := deps.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	config.Dir = firstNonEmpty(config.Dir, ".")
	config.TokenEnv = firstNonEmpty(config.TokenEnv, endpoint.DefaultTokenEnv)
	config.PackageClientURL = endpoint.ResolvePackageClientURL(config.PackageClientURL, env)
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	if config.Timeout < 0 {
		return ResolvedConfig{}, fmt.Errorf("--timeout must be positive")
	}
	if config.JSONAlias {
		if strings.TrimSpace(config.Format) != "" && strings.TrimSpace(config.Format) != string(FormatMarkdown) {
			return ResolvedConfig{}, fmt.Errorf("--json cannot be combined with --format")
		}
		config.Format = string(FormatJSON)
	}
	format, err := ParseFormat(config.Format)
	if err != nil {
		return ResolvedConfig{}, err
	}
	domain, err := newPackageClientDomain(config.PackageClientURL, config.AllowInsecureLocalhost)
	if err != nil {
		return ResolvedConfig{}, err
	}
	resolved := ResolvedConfig{Config: config, Domain: domain, FormatValue: format}
	if strings.TrimSpace(config.Feed) != "" {
		feed, ok := domain.feedFromSlug(config.Feed)
		if !ok {
			return ResolvedConfig{}, fmt.Errorf("--feed must be in org/feed form")
		}
		resolved.TargetFeed = &feed
	}
	resolved.Token, _, _ = ci.ReadTokenEnv(env, config.TokenEnv)
	return resolved, nil
}

// FailureError is the non-zero exit for a report with failing checks; the
// report itself has already been written to stdout.
func (r Report) FailureError() error {
	if !r.Failed() {
		return nil
	}
	return fmt.Errorf("%w: %s, see the report", ErrChecksFailed, plural(countStatus(r.Checks, StatusFail), "check failed", "checks failed"))
}

// Run reads the repository, applies the secretless checks, and, when a Token is
// available, makes one read-only request per configured Feed to prove the
// Token against it. It writes nothing and contacts only the Package Client
// Domain.
func Run(ctx context.Context, config Config, deps Dependencies) (Report, ResolvedConfig, error) {
	if deps.Env == nil {
		deps.Env = ci.DefaultLookupEnv
	}
	resolved, err := Resolve(config, deps)
	if err != nil {
		return Report{}, ResolvedConfig{}, err
	}
	project, err := discoverProject(resolved.Dir)
	if err != nil {
		return Report{}, resolved, fmt.Errorf("maze doctor cannot read %s: %w", resolved.Dir, err)
	}
	defer project.close()
	scan := scanProject(project, resolved.Domain, deps.Env)

	feeds := collectFeeds(scan.declarations)
	if resolved.TargetFeed != nil {
		feeds = selectFeed(feeds, *resolved.TargetFeed)
	}
	checks := scan.checks
	checks = append(checks, crossChecks(feeds, scan)...)
	token := readTokenStatus(deps.Env, resolved.TokenEnv, resolved.Token != "")
	checks = append(checks, tokenChecks(token, feeds)...)

	notChecked := project.notes
	for _, skipped := range project.skipped {
		notChecked = append(notChecked, NotChecked{Check: "unreadable_file", Reason: fmt.Sprintf("%s is not a regular file under %d bytes and was not read.", skipped, maxConfigFileBytes)})
	}
	switch {
	case len(feeds) == 0:
	case resolved.Offline:
		notChecked = append(notChecked, NotChecked{Check: "live_token_check", Reason: "--offline was set, so the Token was not tested against the Feed."})
	case resolved.Token == "":
		notChecked = append(notChecked, NotChecked{Check: "live_token_check", Reason: fmt.Sprintf("%s is not set, so no request was made to PackageMaze.", resolved.TokenEnv)})
	default:
		prober := deps.Prober
		if prober == nil {
			httpClient := deps.HTTPClient
			if httpClient == nil {
				httpClient = &http.Client{Timeout: resolved.Timeout}
			}
			prober = NewHTTPProber(httpClient, resolved.AllowInsecureLocalhost)
		}
		probed, notes := probeFeeds(ctx, prober, feeds, resolved.Token, resolved.TokenEnv)
		checks = append(checks, probed...)
		notChecked = append(notChecked, notes...)
	}
	if len(feeds) > 0 {
		notChecked = append(notChecked, standardNotChecked()...)
	}
	sortChecks(checks)

	report := Report{
		Directory:     project.dir,
		Status:        overallStatus(checks),
		Feeds:         feeds,
		Configuration: scan.declarations,
		Token:         token,
		Checks:        checks,
		NotChecked:    notChecked,
		Docs:          []string{DocsFixAFailingInstall, DocsAgentQuickstart},
	}
	if len(feeds) == 0 {
		report.Status = StatusNotSetUp
	}
	report.NextStep = nextStep(report.Status)
	return report, resolved, nil
}

type projectScan struct {
	reading
	pnpmDescriptors     map[string]pnpmRegistryDescriptor
	pnpmInUse           bool
	pnpmWorkspaceSource string
}

// scanProject is the composition root for the readers: it decides which files
// are read and in which order, and nothing else does.
func scanProject(project *project, domain packageClientDomain, env ci.LookupEnv) *projectScan {
	scan := &projectScan{pnpmWorkspaceSource: "pnpm-workspace.yaml"}
	if file, ok := project.nearest("package.json"); ok {
		result, usesPnpm := readPackageJSON(file, domain)
		scan.merge(result)
		scan.pnpmInUse = usesPnpm
	}
	if file, ok := project.nearest("pnpm-workspace.yaml"); ok {
		result, descriptors := readPnpmWorkspace(file, domain)
		scan.merge(result)
		scan.pnpmDescriptors = descriptors
		scan.pnpmWorkspaceSource = file.path
		scan.pnpmInUse = true
	}
	scan.pnpmInUse = scan.pnpmInUse || project.exists("pnpm-lock.yaml")
	if file, ok := project.nearest(".npmrc"); ok {
		scan.merge(readNpmrc(file, domain, scan.pnpmInUse))
	}
	if file, ok := project.nearest(".yarnrc.yml"); ok {
		scan.merge(readYarnrc(file, domain))
	}
	if file, ok := project.nearest("bunfig.toml"); ok {
		scan.merge(readBunfig(file, domain))
	}
	for _, file := range project.nearestMatching(isRequirementsFile) {
		scan.merge(readRequirements(file, domain))
	}
	for _, name := range []string{"pip.conf", "pip.ini"} {
		if file, ok := project.nearest(name); ok {
			scan.merge(readPipConf(file, domain))
		}
	}
	if file, ok := project.nearest("pyproject.toml"); ok {
		scan.merge(readPyproject(file, domain))
	}
	if file, ok := project.nearest("uv.toml"); ok {
		scan.merge(readUvToml(file, domain))
	}
	if file, ok := project.nearest("Pipfile"); ok {
		scan.merge(readPipfile(file, domain))
	}
	scan.merge(readWorkflows(project, domain))
	scan.merge(readEnvironment(env, domain))
	return scan
}

func collectFeeds(declarations []Declaration) []FeedReport {
	var feeds []FeedReport
	index := map[string]int{}
	for _, declaration := range declarations {
		if declaration.Feed == nil {
			continue
		}
		slug := declaration.Feed.Slug()
		position, seen := index[slug]
		if !seen {
			position = len(feeds)
			index[slug] = position
			feeds = append(feeds, FeedReport{Feed: *declaration.Feed})
		}
		if feeds[position].Protocol == ProtocolUnknown {
			feeds[position].Protocol = declaration.Protocol
		}
		if !slices.Contains(feeds[position].Sources, declaration.Source) {
			feeds[position].Sources = append(feeds[position].Sources, declaration.Source)
		}
	}
	return feeds
}

func selectFeed(feeds []FeedReport, target Feed) []FeedReport {
	for _, feed := range feeds {
		if strings.EqualFold(feed.Slug(), target.Slug()) {
			return []FeedReport{feed}
		}
	}
	return []FeedReport{{Feed: target, Protocol: ProtocolUnknown, Sources: []string{"--feed"}}}
}

func crossChecks(feeds []FeedReport, scan *projectScan) []Check {
	var checks []Check
	byProtocol := map[ArtifactProtocol][]FeedReport{}
	for _, feed := range feeds {
		byProtocol[feed.Protocol] = append(byProtocol[feed.Protocol], feed)
	}
	for _, protocol := range []ArtifactProtocol{ProtocolNpm, ProtocolPyPI} {
		group := byProtocol[protocol]
		if len(group) < 2 {
			continue
		}
		names := make([]string, 0, len(group))
		for _, feed := range group {
			names = append(names, fmt.Sprintf("%s (%s)", feed.Slug(), strings.Join(feed.Sources, ", ")))
		}
		checks = append(checks, check(
			"multiple_feeds_configured",
			StatusWarning,
			fmt.Sprintf("%d %s Feeds are configured: %s.", len(group), protocolLabel(protocol), strings.Join(names, ", ")),
			"Point every client at one Feed per Artifact Protocol, or pass --feed to diagnose one of them.",
		))
	}
	committedNpm := map[string]bool{}
	committedAny := map[string]bool{}
	for _, declaration := range scan.declarations {
		if !declaration.committed() {
			continue
		}
		committedAny[declaration.Feed.Slug()] = true
		if declaration.Protocol == ProtocolNpm && declaration.Client != "pnpm" {
			committedNpm[declaration.Feed.Slug()] = true
		}
	}
	checks = append(checks, protocolConflictChecks(scan.declarations)...)
	checks = append(checks, ciFeedChecks(scan.declarations, committedAny)...)
	if scan.pnpmInUse {
		for _, feed := range feeds {
			if feed.Protocol == ProtocolNpm && committedNpm[feed.Slug()] {
				checks = append(checks, pnpmDescriptorCheck(scan.pnpmWorkspaceSource, feed.Feed, scan.pnpmDescriptors))
			}
		}
	}
	return checks
}

func protocolConflictChecks(declarations []Declaration) []Check {
	sources := map[string]map[ArtifactProtocol][]string{}
	for _, declaration := range declarations {
		if declaration.Feed == nil || declaration.Protocol == ProtocolUnknown {
			continue
		}
		slug := declaration.Feed.Slug()
		if sources[slug] == nil {
			sources[slug] = map[ArtifactProtocol][]string{}
		}
		if !slices.Contains(sources[slug][declaration.Protocol], declaration.Source) {
			sources[slug][declaration.Protocol] = append(sources[slug][declaration.Protocol], declaration.Source)
		}
	}
	var checks []Check
	for _, slug := range sortedKeys(sources) {
		byProtocol := sources[slug]
		if len(byProtocol[ProtocolNpm]) == 0 || len(byProtocol[ProtocolPyPI]) == 0 {
			continue
		}
		checks = append(checks, Check{
			Code:        "feed_protocol_conflict",
			Status:      StatusWarning,
			Summary:     fmt.Sprintf("npm clients (%s) and Python clients (%s) both point at Feed %s, but a Feed serves one Artifact Protocol.", strings.Join(byProtocol[ProtocolNpm], ", "), strings.Join(byProtocol[ProtocolPyPI], ", "), slug),
			Remediation: "Give each Artifact Protocol its own Feed and point each client at the matching one.",
			Feed:        slug,
		})
	}
	return checks
}

func ciFeedChecks(declarations []Declaration, committed map[string]bool) []Check {
	if len(committed) == 0 {
		return nil
	}
	committedNames := strings.Join(sortedKeys(committed), ", ")
	var checks []Check
	for _, declaration := range declarations {
		if declaration.Client != githubActionsClient || declaration.Feed == nil || committed[declaration.Feed.Slug()] {
			continue
		}
		checks = append(checks, check(
			"ci_feed_mismatch",
			StatusWarning,
			fmt.Sprintf("%s mints a Token for Feed %s, but the committed package-client configuration points at %s.", declaration.Source, declaration.Feed.Slug(), committedNames),
			"Use the same Feed in the workflow's setup-maze step and in the committed client configuration.",
		).at(declaration.Source).forFeed(*declaration.Feed))
	}
	return checks
}

// probeFeeds probes each configured Feed once and stops after the first sign
// that PackageMaze cannot be reached or is throttling this client, so a run
// never waits out several timeouts or feeds the throttle.
func probeFeeds(ctx context.Context, prober Prober, feeds []FeedReport, token string, tokenEnv string) ([]Check, []NotChecked) {
	var checks []Check
	var notes []NotChecked
	for position, feed := range feeds {
		if position >= maxProbedFeeds {
			notes = append(notes, NotChecked{Check: "live_token_check", Reason: fmt.Sprintf("Feed %s was not probed; at most %d Feeds are probed per run. Pass --feed to choose.", feed.Slug(), maxProbedFeeds)})
			continue
		}
		result, protocol := probeFeed(ctx, prober, feed, token)
		checks = append(checks, probeCheck(result, feed.Feed, protocol, tokenEnv))
		switch result.Outcome {
		case ProbeTokenRejected:
			notes = append(notes, NotChecked{Check: "token_rejection_reason", Reason: "PackageMaze does not tell package clients why a Token was rejected, so maze doctor cannot distinguish an expired, revoked, wrong-Feed, or read-less Token."})
		case ProbeUnreachable, ProbeThrottled:
			for _, remaining := range feeds[position+1:] {
				notes = append(notes, NotChecked{Check: "live_token_check", Reason: fmt.Sprintf("Feed %s was not probed after the previous request %s.", remaining.Slug(), map[ProbeOutcome]string{ProbeUnreachable: "could not reach PackageMaze", ProbeThrottled: "was throttled"}[result.Outcome])})
			}
			return checks, notes
		}
	}
	return checks, notes
}

// probeFeed probes with the protocol the configuration implies; when no client
// file told us which, it asks as an npm client first and as a PyPI client only
// if no npm Feed answers at that address.
func probeFeed(ctx context.Context, prober Prober, feed FeedReport, token string) (ProbeResult, ArtifactProtocol) {
	if feed.Protocol != ProtocolUnknown {
		return prober.Probe(ctx, ProbeRequest{Feed: feed.Feed, Protocol: feed.Protocol, Token: token}), feed.Protocol
	}
	result := prober.Probe(ctx, ProbeRequest{Feed: feed.Feed, Protocol: ProtocolNpm, Token: token})
	if result.Outcome != ProbeFeedNotFound {
		return result, ProtocolNpm
	}
	return prober.Probe(ctx, ProbeRequest{Feed: feed.Feed, Protocol: ProtocolPyPI, Token: token}), ProtocolPyPI
}

func standardNotChecked() []NotChecked {
	return []NotChecked{
		{Check: "user_level_client_config", Reason: "User-level client configuration (~/.npmrc, pip and uv credentials, Poetry's auth store) was not read; credentials belong there, not in the repository."},
		{Check: "package_version_policy", Reason: "Whether a specific Package Version is held by the Minimum Age Policy, blocked by a rule, or delisted was not checked; the install error names the package, and " + DocsFixAFailingInstall + " maps it to the fix."},
	}
}

func nextStep(status Status) string {
	switch status {
	case StatusNotSetUp:
		return fmt.Sprintf("Set the repository up with PackageMaze: commit the Feed Base URL for each package client and keep the Token in the environment. Start at %s (agents) or %s and %s (people).", DocsAgentQuickstart, DocsSetUpNpmAndPnpm, DocsSetUpPipUvPoetry)
	case StatusFail:
		return "Apply the next step of the first failing check, then run maze doctor again."
	case StatusWarning:
		return "The setup works; review the warnings, each names its file and fix."
	default:
		return fmt.Sprintf("The setup is healthy. If an install still fails, the cause is a specific package: a policy hold, a blocked name, or a delisted version. %s maps the symptom to the fix.", DocsFixAFailingInstall)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
