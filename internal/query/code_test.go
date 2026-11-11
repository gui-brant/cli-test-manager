package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

func codeRow(file string) TestRow {
	return TestRow{Number: 1, TestSpec: core.TestSpec{ID: "a", File: file}}
}

func TestTestCodeReadsTheFileRelativeToTheProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "checks", "a.mjs"), []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TestCode(root, codeRow("checks/a.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "line one\nline two\n" || got.Omitted != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestTestCodeCutsAHugeFileAndSaysHowMuch(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", MaxCodeBytes+10)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := TestCode(root, codeRow("big.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Text) != MaxCodeBytes || got.Omitted != 10 {
		t.Errorf("text %d bytes, omitted %d; want %d and 10", len(got.Text), got.Omitted, MaxCodeBytes)
	}
}

func TestTestCodeOfAMissingFileIsAnError(t *testing.T) {
	if _, err := TestCode(t.TempDir(), codeRow("nope.mjs")); err == nil {
		t.Error("a missing file gave no error")
	}
}
