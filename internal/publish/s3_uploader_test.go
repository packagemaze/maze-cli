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

func TestS3MultipartUploaderUsesPreparedUploadWithoutCreateOrAbort(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, io.Discard, UploadOptions{UsePreparedUpload: true})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.UploadID != "server-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
}

func TestS3MultipartUploaderStartsUnpreparedUpload(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "ignored-without-capability"
	recorder := &s3RequestRecorder{}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, io.Discard, UploadOptions{})
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.UploadID != "client-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "create,upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
}

func s3UploadArtifact(t *testing.T, path string) PlannedArtifact {
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
	artifact.Upload.Kind = "s3_multipart_upload_v1"
	artifact.Upload.PartSizeBytes = minMultipartPartSizeBytes
	artifact.Upload.Target.Bucket = "packagemaze-artifacts"
	artifact.Upload.Target.Endpoint = "https://account.r2.cloudflarestorage.com"
	artifact.Upload.Target.ObjectKey = "uploads/package-1.0.0.tgz"
	artifact.Upload.Target.Region = "auto"
	artifact.Upload.Target.Credentials.AccessKeyID = "access-key"
	artifact.Upload.Target.Credentials.SecretAccessKey = "secret-key"
	artifact.Upload.Target.Credentials.SessionToken = "session-token"
	return artifact
}

type s3RequestRecorder struct {
	operations []string
}

func (r *s3RequestRecorder) Do(request *http.Request) (*http.Response, error) {
	operation := ""
	body := ""
	headers := make(http.Header)
	switch {
	case request.Method == http.MethodPost && request.URL.Query().Has("uploads"):
		operation = "create"
		body = `<CreateMultipartUploadResult><UploadId>client-upload-123</UploadId></CreateMultipartUploadResult>`
	case request.Method == http.MethodPut && request.URL.Query().Has("partNumber"):
		operation = "upload-part"
		headers.Set("ETag", `"part-etag"`)
	case request.Method == http.MethodPost && request.URL.Query().Has("uploadId"):
		operation = "complete"
		body = `<CompleteMultipartUploadResult><ETag>"complete-etag"</ETag></CompleteMultipartUploadResult>`
	case request.Method == http.MethodDelete:
		operation = "abort"
	default:
		return nil, fmt.Errorf("unexpected S3 request: %s %s", request.Method, request.URL.String())
	}
	r.operations = append(r.operations, operation)
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     headers,
		Request:    request,
		StatusCode: http.StatusOK,
	}, nil
}
