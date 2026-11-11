// Package cli is the plain command-line front end: it reads the arguments,
// finds the project's manifest, wires the real parts together and prints.
//
// Main takes everything it touches (streams, environment, clock, runner,
// credentials) as arguments and returns an exit code instead of exiting, so
// the commands can be tested without starting a process.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/store/sqlite"
)

// Exit codes. They are what a push hook looks at.
const (
	ExitOK        = 0   // every test passed
	ExitFailed    = 1   // a test failed
	ExitError     = 2   // the run could not start, or the request was wrong
	ExitCancelled = 130 // stopped with Ctrl-C
)

// Deps is everything the commands use from the outside world.
type Deps struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Getwd returns the current folder. Nil means os.Getwd.
	Getwd func() (string, error)
	// Environ returns the program's own environment, as "NAME=value".
	Environ func() []string
	// Now returns the time. Nil means time.Now.
	Now func() time.Time
	// Location is the zone times are shown in. Nil means the local zone.
	Location *time.Location
	Runner   core.Runner
	Creds    core.Credentials
	// IsTerminal says whether standard input is a terminal. Only then does a
	// run of every test ask for confirmation.
	IsTerminal bool
}

type app struct {
	Deps
	loc *time.Location
}

// Main runs one command and returns its exit code.
func Main(ctx context.Context, args []string, d Deps) int {
	a := &app{Deps: d, loc: d.Location}
	if a.loc == nil {
		a.loc = time.Local
	}
	if a.Getwd == nil {
		a.Getwd = os.Getwd
	}

	manifestPath, args, err := globalFlags(args)
	if err != nil {
		return a.errorf("%v", err)
	}
	if len(args) == 0 {
		a.usage(a.Stderr)
		return ExitError
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		a.usage(a.Stdout)
		return ExitOK
	case "run":
		return a.cmdRun(ctx, manifestPath, rest)
	case "history":
		return a.cmdHistory(ctx, manifestPath, rest)
	case "tests":
		return a.cmdTests(manifestPath, rest)
	case "counts":
		return a.cmdCounts(ctx, manifestPath, rest)
	case "check":
		return a.cmdCheck(manifestPath, rest)
	}
	fmt.Fprintf(a.Stderr, "tester: unknown command %q\n\n", cmd)
	a.usage(a.Stderr)
	return ExitError
}

// globalFlags takes the flags that come before the command name.
func globalFlags(args []string) (manifest string, rest []string, err error) {
	for len(args) > 0 {
		switch arg := args[0]; {
		case arg == "--manifest" || arg == "-manifest":
			if len(args) < 2 {
				return "", nil, errors.New("--manifest needs a path")
			}
			manifest, args = args[1], args[2:]
		case strings.HasPrefix(arg, "--manifest="):
			manifest, args = strings.TrimPrefix(arg, "--manifest="), args[1:]
		default:
			return manifest, args, nil
		}
	}
	return manifest, args, nil
}

func (a *app) usage(w io.Writer) {
	fmt.Fprint(w, `Usage: tester [--manifest PATH] <command> [flags]

Commands:
  run      run tests and save the run
             --env NAME  --test ID (repeatable)  --feature NAME  --yes
  history  list runs, or show one run in full
             history [--env NAME] [--feature NAME] [--source local|remote] [--limit N] [--offset N]
             history <run number>
  tests    list the tests, or show one test's file
             tests [--feature NAME]
             tests show <id>
  counts   show how each test has done
             --env NAME  --feature NAME  --since DATE
  check    check the manifest, and that every test file exists
  help     show this text

The manifest, test-manifest.yaml, is looked for in the current folder and
the folders above it, unless --manifest names it.

Exit codes: 0 all passed, 1 a test failed, 2 could not run or wrong request,
130 cancelled.
`)
}

func (a *app) errorf(format string, args ...any) int {
	fmt.Fprintf(a.Stderr, "tester: "+format+"\n", args...)
	return ExitError
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("tester "+name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

// parseFlags parses a command's flags. stop is true when the command must end
// now, with the exit code: a bad flag, a request for help, or too many
// arguments.
func (a *app) parseFlags(fs *flag.FlagSet, args []string, maxPositional int) (code int, stop bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK, true
		}
		return ExitError, true
	}
	if fs.NArg() > maxPositional {
		fmt.Fprintf(a.Stderr, "%s: unexpected argument %q\n", fs.Name(), fs.Arg(maxPositional))
		return ExitError, true
	}
	return 0, false
}

// stringList is a flag that can be given several times.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// findManifest returns the manifest's path: the one given, or the nearest
// test-manifest.yaml at or above the current folder.
func (a *app) findManifest(manifestPath string) (string, error) {
	if manifestPath != "" {
		// A project folder is accepted as well as the manifest file itself, as
		// most tools accept a folder: the manifest is the file inside it.
		if info, err := os.Stat(manifestPath); err == nil && info.IsDir() {
			return filepath.Join(manifestPath, manifestfile.FileName), nil
		}
		return manifestPath, nil
	}
	wd, err := a.Getwd()
	if err != nil {
		return "", err
	}
	return manifestfile.Find(wd)
}

func (a *app) load(manifestPath string) (*manifestfile.Loaded, error) {
	path, err := a.findManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	return manifestfile.Load(path)
}

// openIfExists opens the history file if there is one. With none it returns
// nil and no error, so a command that only reads never creates the file.
func (a *app) openIfExists(l *manifestfile.Loaded) (*sqlite.Store, error) {
	if _, err := os.Stat(l.HistoryPath); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return sqlite.Open(l.HistoryPath)
}

// chooseEnvironment returns the environment to use: the one named, or the
// manifest's only one. With several and none named, it is an error, so nothing
// runs against the wrong place by accident.
func chooseEnvironment(m *core.Manifest, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	names := make([]string, 0, len(m.Environments))
	for n := range m.Environments {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 1 {
		return names[0], nil
	}
	return "", fmt.Errorf("--env is required: the manifest defines %s", strings.Join(names, ", "))
}
