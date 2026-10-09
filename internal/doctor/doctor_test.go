package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fixtureFeedBaseURL = "https://pkg.packagemaze.com/acme/npm/"
	fixtureSimpleURL   = "https://pkg.packagemaze.com/acme/pypi/simple/"
	fixtureLegacyURL   = "https://pkg.packagemaze.com/acme/pypi/legacy/"
)

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type fakeProber struct {
	requests []ProbeRequest
	results  map[ArtifactProtocol]ProbeResult
}

func (f *fakeProber) Probe(_ context.Context, request ProbeRequest) ProbeResult {
	f.requests = append(f.requests, request)
	result, ok := f.results[request.Protocol]
	if !ok {
		result = ProbeResult{Outcome: ProbeAccepted, StatusCode: 200}
	}
	return result
}

func mapEnv(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func runDoctor(t *testing.T, config Config, env map[string]string, prober *fakeProber) Report {
	t.Helper()
	report, _, err := Run(context.Background(), config, Dependencies{Env: mapEnv(env), Prober: prober})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return report
}

func codes(report Report) []string {
	result := make([]string, 0, len(report.Checks))
	for _, item := range report.Checks {
		result = append(result, item.Code+":"+string(item.Status))
	}
	return result
}

func requireCode(t *testing.T, report Report, code string, status Status) Check {
	t.Helper()
	for _, item := range report.Checks {
		if item.Code == code && item.Status == status {
			return item
		}
	}
	t.Fatalf("missing check %s (%s) in %v", code, status, codes(report))
	return Check{}
}

func forbidCode(t *testing.T, report Report, code string) {
	t.Helper()
	for _, item := range report.Checks {
		if item.Code == code {
			t.Fatalf("unexpected check %s in %v", code, codes(report))
		}
	}
}

func requireNotChecked(t *testing.T, report Report, check string, reasonPart string) {
	t.Helper()
	for _, note := range report.NotChecked {
		if note.Check == check && strings.Contains(note.Reason, reasonPart) {
			return
		}
	}
	t.Fatalf("missing not_checked %s containing %q in %#v", check, reasonPart, report.NotChecked)
}

const healthyWorkflow = `name: CI
on: [push]
permissions:
  contents: read
  id-token: write
jobs:
  install:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - id: packagemaze-token
        uses: packagemaze/setup-maze@v0.0.4
        with:
          feed: acme/npm
          purpose: install
      - uses: actions/setup-node@v6
        with:
          registry-url: "https://pkg.packagemaze.com/acme/npm/"
      - run: npm ci
        env:
          NODE_AUTH_TOKEN: ${{ steps.packagemaze-token.outputs.token }}
`

func TestNotSetUpRepositoryReportsNoFeedAndMakesNoRequest(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".npmrc":       "registry=https://registry.npmjs.org/\n",
		"package.json": `{"name":"demo"}`,
	})
	prober := &fakeProber{}
	report := runDoctor(t, Config{Dir: dir}, map[string]string{"MAZE_TOKEN": "pm_present"}, prober)
	if report.Status != StatusNotSetUp {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}
	if len(prober.requests) != 0 {
		t.Fatalf("a request was made without a PackageMaze Feed: %#v", prober.requests)
	}
	if len(report.Configuration) != 1 || report.Configuration[0].URL != "https://registry.npmjs.org/" || report.Configuration[0].Feed != nil {
		t.Fatalf("configuration = %#v", report.Configuration)
	}
	if !strings.Contains(report.NextStep, DocsAgentQuickstart) {
		t.Fatalf("next step = %q", report.NextStep)
	}
	if report.FailureError() != nil {
		t.Fatalf("not set up must not exit non-zero")
	}
}

