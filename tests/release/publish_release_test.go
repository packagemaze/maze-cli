package release_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const candidateSHA = "942631da8f660d2ab3095b40208f6f65d423388b"

type releaseAsset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type releaseView struct {
	IsDraft         bool           `json:"isDraft"`
	IsImmutable     bool           `json:"isImmutable"`
	TargetCommitish string         `json:"targetCommitish"`
	TagName         string         `json:"tagName"`
	Assets          []releaseAsset `json:"assets"`
}

type runOptions struct {
	releaseExists bool
	release       releaseView
	tagSHA        string
}

type runResult struct {
	output string
	log    string
	err    error
}

func TestPublishReleaseCreatesFirstPublication(t *testing.T) {
	result := runPublishRelease(t, runOptions{releaseExists: false})
	if result.err != nil {
		t.Fatalf("publish release: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(result.log, "release create v0.0.5") ||
		!strings.Contains(result.log, "--target "+candidateSHA) {
		t.Fatalf("gh calls = %q", result.log)
	}
	if strings.Contains(result.log, "release upload") ||
		strings.Contains(result.log, "api repos/") {
		t.Fatalf("first publication took rerun path: %q", result.log)
	}
}

func TestPublishReleaseContinuesDraft(t *testing.T) {
	result := runPublishRelease(t, runOptions{
		releaseExists: true,
		release: releaseView{
			IsDraft:         true,
			TargetCommitish: candidateSHA,
			TagName:         "v0.0.5",
			Assets:          expectedAssets(),
		},
	})
	if result.err != nil {
		t.Fatalf("publish release: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(result.log, "release upload v0.0.5") ||
		!strings.Contains(result.log, "--clobber") {
		t.Fatalf("gh calls = %q", result.log)
	}
	if strings.Contains(result.log, "release create") ||
		strings.Contains(result.log, "api repos/") {
		t.Fatalf("draft took publication or immutable path: %q", result.log)
	}
}

func TestPublishReleaseContinuesMutablePublication(t *testing.T) {
	result := runPublishRelease(t, runOptions{
		releaseExists: true,
		release: releaseView{
			TargetCommitish: candidateSHA,
			TagName:         "v0.0.5",
			Assets:          expectedAssets(),
		},
	})
	if result.err != nil {
		t.Fatalf("publish release: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(result.log, "release upload v0.0.5") ||
		!strings.Contains(result.log, "--clobber") {
		t.Fatalf("gh calls = %q", result.log)
	}
}

func TestPublishReleaseVerifiesMatchingImmutableRerunWithoutMutation(t *testing.T) {
	result := runPublishRelease(t, runOptions{
		releaseExists: true,
		release:       matchingImmutableRelease(),
		tagSHA:        candidateSHA,
	})
	if result.err != nil {
		t.Fatalf("publish release: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(
		result.output,
		"Verified immutable release v0.0.5 at "+candidateSHA+" with 4 unchanged assets.",
	) {
		t.Fatalf("output = %q", result.output)
	}
	if !strings.Contains(
		result.log,
		"api repos/packagemaze/maze-cli/commits/v0.0.5 --jq .sha",
	) {
		t.Fatalf("remote tag was not verified: %q", result.log)
	}
	if strings.Contains(result.log, "release upload") ||
		strings.Contains(result.log, "release create") {
		t.Fatalf("immutable rerun attempted mutation: %q", result.log)
	}
}

func TestPublishReleaseImmutableRerunFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		release releaseView
		tagSHA  string
		wantErr string
	}{
		{
			name:    "release target mismatch",
			release: releaseWithTarget("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			tagSHA:  candidateSHA,
			wantErr: "immutable release target mismatch",
		},
		{
			name:    "tag target mismatch",
			release: matchingImmutableRelease(),
			tagSHA:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			wantErr: "immutable release tag target mismatch",
		},
		{
			name: "asset set mismatch",
			release: releaseWithAssets(append(
				expectedAssets(),
				releaseAsset{
					Name:   "unexpected.txt",
					Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				},
			)),
			tagSHA:  candidateSHA,
			wantErr: "immutable release asset set mismatch",
		},
		{
			name: "asset digest mismatch",
			release: releaseWithAssets(replaceDigest(
				expectedAssets(),
				"maze_linux_amd64.tar.gz",
				"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			)),
			tagSHA:  candidateSHA,
			wantErr: "immutable release asset digest mismatch for maze_linux_amd64.tar.gz",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runPublishRelease(t, runOptions{
				releaseExists: true,
				release:       test.release,
				tagSHA:        test.tagSHA,
			})
			if result.err == nil || !strings.Contains(result.output, test.wantErr) {
				t.Fatalf("error = %v, output = %q", result.err, result.output)
			}
			if strings.Contains(result.log, "release upload") ||
				strings.Contains(result.log, "release create") {
				t.Fatalf("mismatch attempted mutation: %q", result.log)
			}
		})
	}
}

func runPublishRelease(t *testing.T, options runOptions) runResult {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	distDir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatalf("create dist: %v", err)
	}
	for _, file := range releaseFiles() {
		if err := os.WriteFile(filepath.Join(distDir, file.name), file.content, 0o644); err != nil {
			t.Fatalf("write asset: %v", err)
		}
	}

	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	fakeGH := filepath.Join(fakeBin, "gh")
	if err := os.WriteFile(fakeGH, []byte(fakeGHScript), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	releaseJSON, err := json.Marshal(options.release)
	if err != nil {
		t.Fatalf("encode release: %v", err)
	}

	command := exec.Command(
		"bash",
		filepath.Join(root, ".github", "scripts", "publish-release.sh"),
	)
	command.Env = append(
		os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DIST_DIR="+distDir,
		"FAKE_GH_LOG="+logPath,
		fmt.Sprintf("FAKE_GH_RELEASE_EXISTS=%t", options.releaseExists),
		"FAKE_GH_RELEASE_JSON="+string(releaseJSON),
		"FAKE_GH_TAG_SHA="+options.tagSHA,
		"GITHUB_REPOSITORY=packagemaze/maze-cli",
		"GITHUB_SHA="+candidateSHA,
		"RELEASE_TAG=v0.0.5",
		"VERSION=0.0.5",
	)
	output, commandErr := command.CombinedOutput()
	logContent, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read gh log: %v", readErr)
	}
	return runResult{
		output: string(output),
		log:    string(logContent),
		err:    commandErr,
	}
}

func matchingImmutableRelease() releaseView {
	return releaseView{
		IsImmutable:     true,
		TargetCommitish: candidateSHA,
		TagName:         "v0.0.5",
		Assets:          expectedAssets(),
	}
}

func releaseWithTarget(target string) releaseView {
	release := matchingImmutableRelease()
	release.TargetCommitish = target
	return release
}

func releaseWithAssets(assets []releaseAsset) releaseView {
	release := matchingImmutableRelease()
	release.Assets = assets
	return release
}

func replaceDigest(assets []releaseAsset, name string, digest string) []releaseAsset {
	replaced := append([]releaseAsset(nil), assets...)
	for index := range replaced {
		if replaced[index].Name == name {
			replaced[index].Digest = digest
		}
	}
	return replaced
}

func expectedAssets() []releaseAsset {
	files := releaseFiles()
	assets := make([]releaseAsset, 0, len(files))
	for _, file := range files {
		sum := sha256.Sum256(file.content)
		assets = append(assets, releaseAsset{
			Name:   file.name,
			Digest: fmt.Sprintf("sha256:%x", sum),
		})
	}
	return assets
}

func releaseFiles() []struct {
	name    string
	content []byte
} {
	return []struct {
		name    string
		content []byte
	}{
		{name: "maze_checksums.txt", content: []byte("published checksums\n")},
		{name: "maze_darwin_arm64.tar.gz", content: []byte("darwin arm64 archive")},
		{name: "maze_linux_amd64.tar.gz", content: []byte("linux amd64 archive")},
		{name: "maze_linux_arm64.tar.gz", content: []byte("linux arm64 archive")},
	}
}

const fakeGHScript = `#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$FAKE_GH_LOG"

if [[ "$1" == "release" && "$2" == "view" ]]; then
  if [[ "$FAKE_GH_RELEASE_EXISTS" != "true" ]]; then
    printf 'release not found\n' >&2
    exit 1
  fi
  printf '%s\n' "$FAKE_GH_RELEASE_JSON"
  exit 0
fi

if [[ "$1" == "release" && ( "$2" == "create" || "$2" == "upload" ) ]]; then
  exit 0
fi

if [[ "$1" == "api" ]]; then
  printf '%s\n' "$FAKE_GH_TAG_SHA"
  exit 0
fi

printf 'unexpected fake gh call: %s\n' "$*" >&2
exit 1
`
