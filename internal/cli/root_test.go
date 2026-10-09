package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/packagemaze/maze-cli/internal/api"
	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/doctor"
	publishcmd "github.com/packagemaze/maze-cli/internal/publish"
)

func TestExchangeOIDCCommandTokenOutput(t *testing.T) {
	exchanger := &recordingExchanger{}
	stdout, stderr, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: exchanger,
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
	)
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	if stdout != "maze_ci_real\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !exchanger.called {
		t.Fatalf("backend was not called")
	}
	if exchanger.request.OIDCToken != "manual-oidc" {
		t.Fatalf("oidc_token was not forwarded")
	}
}

func TestExchangeOIDCCommandForwardsExplicitLegacyClientContext(t *testing.T) {
	exchanger := &recordingExchanger{}
	_, stderr, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: exchanger,
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--client-context-json", `{"ci":{"branch":"main","sha":"abcdef123456"}}`,
	)
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	ciContext, ok := exchanger.request.Client["ci"].(map[string]any)
	if !ok {
		t.Fatalf("client ci context = %#v", exchanger.request.Client["ci"])
	}
	if ciContext["branch"] != "main" || ciContext["sha"] != "abcdef123456" {
		t.Fatalf("client context = %#v", exchanger.request.Client)
	}
}

func TestExchangeOIDCCommandForwardsSetupInvocationID(t *testing.T) {
	exchanger := &recordingExchanger{}
	_, stderr, err := runCommandWithDeps(
		auth.Dependencies{
			Env: mapLookup(map[string]string{
				"MAZE_OIDC_TOKEN":          "manual-oidc",
				"MAZE_SETUP_INVOCATION_ID": "setup-maze_environment-id",
			}),
			Exchanger: exchanger,
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--setup-invocation-id", "setup-maze_flag-id",
	)
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	if exchanger.request.SetupInvocationID != "setup-maze_flag-id" {
		t.Fatalf("setup invocation id = %q", exchanger.request.SetupInvocationID)
	}
}

func TestExchangeOIDCCommandJSONAlias(t *testing.T) {
	stdout, _, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: &recordingExchanger{},
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--json",
	)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout)
	}
	if payload["provider"] != "manual" {
		t.Fatalf("provider = %v", payload["provider"])
	}
	if payload["token"] != "maze_ci_real" {
		t.Fatalf("token = %v", payload["token"])
	}
	if payload["build_number"] != float64(482) || payload["build_url"] != "https://www.packagemaze.com/your-org/builds/482" {
		t.Fatalf("Build reference = %#v", payload)
	}
}

func TestExchangeOIDCCommandShellOutput(t *testing.T) {
	stdout, _, err := runCommandWithDeps(
		auth.Dependencies{
			Env:       mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "manual-oidc"}),
			Exchanger: &recordingExchanger{},
		},
		"auth", "exchange-oidc",
		"--provider", "manual",
		"--feed", "your-org/npm",
		"--purpose", "publish",
		"--package", "your-package",
		"--format", "shell",
	)
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !strings.Contains(stdout, "export MAZE_TOKEN='maze_ci_real'") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "export MAZE_TOKEN_EXPIRES_AT='2026-06-08T13:30:00Z'") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestExchangeOIDCJSONAliasConflictsWithFormat(t *testing.T) {
	_, _, err := runCommand(
		"auth", "exchange-oidc",
		"--feed", "your-org/npm",
		"--purpose", "install",
		"--json",
		"--format", "shell",
	)
	if err == nil || !strings.Contains(err.Error(), "--json cannot be combined") {
		t.Fatalf("expected --json conflict, got %v", err)
	}
}

