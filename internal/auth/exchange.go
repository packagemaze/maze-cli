package auth

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/ci"
	"github.com/packagemaze/maze-cli/internal/output"
)

const (
	DefaultBaseURL      = "https://api.packagemaze.com"
	DefaultOIDCTokenEnv = "MAZE_OIDC_TOKEN"
	DefaultOutputName   = "token"
)

var (
	feedPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	outputNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	BaseURL                  string
	APIURL                   string
	Feed                     string
	Purpose                  string
	Package                  string
	Provider                 string
	Audience                 string
	OIDCTokenEnv             string
	OIDCTokenFile            string
	OIDCTokenStdin           bool
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
	ProviderValue ci.Provider
	FormatValue   output.Format
}

func Exchange(ctx context.Context, config Config, deps Dependencies) (output.Result, ResolvedConfig, error) {
	resolved, err := Resolve(config, deps)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	oidcToken, err := acquireOIDCToken(ctx, resolved, deps)
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	packageName := strings.TrimSpace(resolved.Package)
	var requestPackage *string
	if packageName != "" {
		requestPackage = &packageName
	}
	exchanger := deps.Exchanger
	if exchanger == nil {
		httpClient := deps.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: resolved.Timeout}
		}
		exchanger = api.NewClient(resolved.APIURL, httpClient)
	}
	response, err := exchanger.ExchangeCI(ctx, api.CITokenRequest{
		Provider:  string(resolved.ProviderValue),
		Feed:      resolved.Feed,
		Purpose:   resolved.Purpose,
		Package:   requestPackage,
		Audience:  resolved.Audience,
		OIDCToken: oidcToken,
	})
	if err != nil {
		return output.Result{}, ResolvedConfig{}, err
	}
	return output.Result{
		Token:            response.Token,
		ExpiresAt:        response.ExpiresAt,
		TokenType:        response.TokenType,
		Feed:             fallback(response.Feed, resolved.Feed),
		FeedBaseURL:      response.FeedBaseURL,
		Purpose:          fallback(response.Purpose, resolved.Purpose),
		Package:          packageName,
		Scopes:           response.Scopes,
		Provider:         string(resolved.ProviderValue),
		ArtifactProtocol: response.ArtifactProtocol,
	}, resolved, nil
}

func Resolve(config Config, deps Dependencies) (ResolvedConfig, error) {
	env := deps.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
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
	config.OutputName = firstNonEmpty(config.OutputName, DefaultOutputName)
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
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
	providerValue, err := resolveProvider(config, env)
	if err != nil {
		return ResolvedConfig{}, err
	}
	resolved := ResolvedConfig{
		Config:        config,
		ProviderValue: providerValue,
		FormatValue:   formatValue,
	}
	if err := validateResolved(resolved, env); err != nil {
		return ResolvedConfig{}, err
	}
	return resolved, nil
}

