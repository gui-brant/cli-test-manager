// Command tester runs a project's tests and keeps their history.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/cli"
	"github.com/gui-brant/cli-test-manager/internal/creds/env"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
	"github.com/gui-brant/cli-test-manager/internal/runner"
	"github.com/gui-brant/cli-test-manager/internal/tui"
)

func main() {
	// Ctrl-C or a terminate signal cancels the run: the command being run is
	// stopped, and the run is discarded.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// The list of projects lives in the user's config folder. A user with none
	// simply has no list: nothing is added to it and `projects` says so.
	projectsFile, _ := projects.DefaultFile()

	code := cli.Main(ctx, os.Args[1:], cli.Deps{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Getwd:      os.Getwd,
		Environ:    os.Environ,
		Now:        time.Now,
		Location:   time.Local,
		Runner:     runner.New(),
		Creds:      env.New(),
		IsTerminal: isTerminal(os.Stdin),

		ProjectsFile: projectsFile,
		// The menu is the only place the screen framework is used.
		Menu: func(ctx context.Context, in io.Reader, out io.Writer) error {
			return tui.Run(ctx, in, out, tui.Deps{
				Projects: &projects.Service{File: projectsFile},
				Load:     manifestfile.Load,
				Loc:      time.Local,
			})
		},
	})

	stop()
	os.Exit(code)
}

// isTerminal reports whether f is a terminal, that is, whether a person can
// answer a prompt. A push hook's standard input is a pipe, and a script or a
// cron job often gets /dev/null; neither is a person. /dev/null is a character
// device just as a terminal is, so it is ruled out by name.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, null) {
		return false
	}
	return true
}