func TestHealthyNpmRepositoryPassesAndProbesOnce(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".npmrc":                   "registry=" + fixtureFeedBaseURL + "\nreplace-registry-host=npmjs\n",
		"package.json":             `{"name":"demo","publishConfig":{"registry":"` + fixtureFeedBaseURL + `"}}`,
		".github/workflows/ci.yml": healthyWorkflow,
	})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeAccepted, StatusCode: 200, Username: "kko"}}}
	report := runDoctor(t, Config{Dir: dir}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, prober)
	if report.Status != StatusPass {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}
	if len(report.Feeds) != 1 || report.Feeds[0].Slug() != "acme/npm" || report.Feeds[0].Protocol != ProtocolNpm {
		t.Fatalf("feeds = %#v", report.Feeds)
	}
	if got := strings.Join(report.Feeds[0].Sources, ","); got != "package.json,.npmrc,.github/workflows/ci.yml" {
		t.Fatalf("sources = %q", got)
	}
	if len(prober.requests) != 1 || prober.requests[0].Protocol != ProtocolNpm || prober.requests[0].Token != "pm_live_secret" || prober.requests[0].Feed.BaseURL != fixtureFeedBaseURL {
		t.Fatalf("requests = %#v", prober.requests)
	}
	ready := requireCode(t, report, "read_token_ready", StatusPass)
	if !strings.Contains(ready.Summary, "as kko") {
		t.Fatalf("ready summary = %q", ready.Summary)
	}
	requireCode(t, report, "client_config_ready", StatusPass)
	requireCode(t, report, "token_env_present", StatusPass)
	forbidCode(t, report, "ci_missing_id_token_permission")
	forbidCode(t, report, "ci_feed_mismatch")
}

func TestPythonRepositoryWithWrongShapesFails(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		"pyproject.toml": `[project]
name = "demo"

[[tool.uv.index]]
name = "packagemaze"
url = "https://pkg.packagemaze.com/acme/pypi/"
`,
		"requirements.txt": "--index-url " + fixtureSimpleURL + "\n--extra-index-url https://pypi.org/simple\nrequests==2.32.0\n",
	})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolPyPI: {Outcome: ProbeTokenRejected, StatusCode: 401}}}
	report := runDoctor(t, Config{Dir: dir}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, prober)
	if report.Status != StatusFail {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}
	wrong := requireCode(t, report, "client_config_wrong_registry", StatusFail)
	if !strings.Contains(wrong.Remediation, fixtureSimpleURL) || wrong.Source != "pyproject.toml" {
		t.Fatalf("wrong registry check = %#v", wrong)
	}
	bypass := requireCode(t, report, "index_bypasses_packagemaze", StatusFail)
	if !strings.Contains(bypass.Summary, "pypi.org") && !strings.Contains(bypass.Summary, "default = true") {
		t.Fatalf("bypass check = %#v", bypass)
	}
	rejected := requireCode(t, report, "read_token_rejected", StatusFail)
	if !strings.Contains(rejected.Summary, "HTTP 401") || rejected.Feed != "acme/pypi" {
		t.Fatalf("rejected check = %#v", rejected)
	}
	if len(prober.requests) != 1 || prober.requests[0].Protocol != ProtocolPyPI {
		t.Fatalf("requests = %#v", prober.requests)
	}
	if !errors.Is(report.FailureError(), ErrChecksFailed) {
		t.Fatalf("failure error = %v", report.FailureError())
	}
	requireNotChecked(t, report, "token_rejection_reason", "does not tell")
	if report.Checks[0].Status != StatusFail || report.Checks[len(report.Checks)-1].Status == StatusFail {
		t.Fatalf("checks are not sorted most severe first: %v", codes(report))
	}
}

func TestReportsNeverContainTokenSecrets(t *testing.T) {
	const envSecret = "pm_env_secret_value_0123"
	dir := writeFixture(t, map[string]string{
		".npmrc":           "registry=" + fixtureFeedBaseURL + "\n//pkg.packagemaze.com/acme/npm/:_authToken=pm_committed_secret_456\n",
		"requirements.txt": "--index-url https://__token__:pm_requirements_secret_789@pkg.packagemaze.com/acme/pypi/simple/\n",
	})
	env := map[string]string{"MAZE_TOKEN": envSecret, "PIP_INDEX_URL": "https://__token__:" + envSecret + "@pkg.packagemaze.com/acme/pypi/simple/"}
	report := runDoctor(t, Config{Dir: dir}, env, &fakeProber{})
	requireCode(t, report, "client_config_contains_literal_token", StatusFail)
	for _, format := range []Format{FormatMarkdown, FormatJSON} {
		var out bytes.Buffer
		if err := Write(report, format, &out, envSecret); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{envSecret, "pm_committed_secret_456", "pm_requirements_secret_789"} {
			if strings.Contains(out.String(), secret) {
				t.Fatalf("%s output leaked %s:\n%s", format, secret, out.String())
			}
		}
	}
	for _, declaration := range report.Configuration {
		if strings.Contains(declaration.URL, "@") {
			t.Fatalf("declaration kept credentials: %#v", declaration)
		}
	}
}

