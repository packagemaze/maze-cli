package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunExecutesBackendPlanAndWaits(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	client.statusResponses = []PublishSessionStatusResponse{
		statusResponse("pubsession_123", "processing", client.createResponse.Plan.Artifacts),
		statusResponse("pubsession_123", "ready", client.createResponse.Plan.Artifacts),
	}
	uploader := &fakeUploader{
		result: UploadResult{PartCount: 2, R2UploadID: "r2-upload-123"},
	}
	var stderr bytes.Buffer

	result, resolved, err := Run(
		context.Background(),
		Config{
			Feed:        "your-org/npm",
			PackageHint: "@your-org/large-package",
			TokenEnv:    DefaultTokenEnv,
			VersionHint: "1.0.0",
			Wait:        true,
		},
		[]string{path},
		Dependencies{
			Client: client,
			Env:    mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			PublicationRequestID: func() (string, error) {
				return "publication_request_test", nil
			},
			Sleep:    func(context.Context, time.Duration) error { return nil },
			Uploader: uploader,
		},
		&stderr,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if resolved.Token != "pm_publish_token" {
		t.Fatalf("resolved token = %q", resolved.Token)
	}
	if client.createToken != "pm_publish_token" {
		t.Fatalf("create token = %q", client.createToken)
	}
	if client.createFeed != "your-org/npm" {
		t.Fatalf("feed = %q", client.createFeed)
	}
	if got := client.createRequest.Hints["package_name"]; got != "@your-org/large-package" {
		t.Fatalf("package hint = %q", got)
	}
	if got := client.createRequest.Hints["package_version"]; got != "1.0.0" {
		t.Fatalf("version hint = %q", got)
	}
	if len(client.createRequest.Artifacts) != 1 {
		t.Fatalf("artifact facts = %#v", client.createRequest.Artifacts)
	}
	if client.createRequest.Artifacts[0].SHA256 != sha256Hex("artifact bytes") {
		t.Fatalf("sha256 = %q", client.createRequest.Artifacts[0].SHA256)
	}
	if !contains(client.createRequest.Client.Capabilities, "r2_multipart_upload_v1") {
		t.Fatalf("client capabilities = %#v", client.createRequest.Client.Capabilities)
	}
	if !contains(client.createRequest.Client.Capabilities, "server_created_r2_multipart_upload_v1") {
		t.Fatalf("client capabilities = %#v", client.createRequest.Client.Capabilities)
	}
	if client.createRequest.PublicationRequestID != "publication_request_test" {
		t.Fatalf("publication request id = %q", client.createRequest.PublicationRequestID)
	}
	if uploader.path != path {
		t.Fatalf("uploaded path = %q", uploader.path)
	}
	if len(client.completed) != 1 {
		t.Fatalf("completed uploads = %#v", client.completed)
	}
	if client.completed[0].result.R2UploadID != "r2-upload-123" {
		t.Fatalf("completion upload id = %q", client.completed[0].result.R2UploadID)
	}
	if client.statusCalls != 2 {
		t.Fatalf("status calls = %d", client.statusCalls)
	}
	if result.PublishSessionID != "pubsession_123" || result.State != "ready" {
		t.Fatalf("result = %#v", result)
	}
	if result.Artifacts[0].PackageName != "@your-org/large-package" {
		t.Fatalf("package name = %q", result.Artifacts[0].PackageName)
	}
	if strings.Contains(stderr.String(), "pm_publish_token") {
		t.Fatalf("token leaked to stderr: %s", stderr.String())
	}
}

func TestRunUsesServerCreatedMultipartUploadAndReportsAdoption(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "plan_123", []ArtifactFact{factForPath(t, path)})
	client.createResponse.PublishSession.Resumed = true
	client.createResponse.Plan.Capabilities = append(client.createResponse.Plan.Capabilities, "server_created_r2_multipart_upload_v1")
	artifact := &client.createResponse.Plan.Artifacts[0]
	artifact.PublicationAttemptID = "attempt_123"
	artifact.Upload.R2UploadID = "r2-upload-server-created"
	artifact.Completion.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/publish-sessions/plan_123/artifacts/attempt_123/complete"
	client.statusResponses = []PublishSessionStatusResponse{
		statusResponse("plan_123", "ready", client.createResponse.Plan.Artifacts),
	}
	uploader := &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-server-created"}}

	result, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client: client,
			Env:    mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			PublicationRequestID: func() (string, error) {
				return "publication_request_retry", nil
			},
			Sleep:    func(context.Context, time.Duration) error { return nil },
			Uploader: uploader,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if uploader.artifact.Upload.R2UploadID != "r2-upload-server-created" {
		t.Fatalf("uploader plan = %#v", uploader.artifact.Upload)
	}
	if !uploader.options.ServerCreated {
		t.Fatalf("uploader options = %#v", uploader.options)
	}
	if !result.Resumed {
		t.Fatalf("result did not report adoption: %#v", result)
	}
}

