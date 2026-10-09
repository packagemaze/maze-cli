package doctor

import (
	"sort"
	"strings"
)

type Status string

const (
	StatusPass     Status = "pass"
	StatusWarning  Status = "warning"
	StatusFail     Status = "fail"
	StatusNotSetUp Status = "not_set_up"
)

type ArtifactProtocol string

const (
	ProtocolNpm     ArtifactProtocol = "npm"
	ProtocolPyPI    ArtifactProtocol = "pypi"
	ProtocolUnknown ArtifactProtocol = ""
)

const (
	DocsFixAFailingInstall = "https://www.packagemaze.com/docs/fix-a-failing-install/"
	DocsAgentQuickstart    = "https://www.packagemaze.com/docs/agent-quickstart/"
	DocsSetUpNpmAndPnpm    = "https://www.packagemaze.com/docs/set-up-npm-and-pnpm/"
	DocsSetUpPipUvPoetry   = "https://www.packagemaze.com/docs/set-up-pip-uv-and-poetry/"
	DocsSetUpCI            = "https://www.packagemaze.com/docs/set-up-ci/"
)

const (
	githubActionsClient     = "github-actions"
	environmentSourcePrefix = "env:"
)

// Feed is one PackageMaze Feed named by a repository's configuration. BaseURL
// is the Feed Base URL with its trailing slash.
type Feed struct {
	Organization string `json:"organization"`
	Name         string `json:"name"`
	BaseURL      string `json:"feed_base_url"`
}

func (f Feed) Slug() string {
	return f.Organization + "/" + f.Name
}

func (f Feed) SimpleURL() string {
	return f.BaseURL + "simple/"
}

func (f Feed) LegacyURL() string {
	return f.BaseURL + "legacy/"
}

// Declaration is one place a package client is told where to install from or
// publish to: a setting in a committed file, an environment variable, or a
// setup-maze step in a workflow. URL never carries credentials.
type Declaration struct {
	Source   string           `json:"source"`
	Client   string           `json:"client"`
	Protocol ArtifactProtocol `json:"artifact_protocol"`
	Setting  string           `json:"setting"`
	URL      string           `json:"url,omitempty"`
	Feed     *Feed            `json:"feed,omitempty"`
}

// committed says whether a repository file, rather than a workflow step or the
// environment, names this Feed.
func (d Declaration) committed() bool {
	return d.Feed != nil && d.Client != githubActionsClient && !strings.HasPrefix(d.Source, environmentSourcePrefix)
}

type Check struct {
	Code        string `json:"code"`
	Status      Status `json:"status"`
	Summary     string `json:"summary"`
	Remediation string `json:"remediation"`
	Feed        string `json:"feed,omitempty"`
	Source      string `json:"source,omitempty"`
}

type NotChecked struct {
	Check  string `json:"check"`
	Reason string `json:"reason"`
}

type TokenStatus struct {
	Env               string   `json:"env"`
	Present           bool     `json:"present"`
	ClientCredentials []string `json:"client_credentials,omitempty"`
}

type FeedReport struct {
	Feed
	Protocol ArtifactProtocol `json:"artifact_protocol"`
	Sources  []string         `json:"sources"`
}

type Report struct {
	Directory     string        `json:"directory"`
	Status        Status        `json:"status"`
	Feeds         []FeedReport  `json:"feeds"`
	Configuration []Declaration `json:"configuration"`
	Token         TokenStatus   `json:"token"`
	Checks        []Check       `json:"checks"`
	NotChecked    []NotChecked  `json:"not_checked,omitempty"`
	NextStep      string        `json:"next_step"`
	Docs          []string      `json:"docs"`
}

func (r Report) Failed() bool {
	return r.Status == StatusFail
}

func check(code string, status Status, summary string, remediation string) Check {
	return Check{Code: code, Status: status, Summary: summary, Remediation: remediation}
}

func (c Check) at(source string) Check {
	c.Source = source
	return c
}

func (c Check) forFeed(feed Feed) Check {
	c.Feed = feed.Slug()
	return c
}

var statusRank = map[Status]int{StatusFail: 0, StatusWarning: 1, StatusPass: 2}

func sortChecks(checks []Check) {
	sort.SliceStable(checks, func(i, j int) bool {
		return statusRank[checks[i].Status] < statusRank[checks[j].Status]
	})
}

func overallStatus(checks []Check) Status {
	status := StatusPass
	for _, item := range checks {
		if item.Status == StatusFail {
			return StatusFail
		}
		if item.Status == StatusWarning {
			status = StatusWarning
		}
	}
	return status
}

func countStatus(checks []Check, status Status) int {
	count := 0
	for _, item := range checks {
		if item.Status == status {
			count++
		}
	}
	return count
}
