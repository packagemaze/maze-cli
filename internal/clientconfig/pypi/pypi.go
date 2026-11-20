// Package pypi configures PyPI-protocol package clients (pip and uv) for a
// command through their index variables, with the Token in each Feed's
// Simple URL as PackageMaze's pip setup instructions put it.
package pypi

import (
	"net/url"
	"strings"

	"github.com/packagemaze/maze-cli/internal/clientconfig"
)

// Contribute makes the first Feed the default index and every other Feed an
// extra one. pip merges all indexes; uv searches the extra ones, in order,
// before the default. Variables that would add another index are cleared, as
// are uv's deprecated names for the ones set here.
func Contribute(request clientconfig.Request) (clientconfig.Contribution, error) {
	indexes := make([]string, 0, len(request.Feeds))
	for _, feed := range request.Feeds {
		indexes = append(indexes, simpleURL(feed, request.Token))
	}
	set := []clientconfig.Var{
		{Name: "PIP_INDEX_URL", Value: indexes[0]},
		{Name: "UV_DEFAULT_INDEX", Value: indexes[0]},
	}
	cleared := []string{"UV_INDEX_URL", "UV_EXTRA_INDEX_URL"}
	if extra := strings.Join(indexes[1:], " "); extra != "" {
		set = append(set, clientconfig.Var{Name: "PIP_EXTRA_INDEX_URL", Value: extra}, clientconfig.Var{Name: "UV_INDEX", Value: extra})
	} else {
		cleared = append(cleared, "PIP_EXTRA_INDEX_URL", "UV_INDEX")
	}
	return clientconfig.Contribution{Set: set, Clear: cleared}, nil
}

func simpleURL(feed clientconfig.Feed, token string) string {
	index := *feed.BaseURL
	index.User = url.UserPassword("__token__", token)
	index.Path += "/simple/"
	return index.String()
}
