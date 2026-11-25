package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testRunURL      = "https://www.packagemaze.com/your-org/runs/482"
	testApprovalURL = "https://www.packagemaze.com/your-org/approvals/chg_123"
)

// runHeldPublish publishes a wheel and an sdist of one release and answers
// with the status that hold sets up.
func runHeldPublish(t *testing.T, hold func(*PublishSessionStatusResponse)) (Result, *fakeClient, string, error) {
	t.Helper()
	wheel := writeTempArtifact(t, "example-1.0.0-py3-none-any.whl", "wheel bytes")
	sdist := writeTempArtifact(t, "example-1.0.0.tar.gz", "sdist bytes")
	client := &fakeClient{}
	client.createResponse = createResponseForFacts(t, "plan_held", []ArtifactFact{
		factForPath(t, wheel),
		factForPath(t, sdist),
	})
	status := statusResponse("plan_held", "ready", client.createResponse.Plan.Artifacts)
	hold(&status)
	client.statusResponses = []PublishSessionStatusResponse{status}
	var stderr bytes.Buffer
	result, _, err := Run(
		context.Background(),
		Config{Feed: "your-org/npm", TokenEnv: DefaultTokenEnv, Wait: true},
		[]string{wheel, sdist},
		Dependencies{
			Client: client,
			Env:    mapLookup(map[string]string{DefaultTokenEnv: "pm_publish_token"}),
			PublicationRequestID: func() (string, error) {
				return "publication_request_held", nil
			},
			Sleep:    func(context.Context, time.Duration) error { return nil },
			Uploader: &orderedUploader{},
		},
		&stderr,
	)
	return result, client, stderr.String(), err
}

func holdEachArtifact(approval PublishApproval) func(*PublishSessionStatusResponse) {
	return func(status *PublishSessionStatusResponse) {
		for index := range status.Artifacts {
			held := approval
			status.Artifacts[index].PublishApproval = &held
		}
	}
}

func TestRunReportsAVersionAwaitingPublishApprovalAsASuccess(t *testing.T) {
	result, client, stderr, err := runHeldPublish(t, holdEachArtifact(PublishApproval{
		ApprovalURL: testApprovalURL,
		RunURL:      testRunURL,
		State:       PublishApprovalAwaiting,
	}))
	if err != nil {
		t.Fatalf("a publish awaiting approval failed: %v", err)
	}
	want := "@your-org/large-package@1.0.0 is waiting for an Organization Admin's approval: " + testRunURL + "\n"
	if strings.Count(stderr, "is waiting for") != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want the version named once: %q", stderr, want)
	}
	if client.statusCalls != 1 {
		t.Fatalf("status calls = %d, want no polling past ready", client.statusCalls)
	}
	if approval := result.Artifacts[0].PublishApproval; approval == nil || approval.State != PublishApprovalAwaiting {
		t.Fatalf("result artifact approval = %#v", approval)
	}
}

func TestRunLinksTheApprovalPageWhenTheSessionHoldsWithoutARun(t *testing.T) {
	_, _, stderr, err := runHeldPublish(t, func(status *PublishSessionStatusResponse) {
		status.PublishSession.PublishApproval = &PublishApproval{
			ApprovalURL: testApprovalURL,
			State:       PublishApprovalAwaiting,
		}
	})
	if err != nil {
		t.Fatalf("a publish awaiting approval failed: %v", err)
	}
	want := "@your-org/large-package@1.0.0 is waiting for an Organization Admin's approval: " + testApprovalURL + "\n"
	if stderr != "Uploading example-1.0.0-py3-none-any.whl\nUploading example-1.0.0.tar.gz\nWaiting for PackageMaze publish status\n"+want {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestRunFailsWhenAnOrganizationAdminRejectedTheRun(t *testing.T) {
	result, _, stderr, err := runHeldPublish(t, holdEachArtifact(PublishApproval{
		ApprovalURL: testApprovalURL,
		RunURL:      testRunURL,
		State:       PublishApprovalRejected,
	}))
	want := "@your-org/large-package@1.0.0 will not be published: an Organization Admin rejected its Run: " + testRunURL
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if strings.Contains(stderr, "waiting for") {
		t.Fatalf("a rejected Run was reported as waiting: %q", stderr)
	}
	if result.PublishSessionID != "plan_held" {
		t.Fatalf("result = %#v, want the session for JSON output", result)
	}
}

func TestRunSaysNothingAboutApprovalWhenNothingIsHeld(t *testing.T) {
	_, _, stderr, err := runHeldPublish(t, func(*PublishSessionStatusResponse) {})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.Contains(stderr, "approval") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestPublishApprovalReadsFromTheStatusResponseAndReachesJSONOutput(t *testing.T) {
	body := `{"schema_version":1,` +
		`"publish_session":{"id":"plan_123","state":"ready",` +
		`"publish_approval":{"state":"awaiting_approval","number":482,"run_url":null,"approval_url":"` + testApprovalURL + `"}},` +
		`"artifacts":[{"artifact":{"filename":"example-1.0.0.tgz"},"package":{"name":"example","version":"1.0.0"},"state":"ready",` +
		`"publish_approval":{"state":"awaiting_approval","number":482,"run_url":"` + testRunURL + `","approval_url":"` + testApprovalURL + `"}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()

	client := NewClient(server.URL, true, server.Client())
	status, err := client.GetStatus(context.Background(), "secret", server.URL+"/your-org/npm/-/packagemaze/v1/publish-sessions/plan_123")
	if err != nil {
		t.Fatalf("GetStatus returned error: %v", err)
	}
	session := status.PublishSession.PublishApproval
	if session == nil || session.State != PublishApprovalAwaiting || session.RunURL != "" || session.link() != testApprovalURL {
		t.Fatalf("session approval = %#v", session)
	}
	if artifact := status.Artifacts[0].PublishApproval; artifact == nil || artifact.link() != testRunURL {
		t.Fatalf("artifact approval = %#v", artifact)
	}

	var output bytes.Buffer
	if err := Write(resultFromStatus(status, CreatePublishSessionResponse{}), FormatJSON, &output); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	var decoded struct {
		Artifacts []struct {
			PublishApproval map[string]any `json:"publish_approval"`
		} `json:"artifacts"`
		PublishApproval map[string]any `json:"publish_approval"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON output did not decode: %v\n%s", err, output.String())
	}
	if decoded.PublishApproval["number"] != float64(482) || decoded.PublishApproval["approval_url"] != testApprovalURL {
		t.Fatalf("session publish_approval = %#v", decoded.PublishApproval)
	}
	if _, hasRun := decoded.PublishApproval["run_url"]; hasRun {
		t.Fatalf("a null run_url was written out: %#v", decoded.PublishApproval)
	}
	if decoded.Artifacts[0].PublishApproval["run_url"] != testRunURL {
		t.Fatalf("artifact publish_approval = %#v", decoded.Artifacts[0].PublishApproval)
	}
}