func TestPublishCommandJSON(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "large-package-1.0.0.tgz")
	if err := os.WriteFile(artifactPath, []byte("artifact bytes"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	client := &recordingPublishClient{}
	client.createResponse = publishCreateResponse(artifactPath)
	client.statusResponse = publishStatusResponse(client.createResponse)
	uploader := &recordingUploader{
		result: publishcmd.UploadResult{PartCount: 1, UploadID: "upload-1"},
	}
	stdout, stderr, err := runCommandWithPublishDeps(
		auth.Dependencies{},
		publishcmd.Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{"MAZE_TOKEN": "pm_publish_token"}),
			Sleep:    func(context.Context, time.Duration) error { return nil },
			Uploader: uploader,
		},
		"publish",
		artifactPath,
		"--feed", "your-org/npm",
		"--json",
	)
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout)
	}
	if payload["publish_session_id"] != "pubsession_cli" {
		t.Fatalf("publish_session_id = %v", payload["publish_session_id"])
	}
	if client.createToken != "pm_publish_token" {
		t.Fatalf("token = %q", client.createToken)
	}
	if uploader.path != artifactPath {
		t.Fatalf("uploaded path = %q", uploader.path)
	}
	if strings.Contains(stdout+stderr, "pm_publish_token") {
		t.Fatalf("token leaked into output")
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := runCommand("version")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !strings.HasPrefix(stdout, "maze ") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func runCommand(args ...string) (string, string, error) {
	return runCommandWithDeps(auth.Dependencies{Env: mapLookup(nil)}, args...)
}

func runCommandWithDeps(deps auth.Dependencies, args ...string) (string, string, error) {
	return runCommandWithPublishDeps(deps, publishcmd.Dependencies{}, args...)
}

func runCommandWithPublishDeps(deps auth.Dependencies, publishDeps publishcmd.Dependencies, args ...string) (string, string, error) {
	return runRootCommand(deps, publishDeps, doctor.Dependencies{}, args...)
}

func runCommandWithDoctorDeps(doctorDeps doctor.Dependencies, args ...string) (string, string, error) {
	return runRootCommand(auth.Dependencies{Env: doctorDeps.Env}, publishcmd.Dependencies{}, doctorDeps, args...)
}

func runRootCommand(deps auth.Dependencies, publishDeps publishcmd.Dependencies, doctorDeps doctor.Dependencies, args ...string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command := NewRootCommandWithDependencies(deps, publishDeps, doctorDeps)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

type recordingExchanger struct {
	called  bool
	request api.CITokenRequest
}

func (f *recordingExchanger) ExchangeCI(_ context.Context, request api.CITokenRequest) (api.CITokenResponse, error) {
	f.called = true
	f.request = request
	return api.CITokenResponse{
		Token:           "maze_ci_real",
		ExpiresAt:       time.Date(2026, 6, 8, 13, 30, 0, 0, time.UTC),
		TokenType:       "Bearer",
		Feed:            request.Feed,
		ExchangePurpose: request.Purpose,
		BuildNumber:     482,
		BuildURL:        "https://www.packagemaze.com/your-org/builds/482",
		Scopes:          []string{"read"},
	}, nil
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

type recordingPublishClient struct {
	createResponse publishcmd.CreatePublishSessionResponse
	createToken    string
	statusResponse publishcmd.PublishSessionStatusResponse
}

func (r *recordingPublishClient) CreateSession(_ context.Context, _ string, token string, _ publishcmd.CreatePublishSessionRequest) (publishcmd.CreatePublishSessionResponse, error) {
	r.createToken = token
	return r.createResponse, nil
}

func (r *recordingPublishClient) CompleteUpload(context.Context, string, publishcmd.CompletionInstruction, publishcmd.UploadResult) error {
	return nil
}

func (r *recordingPublishClient) GetStatus(context.Context, string, string) (publishcmd.PublishSessionStatusResponse, error) {
	return r.statusResponse, nil
}

type recordingUploader struct {
	path   string
	result publishcmd.UploadResult
}

func (r *recordingUploader) Upload(_ context.Context, _ publishcmd.PlannedArtifact, path string, _ publishcmd.UploadOptions, _ io.Writer) (publishcmd.UploadResult, error) {
	r.path = path
	return r.result, nil
}

func publishCreateResponse(path string) publishcmd.CreatePublishSessionResponse {
	var response publishcmd.CreatePublishSessionResponse
	response.SchemaVersion = 1
	response.PublishSession.ID = "pubsession_cli"
	response.PublishSession.State = "planned"
	response.PublishSession.ArtifactProtocol = "npm"
	response.Plan.Kind = "package_publish_plan"
	response.Plan.SchemaVersion = 1
	response.Plan.Capabilities = []string{
		"s3_multipart_upload_v1",
		"s3_multipart_completion_v1",
		"publish_session_status_v1",
		"prepared_s3_multipart_upload_v1",
	}
	response.Plan.Wait.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/publish-sessions/pubsession_cli"
	response.Plan.Wait.IntervalSeconds = 1
	response.Plan.Wait.TimeoutSeconds = 30
	var artifact publishcmd.PlannedArtifact
	artifact.Artifact.Filename = filepath.Base(path)
	artifact.Artifact.SizeBytes = int64(len("artifact bytes"))
	artifact.Artifact.SHA256 = strings.Repeat("a", 64)
	artifact.Artifact.ContentType = "application/octet-stream"
	artifact.PublicationAttemptID = "attempt_cli"
	artifact.Completion.Method = "POST"
	artifact.Completion.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/publish-sessions/pubsession_cli/artifacts/attempt_cli/complete"
	artifact.Package.Name = "large-package"
	artifact.Package.Version = "1.0.0"
	artifact.Upload.Kind = "s3_multipart_upload_v1"
	artifact.Upload.PartSizeBytes = 5 * 1024 * 1024
	artifact.Upload.Target.Bucket = "packagemaze-artifacts"
	artifact.Upload.Target.Credentials.AccessKeyID = "r2-temp-access-key"
	artifact.Upload.Target.Credentials.SecretAccessKey = "r2-temp-secret-key"
	artifact.Upload.Target.Credentials.SessionToken = "r2-temp-session-token"
	artifact.Upload.Target.Endpoint = "https://example.r2.cloudflarestorage.com"
	artifact.Upload.Target.ObjectKey = "uploads/object"
	artifact.Upload.Target.Region = "auto"
	artifact.Upload.UploadSessionID = "uploadsession_cli"
	artifact.Upload.UploadID = "prepared-upload-cli"
	response.Plan.Artifacts = []publishcmd.PlannedArtifact{artifact}
	return response
}

func publishStatusResponse(create publishcmd.CreatePublishSessionResponse) publishcmd.PublishSessionStatusResponse {
	var response publishcmd.PublishSessionStatusResponse
	response.SchemaVersion = 1
	response.PublishSession.ID = create.PublishSession.ID
	response.PublishSession.State = "ready"
	for _, planned := range create.Plan.Artifacts {
		status := publishcmd.ArtifactStatus{
			Artifact: planned.Artifact,
			State:    "ready",
		}
		status.Package.Name = planned.Package.Name
		status.Package.Version = planned.Package.Version
		status.UploadSession.ID = planned.Upload.UploadSessionID
		status.UploadSession.State = "ready"
		response.Artifacts = append(response.Artifacts, status)
	}
	return response
}

func TestDoctorCommandWritesMarkdownAndExitsNonZeroOnFailingChecks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("--index-url https://pkg.packagemaze.com/acme/pypi/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prober := &recordingProber{result: doctor.ProbeResult{Outcome: doctor.ProbeTokenRejected, StatusCode: 401}}
	stdout, stderr, err := runCommandWithDoctorDeps(
		doctor.Dependencies{Env: mapLookup(map[string]string{"MAZE_TOKEN": "pm_cli_secret"}), Prober: prober},
		"doctor", "--dir", dir,
	)
	if !errors.Is(err, doctor.ErrChecksFailed) {
		t.Fatalf("err = %v\nstdout: %s", err, stdout)
	}
	if !strings.HasPrefix(stdout, "# maze doctor\n") || !strings.Contains(stdout, "`client_config_wrong_registry`") || !strings.Contains(stdout, "`read_token_rejected`") {
		t.Fatalf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "pm_cli_secret") || strings.Contains(stderr, "pm_cli_secret") || strings.Contains(err.Error(), "pm_cli_secret") {
		t.Fatalf("token leaked: %s %s %v", stdout, stderr, err)
	}
	if len(prober.requests) != 1 || prober.requests[0].Protocol != doctor.ProtocolPyPI || prober.requests[0].Token != "pm_cli_secret" {
		t.Fatalf("requests = %#v", prober.requests)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDoctorCommandJSONAliasAndCleanExitWhenHealthy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("registry=https://pkg.packagemaze.com/acme/npm/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prober := &recordingProber{result: doctor.ProbeResult{Outcome: doctor.ProbeAccepted, StatusCode: 200, Username: "kko"}}
	stdout, stderr, err := runCommandWithDoctorDeps(
		doctor.Dependencies{Env: mapLookup(map[string]string{"MAZE_TOKEN": "pm_cli_secret"}), Prober: prober},
		"doctor", "--dir", dir, "--json",
	)
	if err != nil {
		t.Fatalf("err = %v\nstderr: %s", err, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout was not JSON: %v\n%s", err, stdout)
	}
	if payload["status"] != "pass" {
		t.Fatalf("status = %v", payload["status"])
	}
	if strings.Contains(stdout, "pm_cli_secret") {
		t.Fatalf("token leaked: %s", stdout)
	}
}

func TestDoctorCommandRejectsUnknownFormats(t *testing.T) {
	_, _, err := runCommandWithDoctorDeps(doctor.Dependencies{Env: mapLookup(nil)}, "doctor", "--dir", t.TempDir(), "--format", "yaml")
	if err == nil || !strings.Contains(err.Error(), "format must be markdown or json") {
		t.Fatalf("err = %v", err)
	}
}

type recordingProber struct {
	requests []doctor.ProbeRequest
	result   doctor.ProbeResult
}

func (r *recordingProber) Probe(_ context.Context, request doctor.ProbeRequest) doctor.ProbeResult {
	r.requests = append(r.requests, request)
	return r.result
}
