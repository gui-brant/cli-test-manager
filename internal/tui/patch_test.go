package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bubble Tea's own start-up question about the terminal's background colour
// would make every command wait about five seconds on a terminal that never
// answers. We use a patched copy of the library without it (see
// third_party/bubbletea/PATCHED.md); these checks fail if an upgrade brings it back.

const patchedDir = "../../third_party/bubbletea"

func TestTheStartupQueryStaysRemovedFromTheLibraryCopy(t *testing.T) {
	if _, err := os.Stat(filepath.Join(patchedDir, "tea_init.go")); err == nil {
		t.Fatal("third_party/bubbletea/tea_init.go is back: it makes every command wait on a terminal that does not answer. Delete it (see PATCHED.md).")
	}

	// Nothing else in the copy may ask the terminal for its colours at start-up.
	files, err := filepath.Glob(filepath.Join(patchedDir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found in the library copy: %v", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if i := strings.Index(text, "func init()"); i >= 0 && strings.Contains(text[i:], "HasDarkBackground") {
			t.Errorf("%s asks for the background colour in an init function", f)
		}
	}
}

func TestTheModuleUsesTheLibraryCopy(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	want := "replace github.com/charmbracelet/bubbletea => ./third_party/bubbletea"
	if !strings.Contains(string(mod), want) {
		t.Errorf("go.mod lacks %q, so the unpatched library would be used", want)
	}
}