func TestOtherHostsAreNeverContacted(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".npmrc": "registry=https://registry.example.test/acme/npm/\n",
	})
	prober := &fakeProber{}
	report := runDoctor(t, Config{Dir: dir}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, prober)
	if report.Status != StatusNotSetUp || len(prober.requests) != 0 {
		t.Fatalf("status = %q, requests = %#v", report.Status, prober.requests)
	}
}

func TestExplicitFeedWithoutConfigurationProbesNpmThenPyPI(t *testing.T) {
	dir := writeFixture(t, map[string]string{})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{
		ProtocolNpm:  {Outcome: ProbeFeedNotFound, StatusCode: 404},
		ProtocolPyPI: {Outcome: ProbeAccepted, StatusCode: 200},
	}}
	report := runDoctor(t, Config{Dir: dir, Feed: "acme/pypi"}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, prober)
	if len(prober.requests) != 2 || prober.requests[0].Protocol != ProtocolNpm || prober.requests[1].Protocol != ProtocolPyPI {
		t.Fatalf("requests = %#v", prober.requests)
	}
	ready := requireCode(t, report, "read_token_ready", StatusPass)
	if !strings.Contains(ready.Summary, fixtureSimpleURL) {
		t.Fatalf("ready summary = %q", ready.Summary)
	}
	if report.Status != StatusPass || report.Feeds[0].Sources[0] != "--feed" {
		t.Fatalf("report = %#v", report.Feeds)
	}
}

func TestNearestConfigurationInAMonorepoIsFound(t *testing.T) {
	root := writeFixture(t, map[string]string{
		".npmrc":                    "registry=" + fixtureFeedBaseURL + "\n",
		"packages/app/package.json": `{"name":"app"}`,
	})
	report := runDoctor(t, Config{Dir: filepath.Join(root, "packages", "app"), Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	if len(report.Feeds) != 1 || report.Feeds[0].Sources[0] != ".npmrc" {
		t.Fatalf("feeds = %#v", report.Feeds)
	}
	requireNotChecked(t, report, "live_token_check", "--offline")
}

func TestWithoutARepositoryRootOnlyTheGivenDirectoryIsRead(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, ".npmrc"), []byte("registry="+fixtureFeedBaseURL+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{}, &fakeProber{})
	if report.Status != StatusNotSetUp || len(report.Configuration) != 0 {
		t.Fatalf("a parent outside any repository was read: %#v", report.Configuration)
	}
	requireNotChecked(t, report, "repository_root", "only that directory")
}

func TestSymlinksEscapingTheRepositoryAreNotRead(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.npmrc"), []byte("registry="+fixtureFeedBaseURL+"\n//pkg.packagemaze.com/acme/npm/:_authToken=pm_outside_secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := writeFixture(t, map[string]string{})
	if err := os.Symlink(filepath.Join(outside, "secret.npmrc"), filepath.Join(dir, ".npmrc")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{}, &fakeProber{})
	if report.Status != StatusNotSetUp || len(report.Configuration) != 0 {
		t.Fatalf("a symlink outside the repository was followed: %#v", report.Configuration)
	}
}

func TestPipConfContinuationLinesCountAsIndexes(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		"pip.conf": "[global]\nindex-url = " + fixtureSimpleURL + "\nextra-index-url =\n    https://pypi.org/simple\n    https://mirror.example.test/simple\n",
	})
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	bypasses := 0
	for _, item := range report.Checks {
		if item.Code == "index_bypasses_packagemaze" {
			bypasses++
		}
	}
	if bypasses != 2 || len(report.Configuration) != 3 {
		t.Fatalf("checks = %v configuration = %#v", codes(report), report.Configuration)
	}
}

