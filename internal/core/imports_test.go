package core

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreImportsOnlyStandardLibrary enforces the dependency rule in doc.go:
// core imports nothing but the Go standard library, and never another package
// of this module. Standard-library import paths have no dot in their first
// element ("fmt", "go/parser"); module paths do ("github.com/...").
func TestCoreImportsOnlyStandardLibrary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // tests may use more; the rule is about shipped code
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", f, err)
		}
		checked++
		for _, imp := range parsed.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %q: core may import only the standard library", f, path)
			}
		}
	}

	// Guard against the check passing because it looked at nothing.
	if checked < 5 {
		t.Errorf("checked only %d source files; the test is not finding core's files", checked)
	}
}
