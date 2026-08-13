package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestS3MultipartUploaderUsesPreparedUploadWithoutCreateOrAbort(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{}, io.Discard)
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

func TestS3MultipartUploaderReusesAuthoritativeTransferredParts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package-1.0.0.tgz")
	content := bytes.Repeat([]byte("a"), minMultipartPartSizeBytes+17)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write Artifact: %v", err)
	}
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{listedParts: map[int32]int64{1: minMultipartPartSizeBytes}}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{Resume: true}, io.Discard)
	if err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if result.PartCount != 2 || result.UploadID != "server-upload-123" {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "list-parts,upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
	if got := fmt.Sprint(recorder.uploadPartNumbers); got != "[2]" {
		t.Fatalf("uploaded parts = %s", got)
	}
}

func TestS3MultipartUploaderReplacesAListedPartWithTheWrongSize(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{listedParts: map[int32]int64{1: 1}}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	if _, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{Resume: true}, io.Discard); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if got := fmt.Sprint(recorder.uploadPartNumbers); got != "[1]" {
		t.Fatalf("uploaded parts = %s", got)
	}
}

func TestS3MultipartUploaderPaginatesTransferredPartsAndCompletesInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "package-1.0.0.tgz")
	content := bytes.Repeat([]byte("a"), 2*minMultipartPartSizeBytes+17)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write Artifact: %v", err)
	}
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{listResponses: []string{
		`<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>1</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>"part-etag-1"</ETag><Size>5242880</Size></Part></ListPartsResult>`,
		`<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>3</PartNumber><ETag>"part-etag-3"</ETag><Size>17</Size></Part></ListPartsResult>`,
	}}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	if _, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{Resume: true}, io.Discard); err != nil {
		t.Fatalf("Upload returned error: %v", err)
	}
	if got := strings.Join(recorder.operations, ","); got != "list-parts,list-parts,upload-part,complete" {
		t.Fatalf("operations = %s", got)
	}
	if got := fmt.Sprint(recorder.uploadPartNumbers); got != "[2]" {
		t.Fatalf("uploaded parts = %s", got)
	}
	for _, fragment := range []string{"part-etag-1", "part-etag-new-2", "part-etag-3"} {
		if !strings.Contains(recorder.completeBody, fragment) {
			t.Fatalf("completion body omitted %q: %s", fragment, recorder.completeBody)
		}
	}
	if strings.Index(recorder.completeBody, "part-etag-1") > strings.Index(recorder.completeBody, "part-etag-new-2") ||
		strings.Index(recorder.completeBody, "part-etag-new-2") > strings.Index(recorder.completeBody, "part-etag-3") {
		t.Fatalf("completion parts were not ordered: %s", recorder.completeBody)
	}
}

func TestS3MultipartUploaderRejectsMalformedTransferredPartEvidence(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	for name, response := range map[string]string{
		"duplicate":    `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><ETag>"one"</ETag><Size>14</Size></Part><Part><PartNumber>1</PartNumber><ETag>"two"</ETag><Size>14</Size></Part></ListPartsResult>`,
		"missing etag": `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>1</PartNumber><Size>14</Size></Part></ListPartsResult>`,
		"out of range": `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>2</PartNumber><ETag>"two"</ETag><Size>14</Size></Part></ListPartsResult>`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &s3RequestRecorder{listResponses: []string{response}}
			uploader := &S3MultipartUploader{HTTPClient: recorder}

			_, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{Resume: true}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "inspect prior Artifact transfer progress") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "s3") || strings.Contains(strings.ToLower(err.Error()), "r2") {
				t.Fatalf("provider detail leaked: %v", err)
			}
			if got := strings.Join(recorder.operations, ","); got != "list-parts" {
				t.Fatalf("operations = %s", got)
			}
		})
	}
}

