package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	execChildModeEnv   = "MAZE_E2E_EXEC_CHILD"
	execChildReportEnv = "MAZE_E2E_EXEC_REPORT"
	execChildExitEnv   = "MAZE_E2E_EXEC_EXIT"
	execToken          = "maze_ci_e2e_exec_secret"
)

// TestMain lets this test binary be the command maze auth exec runs.
func TestMain(m *testing.M) {
	if mode := os.Getenv(execChildModeEnv); mode != "" {
		os.Exit(runExecChild(mode))
	}
	os.Exit(m.Run())
}

type execChildReport struct {
	Env       map[string]string `json:"env"`
	Npmrc     string            `json:"npmrc"`
	NpmrcMode uint32            `json:"npmrc_mode"`
	DirMode   uint32            `json:"dir_mode"`
}

func runExecChild(mode string) int {
	report := execChildReport{Env: map[string]string{}}
	for _, name := range []string{"MAZE_TOKEN", "NPM_CONFIG_USERCONFIG", "YARN_NPM_AUTH_TOKEN", "COREPACK_NPM_REGISTRY", "PIP_INDEX_URL", "UV_DEFAULT_INDEX", "PIP_EXTRA_INDEX_URL"} {
		if value, ok := os.LookupEnv(name); ok {
			report.Env[name] = value
		}
	}
	if path := os.Getenv("NPM_CONFIG_USERCONFIG"); path != "" {
		content, _ := os.ReadFile(path)
		report.Npmrc = string(content)
		if info, err := os.Stat(path); err == nil {
			report.NpmrcMode = uint32(info.Mode().Perm())
		}
		if info, err := os.Stat(filepath.Dir(path)); err == nil {
			report.DirMode = uint32(info.Mode().Perm())
		}
	}
	content, _ := json.Marshal(report)
	path := os.Getenv(execChildReportEnv)
	if os.WriteFile(path+".partial", content, 0o600) != nil || os.Rename(path+".partial", path) != nil {
		return 99
	}
	if mode == "wait" {
		time.Sleep(time.Minute)
	}
	code, _ := strconv.Atoi(os.Getenv(execChildExitEnv))
	return code
}

func buildMazeBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "maze")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "-C", packageMazeCLIDir(t), "build", "-o", binary, "./cmd/maze")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build maze: %v\n%s", err, output)
	}
	return binary
}

func execBackend(t *testing.T) *httptest.Server {
	t.Helper()
	response := `{"build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feeds":[{"artifact_protocol":"npm","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm"},{"artifact_protocol":"pypi","feed":"acme/pypi","feed_base_url":"https://pkg.packagemaze.com/acme/pypi"}],"purpose":"install","scopes":["read"],"token":"` + execToken + `","token_type":"Bearer"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, response)
	}))
	t.Cleanup(server.Close)
	return server
}

// execCommand runs the built maze with this test binary as the command, in an
// environment that names no built-in platform and no PackageMaze settings.
func execCommand(t *testing.T, binary string, server *httptest.Server, mode string, report string, exit int) *exec.Cmd {
	t.Helper()
	command := exec.Command(binary, "auth", "exec",
		"--api-url", server.URL+"/v1", "--allow-insecure-localhost", "--provider", "oidc",
		"--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install",
		"--", os.Args[0], "-test.run=^$")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name != "GITHUB_ACTIONS" && name != "CIRCLECI" && !strings.HasPrefix(name, "MAZE_") && !strings.HasPrefix(strings.ToUpper(name), "NPM_CONFIG_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env,
		"MAZE_OIDC_TOKEN=platform-identity-token",
		"PIP_EXTRA_INDEX_URL=https://pypi.org/simple/",
		execChildModeEnv+"="+mode,
		execChildReportEnv+"="+report,
		execChildExitEnv+"="+strconv.Itoa(exit),
	)
	return command
}

func readExecChildReport(t *testing.T, path string) execChildReport {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the command wrote no report: %v", err)
	}
	var report execChildReport
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return report
}

func TestAuthExecRunsTheCommandWithPrivatePackageClientConfiguration(t *testing.T) {
	t.Parallel()
	binary := buildMazeBinary(t)
	report := filepath.Join(t.TempDir(), "report.json")
	command := execCommand(t, binary, execBackend(t), "report", report, 3)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr

	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 3 {
		t.Fatalf("maze exit = %v, want the command's status 3\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), execToken) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	child := readExecChildReport(t, report)
	index := "https://__token__:" + execToken + "@pkg.packagemaze.com/acme/pypi/simple/"
	if child.Env["MAZE_TOKEN"] != execToken || child.Env["YARN_NPM_AUTH_TOKEN"] != execToken ||
		child.Env["COREPACK_NPM_REGISTRY"] != "https://pkg.packagemaze.com/acme/npm" ||
		child.Env["PIP_INDEX_URL"] != index || child.Env["UV_DEFAULT_INDEX"] != index {
		t.Fatalf("command environment = %#v", child.Env)
	}
	if _, inherited := child.Env["PIP_EXTRA_INDEX_URL"]; inherited {
		t.Fatalf("an inherited extra index reached the command")
	}
	if child.Npmrc != "registry=https://pkg.packagemaze.com/acme/npm/\nreplace-registry-host=npmjs\n//pkg.packagemaze.com/acme/npm/:_authToken=${MAZE_TOKEN}\n" {
		t.Fatalf("npmrc = %q", child.Npmrc)
	}
	if runtime.GOOS != "windows" && (child.NpmrcMode != 0o600 || child.DirMode != 0o700) {
		t.Fatalf("modes: file %o, dir %o", child.NpmrcMode, child.DirMode)
	}
	if _, err := os.Stat(filepath.Dir(child.Env["NPM_CONFIG_USERCONFIG"])); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private configuration was not removed: %v", err)
	}
}

func TestAuthExecRemovesItsConfigurationWhenSignalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no SIGTERM to send")
	}
	t.Parallel()
	binary := buildMazeBinary(t)
	report := filepath.Join(t.TempDir(), "report.json")
	command := execCommand(t, binary, execBackend(t), "wait", report, 0)
	if err := command.Start(); err != nil {
		t.Fatalf("start maze: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(report); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatalf("the command never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	child := readExecChildReport(t, report)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal maze: %v", err)
	}
	err := command.Wait()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Fatalf("maze exit = %v, want %d", err, 128+int(syscall.SIGTERM))
	}
	if _, err := os.Stat(filepath.Dir(child.Env["NPM_CONFIG_USERCONFIG"])); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("private configuration was not removed: %v", err)
	}
}
