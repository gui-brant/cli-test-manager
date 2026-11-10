package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gui-brant/cli-test-manager/internal/manifest"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
)

// cmdCheck reports every problem with the manifest, and then every test file
// that is missing, so a project can prove its test list matches its files.
// It exits 0 when all is well, 1 when it found problems and 2 when it could
// not read the manifest at all. It never touches the history file.
func (a *app) cmdCheck(manifestPath string, args []string) int {
	fs := a.flags("check")
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}

	path, err := a.findManifest(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return a.errorf("%v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return a.errorf("%v", err)
	}
	m, err := manifest.Parse(bytes.NewReader(data))
	if err != nil {
		return a.errorf("%s: %v", abs, err)
	}

	if problems := manifest.Validate(m); len(problems) > 0 {
		fmt.Fprintln(a.Stdout, manifest.Report(problems))
		fmt.Fprintln(a.Stdout, "(test files are checked once the manifest is valid)")
		return ExitFailed
	}

	loaded := &manifestfile.Loaded{Manifest: m, Path: abs, Root: filepath.Dir(abs)}
	if problems := manifestfile.CheckFiles(loaded); len(problems) > 0 {
		fmt.Fprintf(a.Stdout, "Test file problems (%d):\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(a.Stdout, "  - %v\n", p)
		}
		return ExitFailed
	}

	fmt.Fprintf(a.Stdout, "Manifest OK: %s, every test file exists.\n", plural(len(m.Tests), "test", "tests"))
	return ExitOK
}