func TestRunRejectsServerCreatedPlanWithoutUploadID(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "plan_123", []ArtifactFact{factForPath(t, path)})
	client.createResponse.Plan.Capabilities = append(client.createResponse.Plan.Capabilities, "server_created_r2_multipart_upload_v1")
	artifact := &client.createResponse.Plan.Artifacts[0]
	artifact.PublicationAttemptID = "attempt_123"
	artifact.Completion.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/publish-sessions/plan_123/artifacts/attempt_123/complete"
	uploader := &fakeUploader{}

	_, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client: client,
			Env:    mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			PublicationRequestID: func() (string, error) {
				return "publication_request_invalid", nil
			},
			Uploader: uploader,
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "server-created upload plan is incomplete") {
		t.Fatalf("expected incomplete plan error, got %v", err)
	}
	if uploader.path != "" {
		t.Fatalf("uploader ran for invalid plan: %q", uploader.path)
	}
}

func TestHTTPClientAcceptsAdoptedCreateResponse(t *testing.T) {
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		bodies = append(bodies, body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"schema_version":1,"publish_session":{"id":"plan_123","resumed":true},"plan":{"schema_version":1}}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, true, server.Client())
	request := CreatePublishSessionRequest{PublicationRequestID: "publication_request_retry"}

	response, err := client.CreateSession(context.Background(), "your-org/npm", "secret", request)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("request bodies = %q", bodies)
	}
	if !response.PublishSession.Resumed {
		t.Fatalf("adopted response = %#v", response)
	}
}

func TestHTTPClientDoesNotRetryCreateBeforeServerFlowIsKnown(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(writer, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewClient(server.URL, true, server.Client())

	_, err := client.CreateSession(
		context.Background(),
		"your-org/npm",
		"secret",
		CreatePublishSessionRequest{PublicationRequestID: "publication_request_retry"},
	)
	if err == nil {
		t.Fatal("CreateSession unexpectedly succeeded")
	}
	if requests != 1 {
		t.Fatalf("create requests = %d", requests)
	}
}

func TestRunReturnsBackendErrorStatusWithResult(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	failed := statusResponse("pubsession_123", "error", client.createResponse.Plan.Artifacts)
	failed.Artifacts[0].State = "error"
	failed.Artifacts[0].Error = &Diagnostic{
		Code:    "package_version_already_published",
		Message: "Package publish conflicts with an immutable private package.",
		Phase:   "product_state_finalization",
	}
	client.statusResponses = []PublishSessionStatusResponse{failed}

	result, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			Sleep:    func(context.Context, time.Duration) error { return nil },
			Uploader: &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-123"}},
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "immutable private package") {
		t.Fatalf("expected publish error, got %v", err)
	}
	if result.State != "error" {
		t.Fatalf("result state = %q", result.State)
	}
	if result.Artifacts[0].DiagnosticCode != "package_version_already_published" {
		t.Fatalf("diagnostic = %#v", result.Artifacts[0])
	}
}

