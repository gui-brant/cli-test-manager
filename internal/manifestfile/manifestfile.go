// Package manifestfile is the disk side of the manifest: finding
// test-manifest.yaml, reading it, and checking that the test files it lists
// exist. Parsing and validating are in the manifest package, which does no
// disk access.
package manifestfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifest"
)

// FileName is the manifest's name, at the root of a project.
const FileName = "test-manifest.yaml"

// Loaded is a manifest read from disk, with the paths worked out.
type Loaded struct {
	Manifest *core.Manifest
	// Path is the absolute path of the manifest file.
	Path string
	// Root is the project folder: the folder that holds the manifest. All
	// paths in the manifest are relative to it.
	Root string
	// HistoryPath is the absolute path of the project's history file.
	HistoryPath string
}

// Find walks up from dir through its parent folders to the filesystem root and
// returns the path of the nearest test-manifest.yaml, the way git finds .git.
// A folder that merely has that name is skipped. When none is found it returns
// an error wrapping core.ErrNotFound.
func Find(dir string) (string, error) {
	start, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("manifestfile: %w", err)
	}
	for cur := start; ; {
		candidate := filepath.Join(cur, FileName)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && info.Mode().IsRegular():
			return candidate, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("manifestfile: %w", err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("manifestfile: no %s found in %s or any folder above it: %w", FileName, start, core.ErrNotFound)
		}
		cur = parent
	}
}

// Load reads, parses and validates the manifest at path. Every validation
// problem is reported together, and each error names the file.
func Load(path string) (*Loaded, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("manifestfile: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("manifestfile: %w", err)
	}
	defer f.Close()

	m, err := manifest.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	if err := manifest.Report(manifest.Validate(m)); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}

	root := filepath.Dir(abs)
	return &Loaded{
		Manifest:    m,
		Path:        abs,
		Root:        root,
		HistoryPath: filepath.Join(root, filepath.FromSlash(m.History)),
	}, nil
}

// CheckFiles reports every test file that is missing, is not a regular file,
// or resolves (through a symbolic link) to somewhere outside the project. It
// returns all the problems, not just the first, so a project can prove its
// test list matches its test files in one run.
func CheckFiles(l *Loaded) []error {
	realRoot, err := filepath.EvalSymlinks(l.Root)
	if err != nil {
		return []error{fmt.Errorf("manifestfile: project folder %s: %w", l.Root, err)}
	}

	var errs []error
	for i, t := range l.Manifest.Tests {
		where := fmt.Sprintf("tests[%d] (%s)", i, t.ID)
		real, err := filepath.EvalSymlinks(filepath.Join(l.Root, filepath.FromSlash(t.File)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: file %q does not exist", where, t.File))
			} else {
				errs = append(errs, fmt.Errorf("%s: file %q: %w", where, t.File, err))
			}
			continue
		}
		if rel, err := filepath.Rel(realRoot, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			errs = append(errs, fmt.Errorf("%s: file %q resolves to a place outside the project", where, t.File))
			continue
		}
		info, err := os.Stat(real)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: file %q: %w", where, t.File, err))
			continue
		}
		if !info.Mode().IsRegular() {
			errs = append(errs, fmt.Errorf("%s: file %q is not a regular file", where, t.File))
		}
	}
	return errs
}