func validateResolved(config ResolvedConfig, env ci.LookupEnv) error {
	if !feedPattern.MatchString(strings.TrimSpace(config.Feed)) {
		return fmt.Errorf("--feed must be in org/feed form")
	}
	switch strings.TrimSpace(config.Purpose) {
	case "install", "publish", "docker-build", "test":
	default:
		return fmt.Errorf("--purpose must be install, publish, docker-build, or test")
	}
	if config.Purpose == "publish" && strings.TrimSpace(config.Package) == "" {
		return fmt.Errorf("--package is required when --purpose publish")
	}
	if config.Purpose != "publish" && strings.TrimSpace(config.Package) != "" {
		return fmt.Errorf("--package is only supported when --purpose publish")
	}
	if config.Timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	if err := validateURL("base-url", config.BaseURL, config.AllowInsecureLocalhost); err != nil {
		return err
	}
	if err := validateURL("api-url", config.APIURL, config.AllowInsecureLocalhost); err != nil {
		return err
	}
	if strings.TrimSpace(config.Audience) == "" {
		return fmt.Errorf("--audience must not be empty")
	}
	if config.OIDCTokenFile != "" && config.OIDCTokenStdin {
		return fmt.Errorf("choose either --oidc-token-file or --oidc-token-stdin, not both")
	}
	if config.FormatValue == output.FormatGitHubOutput {
		if !outputNamePattern.MatchString(config.OutputName) {
			return fmt.Errorf("--output-name must start with a letter or underscore and contain only letters, numbers, and underscores")
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

func resolveProvider(config Config, env ci.LookupEnv) (ci.Provider, error) {
	requested, err := ci.ParseProvider(config.Provider)
	if err != nil {
		return "", err
	}
	if requested != ci.ProviderAuto {
		return requested, nil
	}
	detected, found, err := ci.DetectProvider(env)
	if err != nil {
		return "", err
	}
	if found {
		return detected, nil
	}
	if hasManualTokenSource(config, env) {
		return ci.ProviderManual, nil
	}
	return "", fmt.Errorf(`No CI provider was detected and no OIDC token was supplied.
Run inside GitHub Actions, GitLab CI/CD, or CircleCI, or pass a token with --oidc-token-stdin, --oidc-token-file, or %s.`, config.OIDCTokenEnv)
}

func acquireOIDCToken(ctx context.Context, config ResolvedConfig, deps Dependencies) (string, error) {
	if config.OIDCTokenFile != "" {
		return ci.ReadTokenFile(config.OIDCTokenFile)
	}
	if config.OIDCTokenStdin {
		return ci.ReadTokenStdin(deps.Stdin)
	}

	env := deps.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	switch config.ProviderValue {
	case ci.ProviderGitHub:
		httpClient := deps.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: config.Timeout}
		}
		return ci.RequestGitHubOIDCToken(ctx, config.Audience, env, httpClient)
	case ci.ProviderGitLab:
		if token, _, ok := ci.ReadTokenEnv(env, config.OIDCTokenEnv, "PACKAGEMAZE_OIDC_TOKEN"); ok {
			return token, nil
		}
		return "", fmt.Errorf(`GitLab CI/CD was detected, but no OIDC token was found.
Add this to your job:
id_tokens:
  %s:
    aud: %s`, config.OIDCTokenEnv, config.Audience)
	case ci.ProviderCircleCI:
		if token, _, ok := ci.ReadTokenEnv(env, config.OIDCTokenEnv, "PACKAGEMAZE_OIDC_TOKEN"); ok {
			return token, nil
		}
		return ci.RequestCircleCIToken(ctx, config.Audience, deps.LookPath, deps.CommandRunner)
	case ci.ProviderManual:
		if token, _, ok := ci.ReadTokenEnv(env, config.OIDCTokenEnv, "PACKAGEMAZE_OIDC_TOKEN"); ok {
			return token, nil
		}
		return "", fmt.Errorf(`Manual OIDC token exchange requires a token.
Pass one with --oidc-token-stdin, --oidc-token-file, or %s.`, config.OIDCTokenEnv)
	default:
		return "", fmt.Errorf("provider must be auto, github, gitlab, circleci, or manual")
	}
}

func hasManualTokenSource(config Config, env ci.LookupEnv) bool {
	if config.OIDCTokenFile != "" || config.OIDCTokenStdin {
		return true
	}
	if _, _, ok := ci.ReadTokenEnv(env, firstNonEmpty(config.OIDCTokenEnv, DefaultOIDCTokenEnv), "PACKAGEMAZE_OIDC_TOKEN"); ok {
		return true
	}
	return false
}

func validateURL(flag string, value string, allowInsecureLocalhost bool) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("--%s must be an absolute URL", flag)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && allowInsecureLocalhost && isLocalhost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("--%s must use https; use --allow-insecure-localhost only for local http endpoints", flag)
}

func isLocalhost(host string) bool {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if normalized == "localhost" {
		return true
	}
	ip := net.ParseIP(normalized)
	return ip != nil && ip.IsLoopback()
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
