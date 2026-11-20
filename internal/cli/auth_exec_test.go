package cli

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/packagemaze/maze-cli/internal/auth"
	"github.com/packagemaze/maze-cli/internal/clientconfig"
	"github.com/packagemaze/maze-cli/internal/subprocess"
)

const execToken = "maze_ci_exec_secret"

const execOneFeedResponse = `{"artifact_protocol":"npm","build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm","purpose":"install","scopes":["read"],"token":"` + execToken + `","token_type":"Bearer"}`

const execSeveralFeedsResponse = `{"build_number":7,"build_url":"https://www.packagemaze.com/acme/builds/7","expires_at":"2026-11-10T10:00:00Z","feeds":[{"artifact_protocol":"npm","feed":"acme/npm","feed_base_url":"https://pkg.packagemaze.com/acme/npm"},{"artifact_protocol":"pypi","feed":"acme/pypi","feed_base_url":"https://pkg.packagemaze.com/acme/pypi"}],"purpose":"install","scopes":["read"],"token":"` + execToken + `","token_type":"Bearer"}`

// childRun is what the injected runner saw while the command ran.
type childRun struct {
	spec      subprocess.Spec
	env       map[string]string
	npmrc     string
	npmrcMode fs.FileMode
	dirMode   fs.FileMode
}

type execHarness struct {
	t        *testing.T
	root     string
	response string
	exit     int
	signal   os.Signal
	child    *childRun
	stopped  bool
	exchange string
}

func newExecHarness(t *testing.T, response string) *execHarness {
	return &execHarness{t: t, root: t.TempDir(), response: response}
}

func (h *execHarness) dependencies() execDependencies {
	filesystem := clientconfig.OSFilesystem()
	filesystem.MkdirTemp = func(_ string, pattern string) (string, error) { return os.MkdirTemp(h.root, pattern) }
	return execDependencies{
		Filesystem: filesystem,
		Environ: func() []string {
			return []string{"PATH=/bin", "npm_config_registry=https://registry.npmjs.org/", "PIP_EXTRA_INDEX_URL=https://pypi.org/simple/"}
		},
		Signals: func() (<-chan os.Signal, func()) {
			signals := make(chan os.Signal, 1)
			if h.signal != nil {
				signals <- h.signal
			}
			return signals, func() { h.stopped = true }
		},
		Run: h.run,
	}
}

func (h *execHarness) run(spec subprocess.Spec, signals <-chan os.Signal) (int, error) {
	child := &childRun{spec: spec, env: map[string]string{}}
	for _, entry := range spec.Env {
		name, value, _ := strings.Cut(entry, "=")
		child.env[name] = value
	}
	if path := child.env["NPM_CONFIG_USERCONFIG"]; path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			h.t.Fatalf("the npm user config does not exist while the command runs: %v", err)
		}
		child.npmrc = string(content)
		child.npmrcMode = fileMode(h.t, path)
		child.dirMode = fileMode(h.t, filepath.Dir(path))
	}
	h.child = child
	select {
	case received := <-signals:
		return 128 + int(received.(syscall.Signal)), nil
	default:
		return h.exit, nil
	}
}

func fileMode(t *testing.T, path string) fs.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// execute runs maze auth exec against a backend answering h.response.
func (h *execHarness) execute(args ...string) (string, string, error) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		h.exchange = string(content)
		_, _ = io.WriteString(writer, h.response)
	}))
	defer server.Close()
	deps := auth.Dependencies{Env: mapLookup(map[string]string{"MAZE_OIDC_TOKEN": "platform-identity"}), HTTPClient: server.Client()}
	root := &cobra.Command{Use: "maze", SilenceUsage: true, SilenceErrors: true}
	authCommand := &cobra.Command{Use: "auth"}
	authCommand.AddCommand(newAuthExecCommand(deps, h.dependencies()))
	root.AddCommand(authCommand)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"auth", "exec", "--provider", "oidc", "--api-url", server.URL, "--allow-insecure-localhost"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func (h *execHarness) assertWorkspaceRemoved() {
	h.t.Helper()
	entries, err := os.ReadDir(h.root)
	if err != nil || len(entries) != 0 {
		h.t.Fatalf("private configuration left behind: %v %v", entries, err)
	}
	if !h.stopped {
		h.t.Fatalf("signals were not released")
	}
}

func TestAuthExecRunsTheCommandWithNpmFeedConfiguration(t *testing.T) {
	h := newExecHarness(t, execOneFeedResponse)
	stdout, stderr, err := h.execute("--feed", "acme/npm", "--purpose", "install", "--", "npm", "ci", "--no-audit")
	if err != nil {
		t.Fatalf("command returned error: %v\nstderr: %s", err, stderr)
	}
	child := h.child
	if child.spec.Path != "npm" || strings.Join(child.spec.Args, " ") != "ci --no-audit" {
		t.Fatalf("argv = %s %v", child.spec.Path, child.spec.Args)
	}
	for name, want := range map[string]string{
		"MAZE_TOKEN":            execToken,
		"COREPACK_NPM_REGISTRY": "https://pkg.packagemaze.com/acme/npm",
		"COREPACK_NPM_TOKEN":    execToken,
		"PATH":                  "/bin",
	} {
		if child.env[name] != want {
			t.Fatalf("%s = %q, want %q", name, child.env[name], want)
		}
	}
	if _, inherited := child.env["npm_config_registry"]; inherited {
		t.Fatalf("an inherited registry would outrank the user config")
	}
	if child.env["PIP_EXTRA_INDEX_URL"] != "https://pypi.org/simple/" {
		t.Fatalf("PyPI variables changed without a PyPI Feed: %#v", child.env)
	}
	if child.npmrc != "registry=https://pkg.packagemaze.com/acme/npm/\nreplace-registry-host=npmjs\n//pkg.packagemaze.com/acme/npm/:_authToken=${MAZE_TOKEN}\n" {
		t.Fatalf("npmrc = %q", child.npmrc)
	}
	if runtime.GOOS != "windows" && (child.npmrcMode != 0o600 || child.dirMode != 0o700) {
		t.Fatalf("modes: file %v, dir %v", child.npmrcMode, child.dirMode)
	}
	for _, arg := range append([]string{child.spec.Path}, child.spec.Args...) {
		if strings.Contains(arg, execToken) {
			t.Fatalf("the Token reached the command line")
		}
	}
	if strings.Contains(stdout+stderr, execToken) || stdout != "" || stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(h.exchange, `"feed":"acme/npm"`) {
		t.Fatalf("exchange = %s", h.exchange)
	}
	h.assertWorkspaceRemoved()
}

