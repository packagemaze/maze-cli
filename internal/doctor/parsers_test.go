package doctor

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTOMLReadsTheDocumentedPythonShapes(t *testing.T) {
	root := parseTOML(`
[project]
name = "demo" # a comment

[[tool.uv.index]]
name = "packagemaze"
url = "https://pkg.packagemaze.com/acme/pypi/simple/"
publish-url = "https://pkg.packagemaze.com/acme/pypi/legacy/"
default = true
authenticate = "always"

[[tool.poetry.source]]
name = "packagemaze"
url = 'https://pkg.packagemaze.com/acme/pypi/simple/'
priority = "primary"

[tool.uv]
extra-index-url = ["https://pypi.org/simple", "https://example.test/simple/"]

[install]
registry = { url = "https://pkg.packagemaze.com/acme/npm/", token = "pm_literal" }
`)
	indexes := tables(root, "tool", "uv", "index")
	if len(indexes) != 1 || asString(indexes[0]["publish-url"]) != "https://pkg.packagemaze.com/acme/pypi/legacy/" {
		t.Fatalf("uv index = %#v", indexes)
	}
	if !asBool(indexes[0]["default"]) {
		t.Fatalf("uv default = %#v", indexes[0]["default"])
	}
	sources := tables(root, "tool", "poetry", "source")
	if len(sources) != 1 || asString(sources[0]["priority"]) != "primary" {
		t.Fatalf("poetry source = %#v", sources)
	}
	extra := asStrings(tables(root, "tool", "uv")[0]["extra-index-url"])
	if !reflect.DeepEqual(extra, []string{"https://pypi.org/simple", "https://example.test/simple/"}) {
		t.Fatalf("extra-index-url = %#v", extra)
	}
	registry := tables(root, "install", "registry")
	if len(registry) != 1 || asString(registry[0]["token"]) != "pm_literal" {
		t.Fatalf("bun registry = %#v", registry)
	}
}

func TestQuotedStringEscapesStayWithinUnicode(t *testing.T) {
	if parsed, ok := parseQuotedString(`"caf\u00e9 \U0001F600"`); !ok || parsed != "café 😀" {
		t.Fatalf("escapes = %q %v", parsed, ok)
	}
	if _, ok := parseQuotedString(`"\UFFFFFFFF"`); ok {
		t.Fatal("a code point beyond unicode.MaxRune was accepted")
	}
}

func TestDeeplyNestedFlowCollectionsStayBounded(t *testing.T) {
	nested := strings.Repeat("[", 2000) + "x" + strings.Repeat("]", 2000)
	root := parseTOML("value = " + nested + "\n")
	if _, ok := root["value"]; !ok {
		t.Fatalf("nested value was dropped: %#v", root)
	}
	workflow := asMap(parseYAML("key: " + nested + "\n"))
	if _, ok := workflow["key"]; !ok {
		t.Fatalf("nested YAML value was dropped: %#v", workflow)
	}
}

func TestParseYAMLReadsWorkflowsAndPnpmRegistries(t *testing.T) {
	workflow := asMap(parseYAML(`
name: CI
on:
  push:
    branches: [main]
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
      - run: |
          echo "registry=https://pkg.packagemaze.com/acme/npm/" > .npmrc
          npm ci
        env:
          NODE_AUTH_TOKEN: ${{ steps.packagemaze-token.outputs.token }}
`))
	if asString(asMap(workflow["permissions"])["id-token"]) != "write" {
		t.Fatalf("permissions = %#v", workflow["permissions"])
	}
	steps := asList(asMap(asMap(workflow["jobs"])["install"])["steps"])
	if len(steps) != 3 {
		t.Fatalf("steps = %#v", steps)
	}
	setup := asMap(steps[1])
	if asString(setup["uses"]) != "packagemaze/setup-maze@v0.0.4" || asString(asMap(setup["with"])["feed"]) != "acme/npm" {
		t.Fatalf("setup step = %#v", setup)
	}
	run := asMap(steps[2])
	if asString(asMap(run["env"])["NODE_AUTH_TOKEN"]) != "${{ steps.packagemaze-token.outputs.token }}" {
		t.Fatalf("run step = %#v", run)
	}

	workspace := asMap(parseYAML(`
packages:
- "packages/*"
registries:
  https://pkg.packagemaze.com/acme/npm/:
    serverType: npm
    supportsTimeField: true
`))
	registries := asMap(workspace["registries"])
	entry := asMap(registries["https://pkg.packagemaze.com/acme/npm/"])
	if asString(entry["serverType"]) != "npm" || !asBool(entry["supportsTimeField"]) {
		t.Fatalf("registries = %#v", registries)
	}
	if !reflect.DeepEqual(asList(workspace["packages"]), []any{"packages/*"}) {
		t.Fatalf("packages = %#v", workspace["packages"])
	}
}

func TestRedactionNeverKeepsTokenShapesOrURLCredentials(t *testing.T) {
	if got := redactURL("https://__token__:pm_abc-123@pkg.packagemaze.com/acme/pypi/simple/"); got != "https://pkg.packagemaze.com/acme/pypi/simple/" {
		t.Fatalf("redactURL = %q", got)
	}
	if got := redactText("token pm_abc_123 and plain-secret", "plain-secret"); got != "token [redacted] and [redacted]" {
		t.Fatalf("redactText = %q", got)
	}
}
