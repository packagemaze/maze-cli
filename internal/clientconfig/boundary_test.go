package clientconfig

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The core selects a Contributor by Artifact Protocol; only the composition
// root may import a concrete one.
func TestCoreImportsNoConcreteContributor(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			if strings.Contains(spec.Path.Value, "/internal/") {
				t.Fatalf("%s imports %s", source, spec.Path.Value)
			}
		}
	}
}