func TestPnpmPlaceholderAndRegistryDescription(t *testing.T) {
	files := map[string]string{
		".npmrc":         "registry=" + fixtureFeedBaseURL + "\n//pkg.packagemaze.com/acme/npm/:_authToken=${MAZE_TOKEN}\n",
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
	}
	report := runDoctor(t, Config{Dir: writeFixture(t, files), Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	requireCode(t, report, "pnpm_ignores_committed_credential_placeholder", StatusWarning)
	requireCode(t, report, "pnpm_packagemaze_registry_description_missing", StatusWarning)
	if report.Status != StatusWarning {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}

	files["pnpm-workspace.yaml"] = "packages:\n  - packages/*\nregistries:\n  " + fixtureFeedBaseURL + ":\n    serverType: npm\n    supportsTimeField: true\n"
	delete(files, "pnpm-lock.yaml")
	files[".npmrc"] = "registry=" + fixtureFeedBaseURL + "\n"
	report = runDoctor(t, Config{Dir: writeFixture(t, files), Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	requireCode(t, report, "pnpm_packagemaze_registry_description_ready", StatusPass)
	if report.Status != StatusPass {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}
}

func TestMissingTokenFailsOnlyWhenAFeedIsConfigured(t *testing.T) {
	dir := writeFixture(t, map[string]string{".npmrc": "registry=" + fixtureFeedBaseURL + "\n"})
	report := runDoctor(t, Config{Dir: dir}, map[string]string{}, &fakeProber{})
	missing := requireCode(t, report, "token_env_missing", StatusFail)
	if !strings.Contains(missing.Remediation, `export MAZE_TOKEN="<Token Secret>"`) || !strings.Contains(missing.Remediation, DocsFixAFailingInstall) {
		t.Fatalf("remediation = %q", missing.Remediation)
	}
	report = runDoctor(t, Config{Dir: dir}, map[string]string{"NODE_AUTH_TOKEN": "pm_ci_secret"}, &fakeProber{})
	requireCode(t, report, "client_credential_present", StatusPass)
	forbidCode(t, report, "token_env_missing")
	if report.Status != StatusPass {
		t.Fatalf("status = %q, checks %v", report.Status, codes(report))
	}
}

func TestWorkflowWithoutIDTokenPermissionFails(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".github/workflows/publish.yml": `name: Publish
on: [push]
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: packagemaze/setup-maze@v0.0.4
        with:
          feed: acme/pypi
          purpose: publish
          package: demo
      - run: twine upload dist/*
`,
	})
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	missing := requireCode(t, report, "ci_missing_id_token_permission", StatusFail)
	if missing.Source != ".github/workflows/publish.yml" {
		t.Fatalf("source = %q", missing.Source)
	}
	if len(report.Feeds) != 1 || report.Feeds[0].Protocol != ProtocolPyPI || report.Configuration[0].Setting != "setup-maze feed (publish)" {
		t.Fatalf("feeds = %#v configuration = %#v", report.Feeds, report.Configuration)
	}
}

func TestPoetryAndPipenvOneIndexRule(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		"pyproject.toml": `[[tool.poetry.source]]
name = "packagemaze"
url = "` + fixtureSimpleURL + `"
priority = "supplemental"
`,
		"Pipfile": `[[source]]
name = "pypi"
url = "https://pypi.org/simple"
verify_ssl = true

[[source]]
name = "packagemaze"
url = "` + fixtureSimpleURL + `"
verify_ssl = true
`,
	})
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	bypasses := 0
	for _, item := range report.Checks {
		if item.Code == "index_bypasses_packagemaze" {
			bypasses++
		}
	}
	if bypasses != 2 {
		t.Fatalf("expected a bypass per file, got %v", codes(report))
	}
}

