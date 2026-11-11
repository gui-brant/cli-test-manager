package cli

import (
	"fmt"

	"github.com/gui-brant/cli-test-manager/internal/query"
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
	if len(args) != 1 {
		return a.errorf("tests show needs exactly one test id")
	}
	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	row, ok := query.FindTest(loaded.Manifest, args[0])
	if !ok {
		return a.errorf("test %q is not in the manifest", args[0])
	}

	code, err := query.TestCode(loaded.Root, row)
	if err != nil {
		return a.errorf("cannot read the test file %s: %v", row.File, err)
	}

	fmt.Fprintf(a.Stdout, "#%d  %s  %s\n", row.Number, row.ID, row.Name)
	fmt.Fprintf(a.Stdout, "feature: %s   runner: %s   tag: %s\n", row.Feature, row.Runner, dash(row.Tag))
	fmt.Fprintf(a.Stdout, "file: %s\n\n", row.File)
	fmt.Fprint(a.Stdout, code.Text)
	if code.Omitted > 0 {
		fmt.Fprintf(a.Stdout, "\n[... %d more bytes not shown ...]\n", code.Omitted)
	}
	return ExitOK
}