func TestS3MultipartUploaderTreatsAnAbsentResumedUploadAsCompletionUncertain(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{
		listResponses: []string{`<Error><Code>NoSuchUpload</Code><Message>The specified upload does not exist.</Message></Error>`},
		listStatuses:  []int{http.StatusNotFound},
	}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{Resume: true}, io.Discard)
	var uncertain *artifactTransferCompletionUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected completion uncertainty, got %v", err)
	}
	if result.UploadID != "server-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(recorder.operations, ","); got != "list-parts" {
		t.Fatalf("operations = %s", got)
	}
}

func TestS3MultipartUploaderRejectsPlanWithoutPreparedUpload(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = ""
	recorder := &s3RequestRecorder{}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	_, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "transfer plan was incomplete") {
		t.Fatalf("expected incomplete plan error, got %v", err)
	}
	if len(recorder.operations) != 0 {
		t.Fatalf("operations = %#v", recorder.operations)
	}
}

func TestS3MultipartUploaderPreservesIdentityWhenCompletionIsUncertain(t *testing.T) {
	path := writeTempArtifact(t, "package-1.0.0.tgz", "artifact bytes")
	artifact := s3UploadArtifact(t, path)
	artifact.Upload.UploadID = "server-upload-123"
	recorder := &s3RequestRecorder{completeError: context.Canceled}
	uploader := &S3MultipartUploader{HTTPClient: recorder}

	result, err := uploader.Upload(context.Background(), artifact, path, UploadOptions{}, io.Discard)
	var uncertain *artifactTransferCompletionUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("expected uncertain completion error, got %v", err)
	}
	if result.UploadID != "server-upload-123" || result.PartCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(recorder.operations) < 2 || recorder.operations[0] != "upload-part" {
		t.Fatalf("operations = %#v", recorder.operations)
	}
	for _, operation := range recorder.operations[1:] {
		if operation != "complete" {
			t.Fatalf("operations = %#v", recorder.operations)
		}
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
	completeError     error
	completeBody      string
	listCalls         int
	listResponses     []string
	listStatuses      []int
	listedParts       map[int32]int64
	operations        []string
	uploadPartNumbers []int32
}

func (r *s3RequestRecorder) Do(request *http.Request) (*http.Response, error) {
	operation := ""
	body := ""
	headers := make(http.Header)
	switch {
	case request.Method == http.MethodGet && request.URL.Query().Has("uploadId"):
		operation = "list-parts"
		if r.listCalls < len(r.listResponses) {
			body = r.listResponses[r.listCalls]
		} else {
			var parts strings.Builder
			for partNumber := int32(1); partNumber <= 10_000; partNumber++ {
				size, ok := r.listedParts[partNumber]
				if !ok {
					continue
				}
				_, _ = fmt.Fprintf(&parts, "<Part><PartNumber>%d</PartNumber><ETag>\"part-etag-%d\"</ETag><Size>%d</Size></Part>", partNumber, partNumber, size)
			}
			body = "<ListPartsResult><IsTruncated>false</IsTruncated>" + parts.String() + "</ListPartsResult>"
		}
		r.listCalls++
	case request.Method == http.MethodPut && request.URL.Query().Has("partNumber"):
		operation = "upload-part"
		partNumber, _ := strconv.ParseInt(request.URL.Query().Get("partNumber"), 10, 32)
		headers.Set("ETag", fmt.Sprintf(`"part-etag-new-%d"`, partNumber))
		r.uploadPartNumbers = append(r.uploadPartNumbers, int32(partNumber))
	case request.Method == http.MethodPost && request.URL.Query().Has("uploadId"):
		operation = "complete"
		completion, _ := io.ReadAll(request.Body)
		r.completeBody = string(completion)
		body = `<CompleteMultipartUploadResult><ETag>"complete-etag"</ETag></CompleteMultipartUploadResult>`
	default:
		return nil, fmt.Errorf("unexpected S3 request: %s %s", request.Method, request.URL.String())
	}
	r.operations = append(r.operations, operation)
	if operation == "complete" && r.completeError != nil {
		return nil, r.completeError
	}
	status := http.StatusOK
	if operation == "list-parts" && r.listCalls <= len(r.listStatuses) {
		status = r.listStatuses[r.listCalls-1]
	}
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     headers,
		Request:    request,
		StatusCode: status,
	}, nil
}
