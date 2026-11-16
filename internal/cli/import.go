package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/importer"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

func (a *app) cmdImport(ctx context.Context, manifestPath string, args []string) int {
	fs := a.flags("import")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: tester import [flags] <file | ->

Records a result file that came from outside: the output of a tool the tester
never starts, such as a load or network tool. The file is read as JUnit XML or
TAP and saved as one run, shown in "tester history" as import. "-" reads the
file from standard input. Put the flags before the file name.

  --env NAME      the environment the results belong to. It must allow import
                  (actions: [import] in the manifest). May be left out when
                  exactly one environment allows it.
  --test ID       give every result in the file to this one test. Without it,
                  each result goes to the test its suite or class name points
                  at: a test's id, or its file path (as for pulled results).
                  Results that match no test are kept, without a test, and
                  counted.
  --format F      junit or tap. Without it the file's extension decides
                  (.xml is junit, .tap is tap).
  --commit SHA    the commit the results are about. Nothing is guessed from the
                  current checkout, because the file may be about another one.
  --tool N=V      a tool and its version, for example --tool k6=1.4.0. May be
                  repeated. Nothing is guessed.
  --when TIME     when the results happened, as 2026-11-14T09:30:00Z. Without it
                  the file's modified time is used, or the moment of import for
                  standard input.

Secrets the environment declares are scrubbed from the file when they are set in
your environment; one that is not set does not stop the import. Importing the
same file into the same environment again stores nothing and says which run
holds it.

Exit codes: 0 recorded or already recorded (whatever the results were), 2 could
not import, 130 cancelled. Importing records results; it does not judge them.
`)
	}
	envFlag := fs.String("env", "", "the environment the results belong to")
	testID := fs.String("test", "", "give every result in the file to this test")
	format := fs.String("format", "", "junit or tap (default: from the file's extension)")
	commit := fs.String("commit", "", "the commit the results are about")
	var tools stringList
	fs.Var(&tools, "tool", "a tool and its version, name=version; repeatable")
	whenFlag := fs.String("when", "", "when the results happened, RFC 3339")
	if code, stop := a.parseFlags(fs, args, 1); stop {
		return code
	}
	if fs.NArg() == 0 {
		return a.errorf("name the file to import, or - for standard input")
	}
	file := fs.Arg(0)

	loaded, err := a.load(manifestPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	m := loaded.Manifest
	envName, err := chooseImportEnvironment(m, *envFlag)
	if err != nil {
		return a.errorf("%v", err)
	}

	fmtName, err := importFormat(*format, file)
	if err != nil {
		return a.errorf("%v", err)
	}
	toolList, err := parseTools(tools)
	if err != nil {
		return a.errorf("%v", err)
	}
	when, err := parseWhen(*whenFlag)
	if err != nil {
		return a.errorf("%v", err)
	}

	data, modified, err := a.readImport(file)
	if err != nil {
		return a.errorf("%v", err)
	}
	if when.IsZero() {
		when = modified // zero for standard input: the service uses the moment of import
	}

	svc := &importer.Service{Creds: a.Creds, Now: a.Now}
	// Everything that can be checked without a history file is checked first, so
	// a request that cannot be imported does not create one.
	prepared, err := svc.Prepare(ctx, importer.Request{
		Manifest: m, Environment: envName, Data: data, Format: fmtName,
		TestID: *testID, Commit: *commit, Tools: toolList, When: when,
	})
	if err != nil {
		return a.errorf("%v", err)
	}

	store, err := sqlite.Open(loaded.HistoryPath)
	if err != nil {
		return a.errorf("%v", err)
	}
	defer store.Close()
	svc.Store = store

	rep, err := svc.Record(ctx, prepared)
	switch {
	case err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		fmt.Fprintln(a.Stderr, "tester: cancelled; nothing was stored")
		return ExitCancelled
	case err != nil:
		return a.errorf("%v", err)
	case rep.Already:
		fmt.Fprintf(a.Stdout, "Already imported as run %d; nothing was stored.\n", rep.RunID)
		return ExitOK
	}

	tests, failed := rep.Run.Counts()
	line := fmt.Sprintf("Imported as run %d: %s, %d failed", rep.RunID, plural(tests, "test", "tests"), failed)
	if n := len(rep.Run.Results); n != tests {
		line += fmt.Sprintf(" (%s)", plural(n, "check", "checks"))
	}
	if rep.Unmatched > 0 {
		line += fmt.Sprintf(" (%d matched no test)", rep.Unmatched)
	}
	fmt.Fprintln(a.Stdout, line)
	fmt.Fprintf(a.Stdout, "Details: tester history %d\n", rep.RunID)
	return ExitOK
}

// chooseImportEnvironment returns the environment to import into: the one
// named, or the only one that allows import.
func chooseImportEnvironment(m *core.Manifest, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	switch envs := importer.ImportEnvironments(m); len(envs) {
	case 0:
		return "", errors.New("no environment of this project allows import; add `actions: [import]` to one in the manifest")
	case 1:
		return envs[0], nil
	default:
		return "", fmt.Errorf("name the environment with --env (those that allow import: %s)", strings.Join(envs, ", "))
	}
}

// importFormat is the format named, or the one the file's extension says.
func importFormat(named, file string) (core.ResultFormat, error) {
	if named != "" {
		switch f := core.ResultFormat(named); f {
		case core.FormatJUnit, core.FormatTAP:
			return f, nil
		}
		return "", fmt.Errorf("--format must be %q or %q, not %q", core.FormatJUnit, core.FormatTAP, named)
	}
	if f, ok := importer.FormatFor(file); ok {
		return f, nil
	}
	return "", fmt.Errorf("cannot tell the format of %q from its name; say --format junit or --format tap", file)
}

// parseTools reads each name=version flag.
func parseTools(flags []string) ([]core.Tool, error) {
	var out []core.Tool
	for _, f := range flags {
		name, version, ok := strings.Cut(f, "=")
		name, version = strings.TrimSpace(name), strings.TrimSpace(version)
		if !ok || name == "" || version == "" {
			return nil, fmt.Errorf("--tool %q must be name=version, for example --tool k6=1.4.0", f)
		}
		out = append(out, core.Tool{Name: name, Version: version})
	}
	return out, nil
}

// parseWhen reads the --when flag. Empty means not given.
func parseWhen(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--when must look like 2026-11-14T09:30:00Z, not %q", s)
	}
	return t, nil
}

// readImport reads the file to import, or standard input for "-", and says when
// the file was last modified (zero for standard input).
func (a *app) readImport(file string) (data []byte, modified time.Time, err error) {
	if file == "-" {
		if a.Stdin == nil {
			return nil, time.Time{}, errors.New("there is no standard input to read")
		}
		// One byte over the limit, so the service can say the file is too large.
		data, err = io.ReadAll(io.LimitReader(a.Stdin, importer.MaxFileBytes+1))
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("reading standard input: %w", err)
		}
		return data, time.Time{}, nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cannot read %s: %v", file, errors.Unwrap(err))
	}
	if info.IsDir() {
		return nil, time.Time{}, fmt.Errorf("%s is a folder, not a result file", file)
	}
	if info.Size() > importer.MaxFileBytes {
		return nil, time.Time{}, fmt.Errorf("%s is larger than %d MB", file, importer.MaxFileBytes>>20)
	}
	data, err = os.ReadFile(file)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cannot read %s: %v", file, errors.Unwrap(err))
	}
	return data, info.ModTime(), nil
}