func TestUvIndexMatchingSetupInstructionsIsReady(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		"pyproject.toml": `[[tool.uv.index]]
name = "packagemaze"
url = "` + fixtureSimpleURL + `"
publish-url = "` + fixtureLegacyURL + `"
default = true
authenticate = "always"
`,
	})
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{"UV_INDEX_PACKAGEMAZE_PASSWORD": "pm_uv_secret"}, &fakeProber{})
	requireCode(t, report, "client_config_ready", StatusPass)
	forbidCode(t, report, "client_config_incomplete")
	forbidCode(t, report, "index_bypasses_packagemaze")
	if report.Status != StatusPass || report.Token.Present || len(report.Token.ClientCredentials) != 1 {
		t.Fatalf("report = %q token = %#v", report.Status, report.Token)
	}
}

func TestProbingStopsAfterPackageMazeIsUnreachable(t *testing.T) {
	dir := writeFixture(t, map[string]string{
		".npmrc":         "registry=" + fixtureFeedBaseURL + "\n@other:registry=https://pkg.packagemaze.com/acme/second/\n",
		"pyproject.toml": "[[tool.uv.index]]\nname = \"packagemaze\"\nurl = \"" + fixtureSimpleURL + "\"\ndefault = true\nauthenticate = \"always\"\n",
	})
	prober := &fakeProber{results: map[ArtifactProtocol]ProbeResult{ProtocolNpm: {Outcome: ProbeUnreachable, Err: errors.New("dial tcp: timeout")}}}
	report := runDoctor(t, Config{Dir: dir}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, prober)
	if len(prober.requests) != 1 {
		t.Fatalf("probing continued after an unreachable answer: %#v", prober.requests)
	}
	requireCode(t, report, "packagemaze_unreachable", StatusWarning)
	requireNotChecked(t, report, "live_token_check", "could not reach PackageMaze")
}

func TestJSONOutputCarriesStableFields(t *testing.T) {
	dir := writeFixture(t, map[string]string{".npmrc": "registry=" + fixtureFeedBaseURL + "\n"})
	report := runDoctor(t, Config{Dir: dir, Offline: true}, map[string]string{"MAZE_TOKEN": "pm_live_secret"}, &fakeProber{})
	var out bytes.Buffer
	if err := Write(report, FormatJSON, &out, "pm_live_secret"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Status string `json:"status"`
		Feeds  []struct {
			Organization string `json:"organization"`
			Name         string `json:"name"`
			FeedBaseURL  string `json:"feed_base_url"`
			Protocol     string `json:"artifact_protocol"`
		} `json:"feeds"`
		Checks []struct {
			Code        string `json:"code"`
			Status      string `json:"status"`
			Summary     string `json:"summary"`
			Remediation string `json:"remediation"`
		} `json:"checks"`
		Token struct {
			Env     string `json:"env"`
			Present bool   `json:"present"`
		} `json:"token"`
		NextStep string `json:"next_step"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if payload.Status != "pass" || len(payload.Feeds) != 1 || payload.Feeds[0].FeedBaseURL != fixtureFeedBaseURL || payload.Feeds[0].Protocol != "npm" {
		t.Fatalf("payload = %s", out.String())
	}
	if !payload.Token.Present || payload.Token.Env != "MAZE_TOKEN" || len(payload.Checks) == 0 || payload.Checks[0].Remediation == "" {
		t.Fatalf("payload = %s", out.String())
	}
}

func TestResolveRejectsInsecurePackageClientURLAndBadFeed(t *testing.T) {
	if _, err := Resolve(Config{PackageClientURL: "http://pkg.example.test"}, Dependencies{Env: mapEnv(nil)}); err == nil {
		t.Fatal("http package client URL was accepted")
	}
	if _, err := Resolve(Config{PackageClientURL: "http://127.0.0.1:8787", AllowInsecureLocalhost: true}, Dependencies{Env: mapEnv(nil)}); err != nil {
		t.Fatalf("localhost http was refused: %v", err)
	}
	if _, err := Resolve(Config{Feed: "not-a-feed"}, Dependencies{Env: mapEnv(nil)}); err == nil {
		t.Fatal("malformed --feed was accepted")
	}
	if _, err := Resolve(Config{Format: "yaml"}, Dependencies{Env: mapEnv(nil)}); err == nil {
		t.Fatal("unknown format was accepted")
	}
}
