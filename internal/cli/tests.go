package cli

import (
	"context"
	"fmt"

	"github.com/gui-brant/cli-test-manager/internal/query"
	"github.com/gui-brant/cli-test-manager/internal/view"
)

func (a *app) cmdTests(manifestPath string, args []string) int {
	if len(args) > 0 && args[0] == "show" {
		return a.showTest(manifestPath, args[1:])
	}

	fs := a.flags("tests")
	feature := fs.String("feature", "", "only the tests of this feature")
	if code, stop := a.parseFlags(fs, args, 0); stop {
		return code
	}
	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	rows, err := query.Tests(loaded.Manifest, *feature)
	if err != nil {
		return a.errorf("%v", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.Stdout, "No tests listed.")
		return ExitOK
	}

	t := newTable(a.Stdout)
	fmt.Fprintln(t, "#\tID\tNAME\tFEATURE\tRUNNER\tTAG\tFILE")
	for _, r := range rows {
		fmt.Fprintf(t, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Number, r.ID, r.Name, r.Feature, r.Runner, dash(r.Tag), r.File)
	}
	t.Flush()
	return ExitOK
}

func (a *app) showTest(manifestPath string, args []string) int {
	fs := a.flags("tests show")
	runNumber := fs.Int64("run", 0, "show the file as it was in this run (default: the latest run that played the test)")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester tests show [--run N] <id>

Shows a test's file as it was when a run played it, from the history. The
tester never shows the file on disk: that is a live file, and it may have
changed since. Without --run it is the latest run that played the test; a test
that no run has played has no snapshot yet, and it says so.

Flags:
`)
		fs.PrintDefaults()
	}
	if code, stop := a.parseFlags(fs, args, 100); stop {
		return code
	}
	rest := fs.Args()
	if len(rest) > 1 {
		// Flags may also follow the id: "tests show login-flow --run 7".
		if code, stop := a.parseFlags(fs, rest[1:], 0); stop {
			return code
		}
		rest = rest[:1]
	}
	if len(rest) != 1 {
		return a.errorf("tests show needs exactly one test id")
	}
	if *runNumber < 0 {
		return a.errorf("--run must be a run number, not %d", *runNumber)
	}

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	row, ok := query.FindTest(loaded.Manifest, rest[0])
	if !ok {
		return a.errorf("test %q is not in the manifest", rest[0])
	}
	store, err := a.openIfExists(loaded)
	if err != nil {
		return a.errorf("%v", err)
	}
	if store == nil {
		return a.errorf("test %q has not been run by the tester yet, so there is no snapshot of its file: run it once", row.ID)
	}
	defer store.Close()

	snap, err := (&query.Service{Store: store}).TestSnapshot(context.Background(), row.ID, *runNumber)
	if err != nil {
		return a.errorf("%v", err)
	}

	fmt.Fprintf(a.Stdout, "#%d  %s  %s\n", row.Number, row.ID, row.Name)
	fmt.Fprintf(a.Stdout, "feature: %s   runner: %s   tag: %s\n", row.Feature, row.Runner, dash(row.Tag))
	// The path is the one the run recorded, which may differ from the manifest's
	// today if the file was moved.
	fmt.Fprintf(a.Stdout, "file: %s\n%s\n\n", snap.File.Path, view.SnapshotHeader(snap, a.loc))
	if !snap.File.Kept() {
		fmt.Fprintln(a.Stdout, view.NotKeptText(snap.File))
		return ExitOK
	}
	fmt.Fprint(a.Stdout, string(snap.File.Content))
	return ExitOK
}