func TestAuthExecSeveralFeedsConfiguresEachArtifactProtocol(t *testing.T) {
	h := newExecHarness(t, execSeveralFeedsResponse)
	_, stderr, err := h.execute("--feed", "acme/npm", "--feed", "acme/pypi", "--purpose", "install", "--verbose", "--", "make", "install")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if !strings.Contains(h.exchange, `"feeds":["acme/npm","acme/pypi"]`) {
		t.Fatalf("exchange = %s", h.exchange)
	}
	index := "https://__token__:" + execToken + "@pkg.packagemaze.com/acme/pypi/simple/"
	if h.child.env["PIP_INDEX_URL"] != index || h.child.env["UV_DEFAULT_INDEX"] != index {
		t.Fatalf("env = %#v", h.child.env)
	}
	if _, inherited := h.child.env["PIP_EXTRA_INDEX_URL"]; inherited {
		t.Fatalf("an inherited extra index was kept")
	}
	if h.child.env["NPM_CONFIG_USERCONFIG"] == "" {
		t.Fatalf("npm was not configured")
	}
	for _, want := range []string{
		"maze auth exec: npm Feed acme/npm at https://pkg.packagemaze.com/acme/npm\n",
		"maze auth exec: pypi Feed acme/pypi at https://pkg.packagemaze.com/acme/pypi\n",
		"maze auth exec: set MAZE_TOKEN, NPM_CONFIG_USERCONFIG,",
		"PIP_INDEX_URL, UV_DEFAULT_INDEX\n",
		"maze auth exec: cleared NPM_CONFIG_REGISTRY, UV_INDEX_URL",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, execToken) {
		t.Fatalf("verbose output holds the Token")
	}
	h.assertWorkspaceRemoved()
}

func TestAuthExecReturnsTheCommandsExitStatusAndCleansUp(t *testing.T) {
	h := newExecHarness(t, execOneFeedResponse)
	h.exit = 3
	_, _, err := h.execute("--feed", "acme/npm", "--purpose", "install", "--", "npm", "test")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("err = %v, want exit status 3", err)
	}
	h.assertWorkspaceRemoved()
}

func TestAuthExecCleansUpAfterASignal(t *testing.T) {
	h := newExecHarness(t, execOneFeedResponse)
	h.signal = syscall.SIGTERM
	_, _, err := h.execute("--feed", "acme/npm", "--purpose", "install", "--", "npm", "ci")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 128+int(syscall.SIGTERM) {
		t.Fatalf("err = %v, want the signal's exit status", err)
	}
	h.assertWorkspaceRemoved()
}

func TestAuthExecRefusesWhatItCannotConfigureBeforeRunningAnything(t *testing.T) {
	h := newExecHarness(t, strings.Replace(execOneFeedResponse, `"artifact_protocol":"npm"`, `"artifact_protocol":"maven"`, 1))
	_, _, err := h.execute("--feed", "acme/npm", "--purpose", "install", "--", "mvn", "install")
	if err == nil || !strings.Contains(err.Error(), `Artifact Protocol "maven"`) || h.child != nil {
		t.Fatalf("err = %v, ran = %v", err, h.child != nil)
	}
	h.assertWorkspaceRemoved()
}

func TestAuthExecRefusesOutputFlagsAndAMissingCommand(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--format", "shell", "--", "npm", "ci"}, want: "--format does not apply to maze auth exec"},
		{args: []string{"--json", "--", "npm", "ci"}, want: "--json does not apply to maze auth exec"},
		{args: []string{"--"}, want: "maze auth exec needs a command to run after --"},
	} {
		h := newExecHarness(t, execOneFeedResponse)
		_, _, err := h.execute(append([]string{"--feed", "acme/npm", "--purpose", "install"}, test.args...)...)
		if err == nil || !strings.Contains(err.Error(), test.want) || h.exchange != "" {
			t.Fatalf("args %v: err = %v, exchanged = %v", test.args, err, h.exchange != "")
		}
	}
}

func TestAuthExecStopsParsingFlagsAtTheCommand(t *testing.T) {
	h := newExecHarness(t, execOneFeedResponse)
	_, _, err := h.execute("--feed", "acme/npm", "--purpose", "install", "npm", "ci", "--verbose", "--feed", "x")
	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}
	if strings.Join(h.child.spec.Args, " ") != "ci --verbose --feed x" {
		t.Fatalf("args = %v", h.child.spec.Args)
	}
}
