package publish

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestR2MultipartUploaderUsesServerCreatedUploadWithoutCreateOrAbort(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := r2UploadArtifact(t, path)
	artifact.Upload.R2UploadID = "server-upload-123"
	recorder := &r2RequestRecorder{}
	uploader := &R2MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, io.Discard, UploadOptions{ServerCreated: true})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.R2UploadID != "server-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
}

func TestR2MultipartUploaderPreservesLegacyCreateFlow(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := r2UploadArtifact(t, path)
	artifact.Upload.R2UploadID = "ignored-without-capability"
	recorder := &r2RequestRecorder{}
	uploader := &R2MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, io.Discard, UploadOptions{})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.R2UploadID != "legacy-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "create,upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
}

func r2UploadArtifact(t *testing.T, path string) PlannedArtifact {
	t.Helper()
	var artifact PlannedArtifact
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat Artifact: %v", err)
	}
	artifact.Artifact = ArtifactFact{
		ContentType: "application/octet-stream",
		Filename:    info.Name(),
		SHA256:      strings.Repeat("a", 64),
		SizeBytes:   info.Size(),
	}
	artifact.Upload.Kind = "r2_multipart_upload_v1"
	artifact.Upload.PartSizeBytes = minR2PartSizeBytes
	artifact.Upload.Target.Bucket = "packagemaze-artifacts"
	artifact.Upload.Target.Endpoint = "https://account.r2.cloudflarestorage.com"
	artifact.Upload.Target.ObjectKey = "uploads/package-1.0.0.tgz"
	artifact.Upload.Target.Region = "auto"
	artifact.Upload.Target.Credentials.AccessKeyID = "access-key"
	artifact.Upload.Target.Credentials.SecretAccessKey = "secret-key"
	artifact.Upload.Target.Credentials.SessionToken = "session-token"
	return artifact
}

type r2RequestRecorder struct {
	operations []string
}

func (r *r2RequestRecorder) Do(request *http.Request) (*http.Response, error) {
	operation := ""
	body := ""
	headers := make(http.Header)
	switch {
	case request.Method == http.MethodPost && request.URL.Query().Has("uploads"):
		operation = "create"
		body = `<CreateMultipartUploadResult><UploadId>legacy-upload-123</UploadId></CreateMultipartUploadResult>`
	case request.Method == http.MethodPut && request.URL.Query().Has("partNumber"):
		operation = "upload-part"
		headers.Set("ETag", `"part-etag"`)
	case request.Method == http.MethodPost && request.URL.Query().Has("uploadId"):
		operation = "complete"
		body = `<CompleteMultipartUploadResult><ETag>"complete-etag"</ETag></CompleteMultipartUploadResult>`
	case request.Method == http.MethodDelete:
		operation = "abort"
	default:
		return nil, fmt.Errorf("unexpected R2 request: %s %s", request.Method, request.URL.String())
	}
	r.operations = append(r.operations, operation)
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     headers,
		Request:    request,
		StatusCode: http.StatusOK,
	}, nil
}
