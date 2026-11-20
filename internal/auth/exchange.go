package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/endpoint"
	"github.com/packagemaze/maze-cli/internal/output"
)

const (
	DefaultBaseURL              = "https://api.packagemaze.com"
	DefaultOIDCTokenEnv         = "MAZE_OIDC_TOKEN"
	IdentityTokenEnv            = "MAZE_IDENTITY_TOKEN"
	legacyOIDCTokenEnv          = "PACKAGEMAZE_OIDC_TOKEN"
	DefaultOutputName           = "token"
	DefaultSetupInvocationIDEnv = "MAZE_SETUP_INVOCATION_ID"
	maxReportedLength           = 256
	maxFeeds                    = 20
)

var (
	outputNamePattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	setupInvocationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$`)
)

type Config struct {
	BaseURL                  string
	APIURL                   string
	Feeds                    []string
	Purpose                  string
	Package                  string
	Provider                 string
	Audience                 string
	OIDCTokenEnv             string
	OIDCTokenFile            string
	OIDCTokenStdin           bool
	IdentityInjected         bool // the platform adds the identity token to the request; maze sends none
	SetupInvocationID        string
	ReportedRef              *string
	ReportedCommit           *string
	ClientContextJSON        string
	Format                   string
	OutputName               string
	Timeout                  time.Duration
	Verbose                  bool
	NoColor                  bool
	JSONAlias                bool
	AllowInsecureLocalhost   bool
	AllowGitHubOutputOutside bool
}

type Dependencies struct {
	Env           ci.LookupEnv
	Stdin         io.Reader
	HTTPClient    *http.Client
	Exchanger     TokenExchanger
	LookPath      ci.LookPath
	CommandRunner ci.CommandRunner
}

type TokenExchanger interface {
	ExchangeCI(context.Context, api.CITokenRequest) (api.CITokenResponse, error)
}

type ResolvedConfig struct {
	Config
	// ProviderValue is how maze obtains the identity token: a built-in
	// platform (named or detected), oidc or manual (named), or auto when the
	// caller supplies the token or the platform injects it.
	ProviderValue ci.Provider
	// ProviderNamed says the caller chose ProviderValue with --provider.
	ProviderNamed bool
	FormatValue   output.Format
}

func Exchange(ctx context.Context, config Config, deps Dependencies) (output.Result, ResolvedConfig, error) {
	resolved, err := Resolve(config, deps)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	oidcToken, provider, err := acquireIdentityToken(ctx, resolved, deps)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	packageName := strings.TrimSpace(resolved.Package)
	var requestPackage *string
	if packageName != "" {
		requestPackage = &packageName
	}
	clientContext, err := parseClientContext(resolved.ClientContextJSON)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	clientContext = mergeClientContext(
		clientContext,
		ci.ClientContext(resolved.ProviderValue, deps.Env),
	)
	exchanger := deps.Exchanger
	if exchanger == nil {
		httpClient := deps.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: resolved.Timeout}
		}
		exchanger = api.NewClient(resolved.APIURL, httpClient)
	}
	request := api.CITokenRequest{
		Provider:          provider,
		Purpose:           resolved.Purpose,
		Package:           requestPackage,
		Audience:          resolved.Audience,
		OIDCToken:         oidcToken,
		SetupInvocationID: resolved.SetupInvocationID,
		Client:            clientContext,
		Reported:          reportedContext(resolved.Config),
	}
	if len(resolved.Feeds) == 1 {
		request.Feed = resolved.Feeds[0]
	} else {
		request.Feeds = resolved.Feeds
	}
	response, err := exchanger.ExchangeCI(ctx, request)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	return output.Result{
		Token:            response.Token,
		ExpiresAt:        response.ExpiresAt,
		TokenType:        response.TokenType,
		Feed:             fallback(response.Feed, request.Feed),
		FeedBaseURL:      response.FeedBaseURL,
		Purpose:          fallback(response.ExchangePurpose, resolved.Purpose),
		BuildNumber:      response.BuildNumber,
		BuildURL:         response.BuildURL,
		Package:          packageName,
		Scopes:           response.Scopes,
		Provider:         provider,
		ArtifactProtocol: response.ArtifactProtocol,
		Feeds:            response.Feeds,
		FeedReferences:   request.Feeds,
	}, resolved, nil
}

func Resolve(config Config, deps Dependencies) (ResolvedConfig, error) {
	env := lookupEnv(deps)
	config = withIdentitySourceDefaults(config, env)
	config.SetupInvocationID = firstNonEmpty(
		config.SetupInvocationID,
		envValue(env, DefaultSetupInvocationIDEnv),
	)
	config.ReportedRef = trimmedOptional(config.ReportedRef)
	config.ReportedCommit = trimmedOptional(config.ReportedCommit)
	config.OutputName = firstNonEmpty(config.OutputName, DefaultOutputName)
	if config.JSONAlias {
		if strings.TrimSpace(config.Format) != "" && strings.TrimSpace(config.Format) != string(output.FormatToken) {
			return ResolvedConfig{}, fmt.Errorf("--json cannot be combined with --format")
		}
		config.Format = string(output.FormatJSON)
	}
	formatValue, err := output.ParseFormat(config.Format)
	if err != nil {
		return ResolvedConfig{}, err
	}
	resolved, err := resolveIdentitySource(config, env)
	if err != nil {
		return ResolvedConfig{}, err
	}
	resolved.FormatValue = formatValue
	if err := validateResolved(resolved, env); err != nil {
		return ResolvedConfig{}, err
	}
	return resolved, nil
}

func validateResolved(config ResolvedConfig, env ci.LookupEnv) error {
	if err := validateFeeds(config); err != nil {
		return err
	}
	switch strings.TrimSpace(config.Purpose) {
	case "install", "publish", "docker-build", "test":
	default:
		return fmt.Errorf("--purpose must be install, publish, docker-build, or test")
	}
	if config.Purpose != "publish" && strings.TrimSpace(config.Package) != "" {
		return fmt.Errorf("--package is only supported when --purpose publish")
	}
	if config.Timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	if err := validateEndpoints(config); err != nil {
		return err
	}
	if config.SetupInvocationID != "" && !setupInvocationIDPattern.MatchString(config.SetupInvocationID) {
		return fmt.Errorf("--setup-invocation-id must be 1-160 letters, numbers, dots, underscores, colons, or hyphens; prefix generated ids with the wrapper name")
	}
	if err := validateReported("--ref", config.ReportedRef); err != nil {
		return err
	}
	if err := validateReported("--commit", config.ReportedCommit); err != nil {
		return err
	}
	if err := validateTokenInput(config, env); err != nil {
		return err
	}
	if config.FormatValue == output.FormatGitHubOutput {
		if !outputNamePattern.MatchString(config.OutputName) {
			return fmt.Errorf("--output-name must start with a letter or underscore and contain only letters, numbers, and underscores")
		}
		if githubOutputNameReserved(config.OutputName) {
			return fmt.Errorf("--output-name %s is reserved for PackageMaze metadata outputs", config.OutputName)
		}
		_, hasOutput := env("GITHUB_OUTPUT")
		inGitHubActions := envEqualsTrue(env, "GITHUB_ACTIONS")
		if !hasOutput {
			return fmt.Errorf("--format github-output requires GITHUB_OUTPUT to be set")
		}
		if !inGitHubActions && !config.AllowGitHubOutputOutside && !envEqualsTrue(env, "MAZE_ALLOW_GITHUB_OUTPUT_OUTSIDE_ACTIONS") {
			return fmt.Errorf("--format github-output is only supported inside GitHub Actions")
		}
	}
	return nil
}

// validateFeeds refuses what needs no server to refuse; PackageMaze checks the
// rest, such as Feeds in two Organizations.
func validateFeeds(config ResolvedConfig) error {
	if len(config.Feeds) == 0 || len(config.Feeds) > maxFeeds {
		return fmt.Errorf("--feed must be given 1 to %d times", maxFeeds)
	}
	seen := map[string]bool{}
	for _, feed := range config.Feeds {
		if _, _, ok := endpoint.SplitFeedSlug(feed); !ok {
			return fmt.Errorf("--feed must be in org/feed form")
		}
		if seen[feed] {
			return fmt.Errorf("--feed %s is given twice", feed)
		}
		seen[feed] = true
	}
	if len(config.Feeds) == 1 {
		return nil
	}
	if config.Purpose == "publish" {
		return fmt.Errorf("only one --feed is supported when --purpose publish")
	}
	return nil
}

func reportedContext(config Config) *api.ReportedContext {
	if config.ReportedRef == nil && config.ReportedCommit == nil {
		return nil
	}
	return &api.ReportedContext{Ref: valueOrEmpty(config.ReportedRef), Commit: valueOrEmpty(config.ReportedCommit)}
}

// validateReported matches the server's limit, which counts UTF-16 code units.
func validateReported(flag string, value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" {
		return fmt.Errorf("%s must not be empty", flag)
	}
	if len(utf16.Encode([]rune(*value))) > maxReportedLength {
		return fmt.Errorf("%s must be at most %d characters", flag, maxReportedLength)
	}
	return nil
}

// ResolveIdentitySource resolves only what obtaining the job's identity token
// needs, so commands that never exchange it share exchange-oidc's handling.
func ResolveIdentitySource(config Config, deps Dependencies) (ResolvedConfig, error) {
	env := lookupEnv(deps)
	config = withIdentitySourceDefaults(config, env)
	resolved, err := resolveIdentitySource(config, env)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if err := validateEndpoints(resolved); err != nil {
		return ResolvedConfig{}, err
	}
	if err := validateTokenInput(resolved, env); err != nil {
		return ResolvedConfig{}, err
	}
	return resolved, nil
}

// AcquireIdentityToken obtains the job's identity token exactly as
// exchange-oidc does. The value is a credential: callers must not print it.
func AcquireIdentityToken(ctx context.Context, config ResolvedConfig, deps Dependencies) (string, error) {
	if config.IdentityInjected {
		return "", errors.New("the platform adds this identity token to requests, so maze cannot read it")
	}
	token, _, err := acquireIdentityToken(ctx, config, deps)
	return token, err
}

func withIdentitySourceDefaults(config Config, env ci.LookupEnv) Config {
	config.BaseURL = firstNonEmpty(config.BaseURL, envValue(env, "MAZE_BASE_URL"), DefaultBaseURL)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.APIURL == "" {
		config.APIURL = config.BaseURL + "/v1"
	}
	config.APIURL = strings.TrimRight(strings.TrimSpace(config.APIURL), "/")
	if config.Audience == "" {
		config.Audience = config.BaseURL
	}
	config.Audience = strings.TrimSpace(config.Audience)
	config.OIDCTokenEnv = firstNonEmpty(config.OIDCTokenEnv, DefaultOIDCTokenEnv)
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	return config
}

func validateEndpoints(config ResolvedConfig) error {
	if err := endpoint.ValidateURL("base-url", config.BaseURL, config.AllowInsecureLocalhost); err != nil {
		return err
	}
	if err := endpoint.ValidateURL("api-url", config.APIURL, config.AllowInsecureLocalhost); err != nil {
		return err
	}
	if strings.TrimSpace(config.Audience) == "" {
		return fmt.Errorf("--audience must not be empty")
	}
	return nil
}

func trimmedOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateTokenInput(config ResolvedConfig, env ci.LookupEnv) error {
	if config.OIDCTokenFile != "" && config.OIDCTokenStdin {
		return fmt.Errorf("choose either --identity-token-file or --identity-token-stdin, not both")
	}
	if !config.IdentityInjected {
		return nil
	}
	if config.ProviderNamed {
		return fmt.Errorf("--identity-token-injected cannot be combined with --provider %s: the identity token's issuer selects the provider", config.ProviderValue)
	}
	if source := suppliedTokenSource(config.Config, env); source != "" {
		return fmt.Errorf("--identity-token-injected means the platform adds the identity token to the request and maze sends none; remove %s", source)
	}
	return nil
}

func lookupEnv(deps Dependencies) ci.LookupEnv {
	if deps.Env == nil {
		return ci.DefaultLookupEnv
	}
	return deps.Env
}

func parseClientContext(value string) (map[string]any, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > 16*1024 {
		return nil, fmt.Errorf("--client-context-json must be at most 16384 bytes")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, fmt.Errorf("--client-context-json must be a JSON object: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("--client-context-json must be a JSON object")
	}
	return payload, nil
}

func mergeClientContext(
	legacy map[string]any,
	automatic map[string]any,
) map[string]any {
	if len(legacy) == 0 {
		return automatic
	}
	if len(automatic) == 0 {
		return legacy
	}
	merged := make(map[string]any, len(legacy)+len(automatic))
	for key, value := range legacy {
		merged[key] = value
	}
	for key, value := range automatic {
		merged[key] = value
	}
	return merged
}

func githubOutputNameReserved(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "artifact_protocol", "build_number", "build_url", "feed_base_url":
		return true
	default:
		return false
	}
}

func resolveIdentitySource(config Config, env ci.LookupEnv) (ResolvedConfig, error) {
	requested, err := ci.ParseProvider(config.Provider)
	if err != nil {
		return ResolvedConfig{}, err
	}
	resolved := ResolvedConfig{Config: config, ProviderValue: requested, ProviderNamed: requested != ci.ProviderAuto}
	if resolved.ProviderNamed || config.IdentityInjected {
		return resolved, nil
	}
	detected, found, err := ci.DetectProvider(env)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if found {
		resolved.ProviderValue = detected
		return resolved, nil
	}
	if suppliedTokenSource(config, env) != "" {
		return resolved, nil
	}
	return ResolvedConfig{}, fmt.Errorf(`No built-in platform was detected and no identity token was supplied.
On GitHub Actions and CircleCI, maze requests the job's identity token itself. On any other platform, pass the identity token it issued to this job with --identity-token-stdin, --identity-token-file, or %s, or pass --identity-token-injected when the platform adds it to the request.`, config.OIDCTokenEnv)
}