func TestRunRejectsPlanCompletionURLOutsidePackageMazeOrigin(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	client.createResponse.Plan.Artifacts[0].Completion.URL = "https://attacker.example/upload-sessions/uploadsession_123/complete"
	uploader := &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-123"}}

	_, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			Uploader: uploader,
		},
		nil,
	)

	if err == nil || !strings.Contains(err.Error(), "configured PackageMaze origin") {
		t.Fatalf("expected origin rejection, got %v", err)
	}
	if uploader.path != "" {
		t.Fatalf("uploader was called for rejected plan: %q", uploader.path)
	}
	if len(client.completed) != 0 {
		t.Fatalf("completion sent for rejected plan: %#v", client.completed)
	}
}

func TestRunRejectsPlanStatusURLOutsidePackageMazeControlRoute(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	client.createResponse.Plan.Wait.URL = "https://pkg.packagemaze.com/your-org/npm/-/other/v1/publish-sessions/pubsession_123"

	_, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			Uploader: &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-123"}},
		},
		nil,
	)

	if err == nil || !strings.Contains(err.Error(), "expected PackageMaze control route") {
		t.Fatalf("expected control route rejection, got %v", err)
	}
}

func TestRunRejectsUnsafeR2UploadPlan(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	client.createResponse.Plan.Artifacts[0].Upload.Target.Endpoint = "https://storage.attacker.example"

	_, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			Uploader: &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-123"}},
		},
		nil,
	)

	if err == nil || !strings.Contains(err.Error(), "Cloudflare R2 S3 endpoint") {
		t.Fatalf("expected R2 endpoint rejection, got %v", err)
	}
}

func TestRunRejectsOversizedR2PartSize(t *testing.T) {
	path := writeTempArtifact(t, "large-package-1.0.0.tgz", "artifact bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "pubsession_123", []ArtifactFact{
		factForPath(t, path),
	})
	client.createResponse.Plan.Artifacts[0].Upload.PartSizeBytes = maxR2PartSizeBytes + 1

	_, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{path},
		Dependencies{
			Client:   client,
			Env:      mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			Uploader: &fakeUploader{result: UploadResult{PartCount: 1, R2UploadID: "r2-upload-123"}},
		},
		nil,
	)

	if err == nil || !strings.Contains(err.Error(), "part size") {
		t.Fatalf("expected R2 part size rejection, got %v", err)
	}
}

func TestWriteJSONOmitsSecrets(t *testing.T) {
	result := Result{
		ArtifactProtocol: "npm",
		PublishSessionID: "pubsession_123",
		Resumed:          true,
		State:            "ready",
		Artifacts: []ArtifactResult{{
			Filename:       "large-package-1.0.0.tgz",
			PackageName:    "large-package",
			PackageVersion: "1.0.0",
			State:          "ready",
		}},
	}
	var stdout bytes.Buffer
	if err := Write(result, FormatJSON, &stdout); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON output invalid: %v", err)
	}
	if payload["publish_session_id"] != "pubsession_123" {
		t.Fatalf("publish_session_id = %v", payload["publish_session_id"])
	}
	if payload["resumed"] != true {
		t.Fatalf("resumed = %v", payload["resumed"])
	}
	if strings.Contains(stdout.String(), "secret") || strings.Contains(stdout.String(), "access_key") {
		t.Fatalf("secret material leaked: %s", stdout.String())
	}
	var text bytes.Buffer
	if err := Write(result, FormatText, &text); err != nil {
		t.Fatalf("Write text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "pubsession_123 ready resumed") {
		t.Fatalf("resumed text output = %q", text.String())
	}
}

