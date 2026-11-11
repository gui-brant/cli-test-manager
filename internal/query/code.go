package query

import (
	"os"
	"path/filepath"
)

// MaxCodeBytes caps how much of a test file is shown.
const MaxCodeBytes = 1 << 20

// Code is the text of a test's file.
type Code struct {
	// Text is the file's content, cut at MaxCodeBytes.
	Text string
	// Omitted is how many bytes were cut off the end. It is 0 for a file that fits.
	Omitted int
}

// TestCode reads a test's file. root is the project folder, since the manifest's
// paths are relative to it.
func TestCode(root string, t TestRow) (Code, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(t.File)))
	if err != nil {
		return Code{}, err
	}
	if len(data) > MaxCodeBytes {
		return Code{Text: string(data[:MaxCodeBytes]), Omitted: len(data) - MaxCodeBytes}, nil
	}
	return Code{Text: string(data)}, nil
}
