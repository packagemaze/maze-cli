package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const publicationResumeRecordSchema = "maze.publish-resume.v1"

var publicationResumeKeyPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type PublicationResumeRecord struct {
	CreatedAt    string `json:"created_at"`
	ExpiresAt    string `json:"expires_at"`
	Schema       string `json:"schema"`
	SubmissionID string `json:"submission_id"`
}

type PublicationResumeStore interface {
	CreateOrAdopt(key string, candidate PublicationResumeRecord, now time.Time) (PublicationResumeRecord, bool, error)
	Remove(key string, submissionID string) error
	UpdateExpiry(key string, submissionID string, expiresAt string) error
}

type FilePublicationResumeStore struct {
	directory string
	initErr   error
}

func NewFilePublicationResumeStore(directory string) *FilePublicationResumeStore {
	return &FilePublicationResumeStore{directory: directory}
}

func NewDefaultPublicationResumeStore() *FilePublicationResumeStore {
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return &FilePublicationResumeStore{initErr: err}
	}
	return NewFilePublicationResumeStore(filepath.Join(cacheDirectory, "maze", "publish-resume"))
}

func (s *FilePublicationResumeStore) CreateOrAdopt(key string, candidate PublicationResumeRecord, now time.Time) (PublicationResumeRecord, bool, error) {
	if err := s.validateReady(key); err != nil {
		return PublicationResumeRecord{}, false, err
	}
	if err := validatePublicationResumeRecord(candidate); err != nil {
		return PublicationResumeRecord{}, false, err
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return PublicationResumeRecord{}, false, fmt.Errorf("prepare local publish recovery: %w", err)
	}
	if err := os.Chmod(s.directory, 0o700); err != nil {
		return PublicationResumeRecord{}, false, fmt.Errorf("protect local publish recovery: %w", err)
	}
	path := s.path(key)
	for attempt := 0; attempt < 3; attempt++ {
		created, err := createPublicationResumeRecord(path, candidate)
		if err == nil && created {
			return candidate, false, nil
		}
		if err != nil && !errors.Is(err, os.ErrExist) {
			return PublicationResumeRecord{}, false, fmt.Errorf("prepare local publish recovery: %w", err)
		}
		existing, err := readPublicationResumeRecord(path)
		if err != nil {
			return PublicationResumeRecord{}, false, fmt.Errorf("read local publish recovery: %w", err)
		}
		if !publicationResumeExpired(existing, now) {
			if err := os.Chmod(path, 0o600); err != nil {
				return PublicationResumeRecord{}, false, fmt.Errorf("protect local publish recovery: %w", err)
			}
			return existing, true, nil
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return PublicationResumeRecord{}, false, fmt.Errorf("replace expired local publish recovery: %w", err)
		}
	}
	return PublicationResumeRecord{}, false, fmt.Errorf("local publish recovery changed concurrently")
}

func (s *FilePublicationResumeStore) UpdateExpiry(key string, submissionID string, expiresAt string) error {
	if err := s.validateReady(key); err != nil {
		return err
	}
	path := s.path(key)
	record, err := readPublicationResumeRecord(path)
	if err != nil {
		return fmt.Errorf("read local publish recovery: %w", err)
	}
	if record.SubmissionID != submissionID {
		return fmt.Errorf("local publish recovery identity changed concurrently")
	}
	record.ExpiresAt = expiresAt
	if err := validatePublicationResumeRecord(record); err != nil {
		return err
	}
	if err := replacePublicationResumeRecord(path, record); err != nil {
		return fmt.Errorf("update local publish recovery: %w", err)
	}
	return nil
}

func (s *FilePublicationResumeStore) Remove(key string, submissionID string) error {
	if err := s.validateReady(key); err != nil {
		return err
	}
	path := s.path(key)
	record, err := readPublicationResumeRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local publish recovery: %w", err)
	}
	if record.SubmissionID != submissionID {
		return fmt.Errorf("local publish recovery identity changed concurrently")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear local publish recovery: %w", err)
	}
	return nil
}

func (s *FilePublicationResumeStore) validateReady(key string) error {
	if s.initErr != nil {
		return fmt.Errorf("prepare local publish recovery: %w", s.initErr)
	}
	if strings.TrimSpace(s.directory) == "" || !publicationResumeKeyPattern.MatchString(key) {
		return fmt.Errorf("local publish recovery key is invalid")
	}
	return nil
}

func (s *FilePublicationResumeStore) path(key string) string {
	return filepath.Join(s.directory, key+".json")
}

func createPublicationResumeRecord(path string, record PublicationResumeRecord) (bool, error) {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".publish-resume-*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return false, err
	}
	content, encodeErr := encodePublicationResumeRecord(record)
	if encodeErr == nil {
		_, encodeErr = file.Write(content)
	}
	if encodeErr == nil {
		encodeErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil {
		return false, encodeErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return false, err
	}
	return true, nil
}

func replacePublicationResumeRecord(path string, record PublicationResumeRecord) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".publish-resume-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	content, err := encodePublicationResumeRecord(record)
	if err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, path)
}

func readPublicationResumeRecord(path string) (PublicationResumeRecord, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return PublicationResumeRecord{}, err
	}
	var record PublicationResumeRecord
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return PublicationResumeRecord{}, fmt.Errorf("local publish recovery is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PublicationResumeRecord{}, fmt.Errorf("local publish recovery is invalid")
	}
	if err := validatePublicationResumeRecord(record); err != nil {
		return PublicationResumeRecord{}, err
	}
	return record, nil
}

func encodePublicationResumeRecord(record PublicationResumeRecord) ([]byte, error) {
	content, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func validatePublicationResumeRecord(record PublicationResumeRecord) error {
	if record.Schema != publicationResumeRecordSchema ||
		strings.TrimSpace(record.SubmissionID) == "" ||
		len(record.SubmissionID) > 1_024 ||
		strings.ContainsAny(record.SubmissionID, "\r\n\x00") {
		return fmt.Errorf("local publish recovery is invalid")
	}
	createdAt, createdErr := time.Parse(time.RFC3339, record.CreatedAt)
	expiresAt, expiresErr := time.Parse(time.RFC3339, record.ExpiresAt)
	if createdErr != nil || expiresErr != nil || !expiresAt.After(createdAt) {
		return fmt.Errorf("local publish recovery is invalid")
	}
	return nil
}

func publicationResumeExpired(record PublicationResumeRecord, now time.Time) bool {
	expiresAt, _ := time.Parse(time.RFC3339, record.ExpiresAt)
	return !now.Before(expiresAt)
}

func publicationResumeKey(packageClientURL string, feed string, request CreatePublishSessionRequest) (string, error) {
	request.PublicationRequestID = ""
	content, err := json.Marshal(struct {
		Feed             string                      `json:"feed"`
		PackageClientURL string                      `json:"package_client_url"`
		Request          CreatePublishSessionRequest `json:"request"`
	}{
		Feed:             feed,
		PackageClientURL: packageClientURL,
		Request:          request,
	})
	if err != nil {
		return "", err
	}
	sum := sha256Bytes(content)
	return hex.EncodeToString(sum), nil
}

func sha256Bytes(content []byte) []byte {
	hash := sha256.Sum256(content)
	return hash[:]
}
