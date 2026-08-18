package release_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestBuildReleaseAssetsProducesAllOfficialPlatformArchives(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles the CLI for every official platform")
	}
	distDir := runBuildReleaseAssets(t, "v9.9.9-test")

	wantAssets := []string{
		"maze_checksums.txt",
		"maze_darwin_arm64.tar.gz",
		"maze_linux_amd64.tar.gz",
		"maze_linux_arm64.tar.gz",
		"maze_windows_amd64.zip",
	}
	entries, err := os.ReadDir(distDir)
	if err != nil {
		t.Fatalf("read dist: %v", err)
	}
	gotAssets := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotAssets = append(gotAssets, entry.Name())
	}
	sort.Strings(gotAssets)
	if strings.Join(gotAssets, ",") != strings.Join(wantAssets, ",") {
		t.Fatalf("assets = %v, want %v", gotAssets, wantAssets)
	}

	checksums := readChecksums(t, filepath.Join(distDir, "maze_checksums.txt"))
	if len(checksums) != len(wantAssets)-1 {
		t.Fatalf("checksums cover %d assets, want %d", len(checksums), len(wantAssets)-1)
	}
	for _, asset := range wantAssets {
		if asset == "maze_checksums.txt" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(distDir, asset))
		if err != nil {
			t.Fatalf("read %s: %v", asset, err)
		}
		if digest := fmt.Sprintf("%x", sha256.Sum256(content)); checksums[asset] != digest {
			t.Fatalf("checksum for %s = %q, want %q", asset, checksums[asset], digest)
		}
	}

	if names := zipEntryNames(t, filepath.Join(distDir, "maze_windows_amd64.zip")); strings.Join(names, ",") != "maze.exe" {
		t.Fatalf("windows archive entries = %v, want [maze.exe]", names)
	}
	for _, archive := range []string{"maze_linux_amd64.tar.gz", "maze_linux_arm64.tar.gz", "maze_darwin_arm64.tar.gz"} {
		if names := tarEntryNames(t, filepath.Join(distDir, archive)); strings.Join(names, ",") != "maze" {
			t.Fatalf("%s entries = %v, want [maze]", archive, names)
		}
	}

	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		binary := extractTarEntry(t, filepath.Join(distDir, "maze_linux_amd64.tar.gz"), "maze")
		output, err := exec.Command(binary, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("run built maze version: %v\n%s", err, output)
		}
		if !strings.HasPrefix(string(output), "maze 9.9.9-test (build-test-commit, ") {
			t.Fatalf("version output = %q", output)
		}
	}
}

func TestBuildReleaseAssetsRejectsNonVersionTags(t *testing.T) {
	root := repositoryRoot(t)
	command := exec.Command("bash", filepath.Join(root, "scripts", "build-release-assets.sh"))
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"DIST_DIR="+filepath.Join(t.TempDir(), "dist"),
		"RELEASE_TAG=9.9.9",
		"RELEASE_COMMIT=build-test-commit",
	)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "maze CLI release tags must use v*") {
		t.Fatalf("error = %v, output = %q", err, output)
	}
}

func runBuildReleaseAssets(t *testing.T, releaseTag string) string {
	t.Helper()
	root := repositoryRoot(t)
	distDir := filepath.Join(t.TempDir(), "dist")
	command := exec.Command("bash", filepath.Join(root, "scripts", "build-release-assets.sh"))
	command.Dir = root
	command.Env = append(
		os.Environ(),
		"DIST_DIR="+distDir,
		"RELEASE_TAG="+releaseTag,
		"RELEASE_COMMIT=build-test-commit",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build release assets: %v\n%s", err, output)
	}
	return distDir
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release assets are built by a bash pipeline on Linux release runners")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func readChecksums(t *testing.T, path string) map[string]string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	checksums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("checksum line = %q", line)
		}
		checksums[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return checksums
}

func zipEntryNames(t *testing.T, path string) []string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip %s: %v", path, err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		if file.UncompressedSize64 == 0 {
			t.Fatalf("zip entry %s is empty", file.Name)
		}
		names = append(names, file.Name)
	}
	sort.Strings(names)
	return names
}

func tarEntryNames(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer file.Close()
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	defer decompressed.Close()
	names := []string{}
	archive := tar.NewReader(decompressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive %s: %v", path, err)
		}
		if header.Size == 0 {
			t.Fatalf("archive entry %s is empty", header.Name)
		}
		names = append(names, header.Name)
	}
	sort.Strings(names)
	return names
}

func extractTarEntry(t *testing.T, path string, name string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer file.Close()
	decompressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	defer decompressed.Close()
	archive := tar.NewReader(decompressed)
	for {
		header, err := archive.Next()
		if err != nil {
			t.Fatalf("entry %s not found in %s: %v", name, path, err)
		}
		if header.Name != name {
			continue
		}
		extracted := filepath.Join(t.TempDir(), name)
		content, err := io.ReadAll(archive)
		if err != nil {
			t.Fatalf("read entry %s: %v", name, err)
		}
		if err := os.WriteFile(extracted, content, 0o755); err != nil {
			t.Fatalf("write entry %s: %v", name, err)
		}
		return extracted
	}
}