// acquireIdentityToken obtains the job's identity token and the provider the
// request names: the one the caller named, or the built-in platform whose own
// mechanism maze used. A supplied or injected token names none, so its
// issuer selects the provider.
func acquireIdentityToken(ctx context.Context, config ResolvedConfig, deps Dependencies) (string, string, error) {
	if config.IdentityInjected {
		return "", "", nil
	}
	named := ""
	if config.ProviderNamed {
		named = string(config.ProviderValue)
	}
	if config.OIDCTokenFile != "" {
		token, err := ci.ReadTokenFile(config.OIDCTokenFile)
		return token, named, err
	}
	if config.OIDCTokenStdin {
		token, err := ci.ReadTokenStdin(deps.Stdin)
		return token, named, err
	}
	env := lookupEnv(deps)
	if config.ProviderValue == ci.ProviderGitHub {
		token, err := requestGitHubToken(ctx, config, deps, env)
		return token, string(ci.ProviderGitHub), err
	}
	if token, _, ok := ci.ReadTokenEnv(env, identityTokenEnvNames(config.Config)...); ok {
		return token, named, nil
	}
	if config.ProviderValue == ci.ProviderCircleCI {
		token, err := ci.RequestCircleCIToken(ctx, config.Audience, deps.LookPath, deps.CommandRunner)
		return token, string(ci.ProviderCircleCI), err
	}
	return "", "", missingIdentityTokenError(config)
}

