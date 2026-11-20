package clientconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func mustFeed(t *testing.T, reference, protocol, baseURL string) Feed {
	t.Helper()
	feed, err := NewFeed(reference, protocol, baseURL)
	if err != nil {
		t.Fatalf("NewFeed: %v", err)
	}
	return feed
}

type recordedFiles map[string]string

func (files recordedFiles) Write(name string, content []byte) (string, error) {
	files[name] = string(content)
	return "/private/" + name, nil
}

func TestNewFeedKeepsTheFeedURLWithoutATrailingSlash(t *testing.T) {
	feed := mustFeed(t, "acme/npm", "npm", "https://pkg.packagemaze.com/acme/npm/")
	if feed.BaseURL.String() != "https://pkg.packagemaze.com/acme/npm" {
		t.Fatalf("BaseURL = %s", feed.BaseURL)
	}
	for _, bad := range []string{"", "pkg.packagemaze.com/acme/npm", "ftp://pkg.packagemaze.com/acme/npm", "https://user:pw@pkg.packagemaze.com/acme/npm", "https://pkg.packagemaze.com/acme/npm?x=1", "https://pkg.packagemaze.com/a\nb"} {
		if _, err := NewFeed("acme/npm", "npm", bad); err == nil || !strings.Contains(err.Error(), "Feed acme/npm") {
			t.Fatalf("NewFeed(%q) err = %v", bad, err)
		}
	}
}

func TestConfigureSelectsOneContributorPerArtifactProtocolInRequestOrder(t *testing.T) {
	var calls []string
	contributor := func(name string) Contributor {
		return func(request Request) (Contribution, error) {
			var references []string
			for _, feed := range request.Feeds {
				references = append(references, feed.Reference)
			}
			calls = append(calls, name+":"+strings.Join(references, ","))
			return Contribution{Set: []Var{{Name: strings.ToUpper(name) + "_URL", Value: request.Token}}, Clear: []string{strings.ToUpper(name) + "_EXTRA"}}, nil
		}
	}
	registry := Registry{"pypi": contributor("pypi"), "npm": contributor("npm")}
	feeds := []Feed{
		mustFeed(t, "acme/py", "pypi", "https://pkg.packagemaze.com/acme/py"),
		mustFeed(t, "acme/web", "npm", "https://pkg.packagemaze.com/acme/web"),
		mustFeed(t, "acme/py2", "pypi", "https://pkg.packagemaze.com/acme/py2"),
	}
	contribution, err := Configure(feeds, "maze_ci_secret", registry, recordedFiles{})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if strings.Join(calls, " ") != "pypi:acme/py,acme/py2 npm:acme/web" {
		t.Fatalf("calls = %v", calls)
	}
	wantSet := []Var{{Name: TokenEnv, Value: "maze_ci_secret"}, {Name: "PYPI_URL", Value: "maze_ci_secret"}, {Name: "NPM_URL", Value: "maze_ci_secret"}}
	if !reflect.DeepEqual(contribution.Set, wantSet) || strings.Join(contribution.Clear, ",") != "PYPI_EXTRA,NPM_EXTRA" {
		t.Fatalf("contribution = %#v", contribution)
	}
}

func TestConfigureFailsClosed(t *testing.T) {
	npmOnly := Registry{"npm": func(Request) (Contribution, error) { return Contribution{}, nil }}
	maven := mustFeed(t, "acme/jars", "maven", "https://pkg.packagemaze.com/acme/jars")
	if _, err := Configure([]Feed{maven}, "t", npmOnly, recordedFiles{}); err == nil || err.Error() != `Feed acme/jars uses Artifact Protocol "maven", which maze auth exec cannot configure` {
		t.Fatalf("unknown protocol: err = %v", err)
	}
	if _, err := Configure(nil, "t", npmOnly, recordedFiles{}); err == nil {
		t.Fatalf("no Feeds: expected an error")
	}
	failing := Registry{"npm": func(Request) (Contribution, error) { return Contribution{}, errors.New("disk full") }}
	npmFeed := mustFeed(t, "acme/npm", "npm", "https://pkg.packagemaze.com/acme/npm")
	if _, err := Configure([]Feed{npmFeed}, "t", failing, recordedFiles{}); err == nil || err.Error() != "configure npm package clients: disk full" {
		t.Fatalf("contributor failure: err = %v", err)
	}
	claimsToken := Registry{"npm": func(Request) (Contribution, error) { return Contribution{Clear: []string{"maze_token"}}, nil }}
	if _, err := Configure([]Feed{npmFeed}, "t", claimsToken, recordedFiles{}); err == nil || !strings.Contains(err.Error(), "names maze_token twice") {
		t.Fatalf("duplicate name: err = %v", err)
	}
}

func TestEnvironReplacesAndClearsOwnedNamesWhateverTheirCase(t *testing.T) {
	inherited := []string{"PATH=/bin", "npm_config_userconfig=/home/u/.npmrc", "PIP_EXTRA_INDEX_URL=https://pypi.org/simple", "MAZE_TOKEN=old", "=C:=C:\\"}
	contribution := Contribution{
		Set:   []Var{{Name: TokenEnv, Value: "new"}, {Name: "NPM_CONFIG_USERCONFIG", Value: "/private/npmrc"}},
		Clear: []string{"PIP_EXTRA_INDEX_URL"},
	}
	got := Environ(inherited, contribution)
	want := []string{"PATH=/bin", "=C:=C:\\", "MAZE_TOKEN=new", "NPM_CONFIG_USERCONFIG=/private/npmrc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ = %#v\nwant      %#v", got, want)
	}
	if strings.Join(contribution.Names(), ",") != "MAZE_TOKEN,NPM_CONFIG_USERCONFIG" {
		t.Fatalf("Names = %v", contribution.Names())
	}
}

func TestWorkspaceWritesOwnerOnlyFilesAndRemovesThem(t *testing.T) {
	root := t.TempDir()
	filesystem := OSFilesystem()
	filesystem.MkdirTemp = func(_ string, pattern string) (string, error) { return os.MkdirTemp(root, pattern) }
	workspace, err := NewWorkspace(filesystem)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	path, err := workspace.Write("npmrc", []byte("registry=x\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Dir(path) != workspace.Dir() {
		t.Fatalf("path = %s, dir = %s", path, workspace.Dir())
	}
	if _, err := workspace.Write("npmrc", []byte("again")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Write: err = %v, want it to refuse an existing file", err)
	}
	if runtime.GOOS != "windows" {
		assertMode(t, workspace.Dir(), 0o700)
		assertMode(t, path, 0o600)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(workspace.Dir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace still exists: %v", err)
	}
}

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
	}
}
