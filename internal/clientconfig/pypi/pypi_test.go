package pypi

import (
	"reflect"
	"testing"

	"github.com/packagemaze/maze-cli/internal/clientconfig"
)

func feed(t *testing.T, reference string) clientconfig.Feed {
	t.Helper()
	result, err := clientconfig.NewFeed(reference, "pypi", "https://pkg.packagemaze.com/"+reference)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestContributeOneFeedIsTheOnlyIndex(t *testing.T) {
	contribution, err := Contribute(clientconfig.Request{Feeds: []clientconfig.Feed{feed(t, "acme/pypi")}, Token: "maze_ci_secret"})
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	index := "https://__token__:maze_ci_secret@pkg.packagemaze.com/acme/pypi/simple/"
	wantSet := []clientconfig.Var{{Name: "PIP_INDEX_URL", Value: index}, {Name: "UV_DEFAULT_INDEX", Value: index}}
	if !reflect.DeepEqual(contribution.Set, wantSet) {
		t.Fatalf("Set = %#v", contribution.Set)
	}
	wantClear := []string{"UV_INDEX_URL", "UV_EXTRA_INDEX_URL", "PIP_EXTRA_INDEX_URL", "UV_INDEX"}
	if !reflect.DeepEqual(contribution.Clear, wantClear) {
		t.Fatalf("Clear = %#v", contribution.Clear)
	}
}

func TestContributeSeveralFeedsAddsExtraIndexes(t *testing.T) {
	contribution, err := Contribute(clientconfig.Request{
		Feeds: []clientconfig.Feed{feed(t, "acme/py"), feed(t, "acme/ml"), feed(t, "acme/data")},
		Token: "maze/ci:secret",
	})
	if err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	first := "https://__token__:maze%2Fci%3Asecret@pkg.packagemaze.com/acme/py/simple/"
	extra := "https://__token__:maze%2Fci%3Asecret@pkg.packagemaze.com/acme/ml/simple/ https://__token__:maze%2Fci%3Asecret@pkg.packagemaze.com/acme/data/simple/"
	wantSet := []clientconfig.Var{
		{Name: "PIP_INDEX_URL", Value: first},
		{Name: "UV_DEFAULT_INDEX", Value: first},
		{Name: "PIP_EXTRA_INDEX_URL", Value: extra},
		{Name: "UV_INDEX", Value: extra},
	}
	if !reflect.DeepEqual(contribution.Set, wantSet) {
		t.Fatalf("Set = %#v", contribution.Set)
	}
	if !reflect.DeepEqual(contribution.Clear, []string{"UV_INDEX_URL", "UV_EXTRA_INDEX_URL"}) {
		t.Fatalf("Clear = %#v", contribution.Clear)
	}
}
