package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/packagemaze/maze-cli/internal/ci"
)

const (
	DefaultPackageClientURL = "https://pkg.packagemaze.com"
	DefaultTokenEnv         = "MAZE_TOKEN"
)

var feedPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

type Config struct {
	AllowInsecureLocalhost bool
	Feed                   string
	Format                 string
	JSONAlias              bool
	PackageClientURL       string
	PackageHint            string
	StdinToken             bool
	Timeout                time.Duration
	TokenEnv               string
	TokenFile              string
	Verbose                bool
	VersionHint            string
	Wait                   bool
}

type Dependencies struct {
	Client     Client
	Command    string
	Env        ci.LookupEnv
	HTTPClient *http.Client
	Sleep      func(context.Context, time.Duration) error
	Stdin      io.Reader
	Uploader   Uploader
}

type Client interface {
	CompleteUpload(context.Context, string, CompletionInstruction, UploadResult) error
	CreateSession(context.Context, string, string, CreatePublishSessionRequest) (CreatePublishSessionResponse, error)
	GetStatus(context.Context, string, string) (PublishSessionStatusResponse, error)
}

type Uploader interface {
	Upload(context.Context, PlannedArtifact, string, io.Writer) (UploadResult, error)
}

type UploadResult struct {
	PartCount  int
	R2UploadID string
}

type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

type ResolvedConfig struct {
	Config
	FormatValue Format
	Token       string
}

type ArtifactFact struct {
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
}

type ClientInfo struct {
	Capabilities []string `json:"capabilities"`
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
}

type CreatePublishSessionRequest struct {
	Artifacts []ArtifactFact    `json:"artifacts"`
	Client    ClientInfo        `json:"client"`
	Hints     map[string]string `json:"hints,omitempty"`
}

type CreatePublishSessionResponse struct {
	Plan           PublishPlan `json:"plan"`
	PublishSession struct {
		ArtifactProtocol string `json:"artifact_protocol"`
		ExpiresAt        string `json:"expires_at"`
		ID               string `json:"id"`
		State            string `json:"state"`
	} `json:"publish_session"`
	SchemaVersion int `json:"schema_version"`
}

type PublishPlan struct {
	Artifacts            []PlannedArtifact `json:"artifacts"`
	Capabilities         []string          `json:"capabilities"`
	Kind                 string            `json:"kind"`
	MaxArtifactSizeBytes int64             `json:"max_artifact_size_bytes"`
	SchemaVersion        int               `json:"schema_version"`
	Wait                 WaitInstruction   `json:"wait"`
}

type PlannedArtifact struct {
	Artifact   ArtifactFact          `json:"artifact"`
	ArtifactID string                `json:"artifact_id"`
	Completion CompletionInstruction `json:"completion"`
	Package    struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"package"`
	Upload struct {
		Kind          string `json:"kind"`
		PartSizeBytes int64  `json:"part_size_bytes"`
		Target        struct {
			Bucket      string `json:"bucket"`
			Credentials struct {
				AccessKeyID     string `json:"access_key_id"`
				ExpiresAt       string `json:"expires_at"`
				SecretAccessKey string `json:"secret_access_key"`
				SessionToken    string `json:"session_token"`
			} `json:"credentials"`
			Endpoint  string `json:"endpoint"`
			ObjectKey string `json:"object_key"`
			Region    string `json:"region"`
		} `json:"target"`
		UploadSessionID string `json:"upload_session_id"`
	} `json:"upload"`
}

type CompletionInstruction struct {
	BodyFields []string `json:"body_fields"`
	Method     string   `json:"method"`
	URL        string   `json:"url"`
}

type WaitInstruction struct {
	IntervalSeconds int      `json:"interval_seconds"`
	Method          string   `json:"method"`
	TerminalStates  []string `json:"terminal_states"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
	URL             string   `json:"url"`
}