func createResponseForFacts(t *testing.T, sessionID string, facts []ArtifactFact) CreatePublishSessionResponse {
	t.Helper()
	var response CreatePublishSessionResponse
	response.SchemaVersion = 1
	response.PublishSession.ID = sessionID
	response.PublishSession.State = "planned"
	response.PublishSession.ArtifactProtocol = "npm"
	response.Plan.Kind = "package_publish_plan"
	response.Plan.SchemaVersion = 1
	response.Plan.Wait.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/publish-sessions/" + sessionID
	response.Plan.Wait.IntervalSeconds = 1
	response.Plan.Wait.TimeoutSeconds = 30
	for index, fact := range facts {
		var artifact PlannedArtifact
		artifact.Artifact = fact
		artifact.ArtifactID = "artifact_123"
		artifact.Completion.Method = "POST"
		artifact.Completion.URL = "https://pkg.packagemaze.com/your-org/npm/-/packagemaze/v1/upload-sessions/uploadsession_123/complete"
		artifact.Package.Name = firstNonEmpty("@your-org/large-package", "large-package")
		artifact.Package.Version = "1.0.0"
		artifact.Upload.Kind = "r2_multipart_upload_v1"
		artifact.Upload.PartSizeBytes = 5 * 1024 * 1024
		artifact.Upload.Target.Bucket = "packagemaze-artifacts"
		artifact.Upload.Target.Endpoint = "https://example.r2.cloudflarestorage.com"
		artifact.Upload.Target.Credentials.AccessKeyID = "r2-temp-access-key"
		artifact.Upload.Target.Credentials.SecretAccessKey = "r2-temp-secret-key"
		artifact.Upload.Target.Credentials.SessionToken = "r2-temp-session-token"
		artifact.Upload.Target.ObjectKey = "uploads/object"
		artifact.Upload.Target.Region = "auto"
		artifact.Upload.UploadSessionID = "uploadsession_123"
		if index > 0 {
			artifact.ArtifactID = artifact.ArtifactID + string(rune('a'+index))
		}
		response.Plan.Artifacts = append(response.Plan.Artifacts, artifact)
	}
	return response
}

func statusResponse(sessionID string, state string, plan []PlannedArtifact) PublishSessionStatusResponse {
	var response PublishSessionStatusResponse
	response.SchemaVersion = 1
	response.PublishSession.ID = sessionID
	response.PublishSession.State = state
	for _, artifact := range plan {
		status := ArtifactStatus{
			Artifact: artifact.Artifact,
			State:    state,
		}
		status.Package.Name = artifact.Package.Name
		status.Package.Version = artifact.Package.Version
		status.UploadSession.ID = artifact.Upload.UploadSessionID
		status.UploadSession.State = state
		if state == "ready" {
			status.PublishJob = &struct {
				ID    string `json:"id"`
				State string `json:"state"`
			}{ID: "pubjob_123", State: "ready"}
		}
		response.Artifacts = append(response.Artifacts, status)
	}
	return response
}

func factForPath(t *testing.T, path string) ArtifactFact {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp artifact: %v", err)
	}
	return ArtifactFact{
		ContentType: "application/x-compressed-tar",
		Filename:    filepath.Base(path),
		SHA256:      sha256Hex(string(content)),
		SizeBytes:   int64(len(content)),
	}
}

func writeTempArtifact(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp artifact: %v", err)
	}
	return path
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type fakeClient struct {
	completed       []completionCall
	createFeed      string
	createRequest   CreatePublishSessionRequest
	createResponse  CreatePublishSessionResponse
	createToken     string
	statusCalls     int
	statusResponses []PublishSessionStatusResponse
}

type completionCall struct {
	completion CompletionInstruction
	result     UploadResult
	token      string
}

func (f *fakeClient) CreateSession(_ context.Context, feed string, token string, request CreatePublishSessionRequest) (CreatePublishSessionResponse, error) {
	f.createFeed = feed
	f.createToken = token
	f.createRequest = request
	return f.createResponse, nil
}

func (f *fakeClient) CompleteUpload(_ context.Context, token string, completion CompletionInstruction, result UploadResult) error {
	f.completed = append(f.completed, completionCall{completion: completion, result: result, token: token})
	return nil
}

func (f *fakeClient) GetStatus(context.Context, string, string) (PublishSessionStatusResponse, error) {
	index := f.statusCalls
	f.statusCalls++
	if index >= len(f.statusResponses) {
		return f.statusResponses[len(f.statusResponses)-1], nil
	}
	return f.statusResponses[index], nil
}

type fakeUploader struct {
	artifact PlannedArtifact
	options  UploadOptions
	path     string
	result   UploadResult
}

func (f *fakeUploader) Upload(_ context.Context, artifact PlannedArtifact, path string, _ io.Writer, options UploadOptions) (UploadResult, error) {
	f.artifact = artifact
	f.options = options
	f.path = path
	return f.result, nil
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