func requestGitHubToken(ctx context.Context, config ResolvedConfig, deps Dependencies, env ci.LookupEnv) (string, error) {
	httpClient := deps.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: config.Timeout}
	}
	return ci.RequestGitHubOIDCToken(ctx, config.Audience, env, httpClient)
}

func missingIdentityTokenError(config ResolvedConfig) error {
	if config.ProviderValue == ci.ProviderOIDC {
		return fmt.Errorf(`--provider oidc needs the identity token your platform issued to this job.
Request it for audience %s, then pass it with --identity-token-stdin, --identity-token-file, or %s.`, config.Audience, config.OIDCTokenEnv)
	}
	return fmt.Errorf(`--provider manual is for local development and needs an identity token.
Pass one with --identity-token-stdin, --identity-token-file, or %s.`, config.OIDCTokenEnv)
}

// identityTokenEnvNames lists the variables read for a supplied identity
// token, in order: the named one (MAZE_OIDC_TOKEN unless --identity-token-env
// says otherwise), then MAZE_IDENTITY_TOKEN, then the legacy name.
func identityTokenEnvNames(config Config) []string {
	return []string{firstNonEmpty(config.OIDCTokenEnv, DefaultOIDCTokenEnv), IdentityTokenEnv, legacyOIDCTokenEnv}
}

// suppliedTokenSource names where the caller supplied an identity token, or
// returns "" when they supplied none.
func suppliedTokenSource(config Config, env ci.LookupEnv) string {
	if config.OIDCTokenFile != "" {
		return "--identity-token-file"
	}
	if config.OIDCTokenStdin {
		return "--identity-token-stdin"
	}
	if _, name, ok := ci.ReadTokenEnv(env, identityTokenEnvNames(config)...); ok {
		return name
	}
	return ""
}

func envValue(env ci.LookupEnv, key string) string {
	if value, ok := env(key); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func envEqualsTrue(env ci.LookupEnv, key string) bool {
	value, ok := env(key)
	return ok && strings.EqualFold(strings.TrimSpace(value), "true")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fallback(value string, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		Env:           ci.DefaultLookupEnv,
		Stdin:         os.Stdin,
		LookPath:      ci.DefaultLookPath,
		CommandRunner: ci.DefaultCommandRunner,
	}
}
