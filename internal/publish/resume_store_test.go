package publish

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilePublicationResumeStoreCreatesAdoptsAndProtectsOneRecord(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "publish-resume")
	store := NewFilePublicationResumeStore(directory)
	now := time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC)
	key := strings.Repeat("a", 64)
	first := publicationResumeRecord("submission_first", now, now.Add(time.Hour))

	created, resumed, err := store.CreateOrAdopt(key, first, now)
	if err != nil || resumed || created != first {
		t.Fatalf("created = %#v, resumed = %v, err = %v", created, resumed, err)
	}
	second := publicationResumeRecord("submission_second", now.Add(time.Minute), now.Add(2*time.Hour))
	adopted, resumed, err := store.CreateOrAdopt(key, second, now.Add(time.Minute))
	if err != nil || !resumed || adopted != first {
		t.Fatalf("adopted = %#v, resumed = %v, err = %v", adopted, resumed, err)
	}

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	fileInfo, err := os.Stat(filepath.Join(directory, key+".json"))
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = directory %o, file %o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestFilePublicationResumeStoreReplacesOnlyAnExpiredRecord(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "publish-resume")
	store := NewFilePublicationResumeStore(directory)
	key := strings.Repeat("b", 64)
	now := time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC)
	first := publicationResumeRecord("submission_first", now, now.Add(time.Hour))
	if _, _, err := store.CreateOrAdopt(key, first, now); err != nil {
		t.Fatalf("create first: %v", err)
	}
	second := publicationResumeRecord("submission_second", now.Add(time.Hour), now.Add(2*time.Hour))

	winner, resumed, err := store.CreateOrAdopt(key, second, now.Add(time.Hour))
	if err != nil || resumed || winner != second {
		t.Fatalf("winner = %#v, resumed = %v, err = %v", winner, resumed, err)
	}
}

func TestFilePublicationResumeStoreUpdatesExpiryAndRemovesExactIdentity(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "publish-resume")
	store := NewFilePublicationResumeStore(directory)
	key := strings.Repeat("c", 64)
	now := time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC)
	record := publicationResumeRecord("submission_first", now, now.Add(time.Hour))
	if _, _, err := store.CreateOrAdopt(key, record, now); err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := store.UpdateExpiry(key, record.SubmissionID, now.Add(2*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("update expiry: %v", err)
	}
	if err := store.Remove(key, "submission_other"); err == nil {
		t.Fatal("Remove accepted a different identity")
	}
	if err := store.Remove(key, record.SubmissionID); err != nil {
		t.Fatalf("remove record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, key+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record still exists: %v", err)
	}
}

func TestFilePublicationResumeStoreFailsClosedOnCorruptState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "publish-resume")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := strings.Repeat("d", 64)
	path := filepath.Join(directory, key+".json")
	corrupt := `{"created_at":"2026-08-13T05:00:00Z","expires_at":"2026-08-13T06:00:00Z","schema":"maze.publish-resume.v1","submission_id":"unknown","token":"must-not-be-tolerated"}`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}
	store := NewFilePublicationResumeStore(directory)
	now := time.Date(2026, 8, 13, 5, 0, 0, 0, time.UTC)

	_, _, err := store.CreateOrAdopt(key, publicationResumeRecord("submission_new", now, now.Add(time.Hour)), now)
	if err == nil || !strings.Contains(err.Error(), "local publish recovery is invalid") {
		t.Fatalf("expected corrupt-state error, got %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != corrupt {
		t.Fatalf("corrupt record changed: %q, %v", content, readErr)
	}
}

func TestPublicationResumeKeyOwnsOriginFeedClientAndOrderedFacts(t *testing.T) {
	request := CreatePublishSessionRequest{
		Artifacts: []ArtifactFact{
			{Filename: "one.whl", SHA256: strings.Repeat("a", 64), SizeBytes: 1},
			{Filename: "two.tar.gz", SHA256: strings.Repeat("b", 64), SizeBytes: 2},
		},
		Client:               ClientInfo{Name: "maze", Version: "1.2.3"},
		PublicationRequestID: "ignored",
	}
	first, err := publicationResumeKey("https://pkg.packagemaze.com", "org/feed", request)
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	request.PublicationRequestID = "also-ignored"
	second, err := publicationResumeKey("https://pkg.packagemaze.com", "org/feed", request)
	if err != nil || second != first {
		t.Fatalf("second key = %q, err = %v", second, err)
	}
	request.Artifacts[0], request.Artifacts[1] = request.Artifacts[1], request.Artifacts[0]
	reordered, err := publicationResumeKey("https://pkg.packagemaze.com", "org/feed", request)
	if err != nil || reordered == first {
		t.Fatalf("reordered key = %q, err = %v", reordered, err)
	}
}

func publicationResumeRecord(submissionID string, createdAt time.Time, expiresAt time.Time) PublicationResumeRecord {
	return PublicationResumeRecord{
		CreatedAt:    createdAt.Format(time.RFC3339),
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		Schema:       publicationResumeRecordSchema,
		SubmissionID: submissionID,
	}
}
