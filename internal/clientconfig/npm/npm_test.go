package npm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/packagemaze/maze-cli/internal/clientconfig"
)

type recordedFiles map[string]string

func (files recordedFiles) Write(name string, content []byte) (string, error) {
	files[name] = string(content)
	return "/private/" + name, nil
}

func feeds(t *testing.T, baseURLs ...string) []clientconfig.Feed {
	t.Helper()
	var result []clientconfig.Feed
	for _, baseURL := range baseURLs {
		feed, err := clientconfig.NewFeed("acme/feed", "npm", baseURL)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, feed)
	}
	return result
}

func TestContributeOneFeed(t *testing.T) {
	files := recordedFiles{}
	contribution, err := Contribute(clientconfig.Request{
		Feeds: feeds(t, "https://pkg.packagemaze.com/acme/npm"),
		Token: "maze_ci_secret",
		Files: files,
	})
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	wantFile := "registry=https://pkg.packagemaze.com/acme/npm/\n" +
		"replace-registry-host=npmjs\n" +
		"//pkg.packagemaze.com/acme/npm/:_authToken=${MAZE_TOKEN}\n"
	if files["npmrc"] != wantFile {
		t.Fatalf("npmrc =\n%s\nwant\n%s", files["npmrc"], wantFile)
	}
	wantSet := []clientconfig.Var{
		{Name: "NPM_CONFIG_USERCONFIG", Value: "/private/npmrc"},
		{Name: "COREPACK_NPM_REGISTRY", Value: "https://pkg.packagemaze.com/acme/npm"},
		{Name: "COREPACK_NPM_TOKEN", Value: "maze_ci_secret"},
	}
	if !reflect.DeepEqual(contribution.Set, wantSet) {
		t.Fatalf("Set = %#v", contribution.Set)
	}
	if !reflect.DeepEqual(contribution.Clear, []string{"NPM_CONFIG_REGISTRY"}) {
		t.Fatalf("Clear = %#v", contribution.Clear)
	}
}

func TestContributeSeveralFeedsMakesTheFirstTheRegistryAndScopesEachCredential(t *testing.T) {
	files := recordedFiles{}
	contribution, err := Contribute(clientconfig.Request{
		Feeds: feeds(t, "https://pkg.packagemaze.com/acme/web/", "https://pkg.packagemaze.com/acme/tools"),
		Token: "maze_ci_secret",
		Files: files,
	})
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	wantFile := "registry=https://pkg.packagemaze.com/acme/web/\n" +
		"replace-registry-host=npmjs\n" +
		"//pkg.packagemaze.com/acme/web/:_authToken=${MAZE_TOKEN}\n" +
		"//pkg.packagemaze.com/acme/tools/:_authToken=${MAZE_TOKEN}\n"
	if files["npmrc"] != wantFile {
		t.Fatalf("npmrc =\n%s\nwant\n%s", files["npmrc"], wantFile)
	}
	if strings.Contains(files["npmrc"], "maze_ci_secret") {
		t.Fatalf("the user config holds the Token")
	}
	if contribution.Set[1] != (clientconfig.Var{Name: "COREPACK_NPM_REGISTRY", Value: "https://pkg.packagemaze.com/acme/web"}) {
		t.Fatalf("Corepack must use the first Feed: %#v", contribution.Set)
	}
}

func TestContributeLocalHTTPFeedKeepsThePortAndSetsNoYarnToken(t *testing.T) {
	files := recordedFiles{}
	contribution, err := Contribute(clientconfig.Request{
		Feeds: feeds(t, "http://127.0.0.1:8788/acme/npm"),
		Token: "t",
		Files: files,
	})
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	if !strings.Contains(files["npmrc"], "//127.0.0.1:8788/acme/npm/:_authToken=${MAZE_TOKEN}\n") {
		t.Fatalf("npmrc = %s", files["npmrc"])
	}
	for _, variable := range contribution.Set {
		if strings.HasPrefix(variable.Name, "YARN_") {
			t.Fatalf("Yarn's environment token is not URL-scoped; got %s", variable.Name)
		}
	}
}