type PublishSessionStatusResponse struct {
	Artifacts      []ArtifactStatus `json:"artifacts"`
	PublishSession struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"publish_session"`
	SchemaVersion int             `json:"schema_version"`
	Wait          WaitInstruction `json:"wait"`
}

type ArtifactStatus struct {
	Artifact ArtifactFact `json:"artifact"`
	Error    *Diagnostic  `json:"error"`
	Package  struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"package"`
	PublishJob *struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"publish_job"`
	State         string `json:"state"`
	UploadSession struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"upload_session"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Phase   string `json:"phase"`
}

type Result struct {
	Artifacts        []ArtifactResult `json:"artifacts"`
	ArtifactProtocol string           `json:"artifact_protocol,omitempty"`
	PublishSessionID string           `json:"publish_session_id"`
	State            string           `json:"state"`
}

type ArtifactResult struct {
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
	Filename       string `json:"filename"`
	PackageName    string `json:"package_name,omitempty"`
	PackageVersion string `json:"package_version,omitempty"`
	State          string `json:"state"`
}

type StatusError struct {
	StatusCode int
	Endpoint   string
	Detail     string
}

func (e *StatusError) Error() string {
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	if e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden {
		return fmt.Sprintf("PackageMaze rejected the publish Token for this Feed: %s", detail)
	}
	if e.StatusCode >= 500 {
		return fmt.Sprintf("PackageMaze publish request failed with HTTP %d: %s", e.StatusCode, detail)
	}
	return fmt.Sprintf("PackageMaze publish request was rejected with HTTP %d: %s", e.StatusCode, detail)
}

type MalformedResponseError struct {
	Endpoint string
	Err      error
}

func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("PackageMaze publish response from %s was not valid JSON", e.Endpoint)
}

func (e *MalformedResponseError) Unwrap() error {
	return e.Err
}

func Run(ctx context.Context, config Config, paths []string, deps Dependencies, stderr io.Writer) (Result, ResolvedConfig, error) {
	resolved, err := Resolve(config, deps)
	if err != nil {
		return Result{}, ResolvedConfig{}, err
	}
	if len(paths) == 0 {
		return Result{}, ResolvedConfig{}, fmt.Errorf("maze publish requires at least one path")
	}
	facts, err := fileFacts(paths)
	if err != nil {
		return Result{}, ResolvedConfig{}, err
	}
	client := deps.Client
	if client == nil {
		httpClient := deps.HTTPClient
		if httpClient == nil {
			httpClient = &http.Client{Timeout: resolved.Timeout}
		}
		client = NewClient(resolved.PackageClientURL, resolved.AllowInsecureLocalhost, httpClient)
	}
	uploader := deps.Uploader
	if uploader == nil {
		uploader = NewR2MultipartUploader()
	}
	reporter := stderr
	if reporter == nil {
		reporter = io.Discard
	}

	request := CreatePublishSessionRequest{
		Artifacts: facts,
		Client: ClientInfo{
			Capabilities: []string{
				"r2_multipart_upload_v1",
				"r2_multipart_completion_v1",
				"publish_session_status_v1",
			},
			Name:    firstNonEmpty(deps.Command, "maze"),
			Version: "",
		},
		Hints: publishHints(resolved),
	}
	session, err := client.CreateSession(ctx, resolved.Feed, resolved.Token, request)
	if err != nil {
		return Result{}, ResolvedConfig{}, err
	}
	if err := validatePlan(session); err != nil {
		return Result{}, ResolvedConfig{}, err
	}
	if len(session.Plan.Artifacts) != len(paths) {
		return Result{}, ResolvedConfig{}, fmt.Errorf("PackageMaze publish plan returned %d artifacts for %d local paths", len(session.Plan.Artifacts), len(paths))
	}

	for index, artifact := range session.Plan.Artifacts {
		if _, err := fmt.Fprintf(reporter, "Uploading %s\n", artifact.Artifact.Filename); err != nil {
			return Result{}, ResolvedConfig{}, err
		}
		upload, err := uploader.Upload(ctx, artifact, paths[index], reporter)
		if err != nil {
			return Result{}, ResolvedConfig{}, fmt.Errorf("upload %s: %w", artifact.Artifact.Filename, err)
		}
		if err := client.CompleteUpload(ctx, resolved.Token, artifact.Completion, upload); err != nil {
			return Result{}, ResolvedConfig{}, err
		}
	}

	status := PublishSessionStatusResponse{
		Artifacts:     sessionArtifactStatuses(session.Plan.Artifacts),
		SchemaVersion: 1,
	}
	status.PublishSession.ID = session.PublishSession.ID
	status.PublishSession.State = session.PublishSession.State
	if resolved.Wait {
		if _, err := fmt.Fprintln(reporter, "Waiting for PackageMaze publish status"); err != nil {
			return Result{}, ResolvedConfig{}, err
		}
		status, err = waitForStatus(ctx, client, resolved.Token, session.Plan.Wait, deps.Sleep)
		if err != nil {
			return Result{}, ResolvedConfig{}, err
		}
	} else {
		statusURL := session.Plan.Wait.URL
		if strings.TrimSpace(statusURL) != "" {
			if latest, err := client.GetStatus(ctx, resolved.Token, statusURL); err == nil {
				status = latest
			}
		}
	}
	result := resultFromStatus(status, session)
	if result.State == "error" {
		return result, resolved, publishStatusError(status)
	}
	return result, resolved, nil
}

func Resolve(config Config, deps Dependencies) (ResolvedConfig, error) {
	env := deps.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	config.PackageClientURL = strings.TrimRight(firstNonEmpty(config.PackageClientURL, envValue(env, "MAZE_PACKAGE_CLIENT_URL"), DefaultPackageClientURL), "/")
	config.TokenEnv = firstNonEmpty(config.TokenEnv, DefaultTokenEnv)
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Wait == false {
		// The zero value intentionally means no wait only after cobra has bound it.
	}
	if config.JSONAlias {
		if strings.TrimSpace(config.Format) != "" && strings.TrimSpace(config.Format) != string(FormatText) {
			return ResolvedConfig{}, fmt.Errorf("--json cannot be combined with --format")
		}
		config.Format = string(FormatJSON)
	}
	format, err := parseFormat(config.Format)
	if err != nil {
		return ResolvedConfig{}, err
	}
	if !feedPattern.MatchString(strings.TrimSpace(config.Feed)) {
		return ResolvedConfig{}, fmt.Errorf("--feed must be in org/feed form")
	}
	if err := validateURL("package-client-url", config.PackageClientURL, config.AllowInsecureLocalhost); err != nil {
		return ResolvedConfig{}, err
	}
	if config.Timeout <= 0 {
		return ResolvedConfig{}, fmt.Errorf("--timeout must be positive")
	}
	if config.TokenFile != "" && config.StdinToken {
		return ResolvedConfig{}, fmt.Errorf("choose either --token-file or --token-stdin, not both")
	}
	token, err := acquireToken(config, deps)
	if err != nil {
		return ResolvedConfig{}, err
	}
	config.Feed = strings.TrimSpace(config.Feed)
	return ResolvedConfig{Config: config, FormatValue: format, Token: token}, nil
}

func Write(result Result, format Format, writer io.Writer) error {
	if writer == nil {
		writer = io.Discard
	}
	switch format {
	case "", FormatText:
		_, err := fmt.Fprintf(writer, "Publish Session %s %s\n", result.PublishSessionID, result.State)
		if err != nil {
			return err
		}
		for _, artifact := range result.Artifacts {
			packageLabel := strings.TrimSpace(artifact.PackageName)
			if strings.TrimSpace(artifact.PackageVersion) != "" {
				packageLabel += "@" + artifact.PackageVersion
			}
			if packageLabel == "" {
				packageLabel = "package pending"
			}
			if _, err := fmt.Fprintf(writer, "%s %s %s\n", artifact.Filename, artifact.State, packageLabel); err != nil {
				return err
			}
		}
		return nil
	case FormatJSON:
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	default:
		return fmt.Errorf("format must be text or json")
	}
}

func fileFacts(paths []string) ([]ArtifactFact, error) {
	facts := make([]ArtifactFact, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("publish path %s is a directory", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("hash %s: %w", path, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s: %w", path, closeErr)
		}
		facts = append(facts, ArtifactFact{
			ContentType: contentTypeForPath(path),
			Filename:    filepath.Base(path),
			SHA256:      hex.EncodeToString(hasher.Sum(nil)),
			SizeBytes:   info.Size(),
		})
	}
	return facts, nil
}

func contentTypeForPath(path string) string {
	if contentType := mime.TypeByExtension(filepath.Ext(path)); strings.TrimSpace(contentType) != "" {
		return contentType
	}
	return "application/octet-stream"
}

func waitForStatus(ctx context.Context, client Client, token string, wait WaitInstruction, sleep func(context.Context, time.Duration) error) (PublishSessionStatusResponse, error) {
	if strings.TrimSpace(wait.URL) == "" {
		return PublishSessionStatusResponse{}, fmt.Errorf("PackageMaze publish plan did not include a status URL")
	}
	interval := time.Duration(wait.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 2 * time.Second
	}
	timeout := time.Duration(wait.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	deadline := time.Now().Add(timeout)
	var last PublishSessionStatusResponse
	for {
		status, err := client.GetStatus(ctx, token, wait.URL)
		if err != nil {
			return PublishSessionStatusResponse{}, err
		}
		last = status
		switch status.PublishSession.State {
		case "ready", "error":
			return status, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("PackageMaze publish did not reach ready before the wait timeout")
		}
		if err := sleep(ctx, interval); err != nil {
			return last, err
		}
	}
}

func validatePlan(response CreatePublishSessionResponse) error {
	if response.SchemaVersion != 1 || response.Plan.SchemaVersion != 1 {
		return fmt.Errorf("PackageMaze publish plan version is unsupported")
	}
	if response.Plan.Kind != "package_publish_plan" {
		return fmt.Errorf("PackageMaze publish plan kind is unsupported")
	}
	for _, artifact := range response.Plan.Artifacts {
		if artifact.Upload.Kind != "r2_multipart_upload_v1" {
			return fmt.Errorf("PackageMaze publish plan upload kind is unsupported")
		}
		if artifact.Completion.Method != http.MethodPost {
			return fmt.Errorf("PackageMaze publish plan completion method is unsupported")
		}
		if strings.TrimSpace(artifact.Completion.URL) == "" {
			return fmt.Errorf("PackageMaze publish plan completion URL is missing")
		}
	}
	return nil
}

func sessionArtifactStatuses(artifacts []PlannedArtifact) []ArtifactStatus {
	statuses := make([]ArtifactStatus, 0, len(artifacts))
	for _, artifact := range artifacts {
		status := ArtifactStatus{Artifact: artifact.Artifact, State: "uploaded"}
		status.Package.Name = artifact.Package.Name
		status.Package.Version = artifact.Package.Version
		status.UploadSession.ID = artifact.Upload.UploadSessionID
		status.UploadSession.State = "uploaded"
		statuses = append(statuses, status)
	}
	return statuses
}

func resultFromStatus(status PublishSessionStatusResponse, create CreatePublishSessionResponse) Result {
	result := Result{
		ArtifactProtocol: create.PublishSession.ArtifactProtocol,
		PublishSessionID: firstNonEmpty(status.PublishSession.ID, create.PublishSession.ID),
		State:            firstNonEmpty(status.PublishSession.State, create.PublishSession.State),
	}
	for _, artifact := range status.Artifacts {
		item := ArtifactResult{
			Filename:       artifact.Artifact.Filename,
			PackageName:    artifact.Package.Name,
			PackageVersion: artifact.Package.Version,
			State:          artifact.State,
		}
		if artifact.Error != nil {
			item.DiagnosticCode = artifact.Error.Code
		}
		result.Artifacts = append(result.Artifacts, item)
	}
	return result
}

func publishStatusError(status PublishSessionStatusResponse) error {
	var messages []string
	for _, artifact := range status.Artifacts {
		if artifact.Error != nil {
			messages = append(messages, fmt.Sprintf("%s: %s", artifact.Artifact.Filename, artifact.Error.Message))
		}
	}
	if len(messages) == 0 {
		return fmt.Errorf("PackageMaze publish failed")
	}
	return errors.New(strings.Join(messages, "; "))
}

func publishHints(config ResolvedConfig) map[string]string {
	hints := map[string]string{}
	if strings.TrimSpace(config.PackageHint) != "" {
		hints["package_name"] = strings.TrimSpace(config.PackageHint)
	}
	if strings.TrimSpace(config.VersionHint) != "" {
		hints["package_version"] = strings.TrimSpace(config.VersionHint)
	}
	if len(hints) == 0 {
		return nil
	}
	return hints
}

func acquireToken(config Config, deps Dependencies) (string, error) {
	if config.TokenFile != "" {
		content, err := os.ReadFile(config.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read PackageMaze Token file: %w", err)
		}
		return cleanToken(string(content), "PackageMaze Token file")
	}
	if config.StdinToken {
		content, err := io.ReadAll(deps.Stdin)
		if err != nil {
			return "", fmt.Errorf("read PackageMaze Token from stdin: %w", err)
		}
		return cleanToken(string(content), "stdin")
	}
	env := deps.Env
	if env == nil {
		env = ci.DefaultLookupEnv
	}
	if token, ok := env(config.TokenEnv); ok {
		return cleanToken(token, config.TokenEnv)
	}
	return "", fmt.Errorf("PackageMaze Token was not found. Set %s, use --token-file, or use --token-stdin", config.TokenEnv)
}

func cleanToken(value string, source string) (string, error) {
	token := strings.TrimSpace(value)
	if token == "" {
		return "", fmt.Errorf("%s did not contain a PackageMaze Token", source)
	}
	if strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("%s contained multiple lines; expected one PackageMaze Token", source)
	}
	return token, nil
}

func parseFormat(value string) (Format, error) {
	normalized := Format(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("format must be text or json")
	}
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type HTTPClient struct {
	allowInsecureLocalhost bool
	baseURL                string
	httpClient             *http.Client
}

func NewClient(baseURL string, allowInsecureLocalhost bool, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPClient{
		allowInsecureLocalhost: allowInsecureLocalhost,
		baseURL:                strings.TrimRight(baseURL, "/"),
		httpClient:             httpClient,
	}
}

func (c *HTTPClient) CreateSession(ctx context.Context, feed string, token string, request CreatePublishSessionRequest) (CreatePublishSessionResponse, error) {
	endpoint, err := c.publishSessionEndpoint(feed)
	if err != nil {
		return CreatePublishSessionResponse{}, err
	}
	var response CreatePublishSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint, token, request, http.StatusCreated, &response); err != nil {
		return CreatePublishSessionResponse{}, err
	}
	return response, nil
}

func (c *HTTPClient) CompleteUpload(ctx context.Context, token string, completion CompletionInstruction, upload UploadResult) error {
	if err := validateURL("publish-plan-completion-url", completion.URL, c.allowInsecureLocalhost); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, completion.URL, token, map[string]any{
		"part_count":   upload.PartCount,
		"r2_upload_id": upload.R2UploadID,
	}, http.StatusAccepted, nil)
}

func (c *HTTPClient) GetStatus(ctx context.Context, token string, statusURL string) (PublishSessionStatusResponse, error) {
	if err := validateURL("publish-plan-status-url", statusURL, c.allowInsecureLocalhost); err != nil {
		return PublishSessionStatusResponse{}, err
	}
	var response PublishSessionStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, statusURL, token, nil, http.StatusOK, &response); err != nil {
		return PublishSessionStatusResponse{}, err
	}
	return response, nil
}

func (c *HTTPClient) publishSessionEndpoint(feed string) (string, error) {
	parts := strings.Split(feed, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("--feed must be in org/feed form")
	}
	endpoint := c.baseURL + "/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/-/packagemaze/v1/publish-sessions"
	if err := validateURL("package-client-url", endpoint, c.allowInsecureLocalhost); err != nil {
		return "", err
	}
	return endpoint, nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method string, endpoint string, token string, body any, expectedStatus int, out any) error {
	var reader io.Reader
	if body != nil {
		content, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode PackageMaze publish request: %w", err)
		}
		reader = bytes.NewReader(content)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("build PackageMaze publish request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("PackageMaze publish request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return &StatusError{
			StatusCode: response.StatusCode,
			Endpoint:   endpoint,
			Detail:     responseDetail(response.Body),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(out); err != nil {
		return &MalformedResponseError{Endpoint: endpoint, Err: err}
	}
	return nil
}

func responseDetail(reader io.Reader) string {
	content, err := io.ReadAll(io.LimitReader(reader, 64*1024))
	if err != nil {
		return ""
	}
	var payload struct {
		Detail any `json:"detail"`
	}
	if err := json.Unmarshal(content, &payload); err == nil {
		switch detail := payload.Detail.(type) {
		case string:
			return detail
		case map[string]any:
			if message, ok := detail["message"].(string); ok {
				return message
			}
		}
	}
	return strings.TrimSpace(string(content))
}
