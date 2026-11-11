package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeMenu records that it was started and returns what the test wants.
type fakeMenu struct {
	calls int
	in    io.Reader
	out   io.Writer
	err   error
}

func (f *fakeMenu) run(_ context.Context, in io.Reader, out io.Writer) error {
	f.calls++
	f.in, f.out = in, out
	return f.err
}

func menuProject(t *testing.T, f *fakeMenu) *project {
	t.Helper()
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.deps.IsTerminal = true
	p.deps.Menu = f.run
	return p
}

func TestMenuOpensOnATerminal(t *testing.T) {
	f := &fakeMenu{}
	p := menuProject(t, f)
	if code := p.do("menu"); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	if f.calls != 1 || f.in == nil || f.out == nil {
		t.Errorf("the menu was started %d times with input %v, output %v; want once with both", f.calls, f.in, f.out)
	}
}

// Entry 47: without a terminal the menu refuses, loudly, instead of hanging.
func TestMenuWithoutATerminalRefuses(t *testing.T) {
	f := &fakeMenu{}
	p := menuProject(t, f)
	p.deps.IsTerminal = false
	if code := p.do("menu"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "the menu needs a terminal", "not from a script or a hook")
	if f.calls != 0 {
		t.Error("the menu was started without a terminal")
	}
}

func TestMenuRefusesWhatItCannotDo(t *testing.T) {
	cases := map[string]func(p *project, f *fakeMenu) (args []string, want string){
		"an argument": func(p *project, f *fakeMenu) ([]string, string) {
			return []string{"menu", "extra"}, `menu takes no arguments; "extra" is unexpected`
		},
		"no list of projects": func(p *project, f *fakeMenu) ([]string, string) {
			p.deps.ProjectsFile = ""
			return []string{"menu"}, "the list of projects is not available"
		},
		"no menu in this build": func(p *project, f *fakeMenu) ([]string, string) {
			p.deps.Menu = nil
			return []string{"menu"}, "has no menu"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeMenu{}
			p := menuProject(t, f)
			args, want := setup(p, f)
			if code := p.do(args...); code != ExitError {
				t.Errorf("exit %d; want %d", code, ExitError)
			}
			contains(t, "stderr", p.stderr(), want)
			if f.calls != 0 {
				t.Error("the menu was started")
			}
		})
	}
}

func TestMenuTurnsItsEndingIntoAnExitCode(t *testing.T) {
	f := &fakeMenu{err: errors.New("the terminal went away")}
	p := menuProject(t, f)
	if code := p.do("menu"); code != ExitError {
		t.Errorf("a failing menu: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "the terminal went away")

	f = &fakeMenu{err: context.Canceled}
	p = menuProject(t, f)
	if code := p.do("menu"); code != ExitCancelled {
		t.Errorf("a cancelled menu: exit %d; want %d", code, ExitCancelled)
	}
}

func TestHelpListsTheMenu(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("help")
	contains(t, "help", p.stdout(), "menu open the interactive menu", "(needs a terminal)")
	if strings.Count(p.stdout(), "menu open") != 1 {
		t.Errorf("the menu is listed more than once:\n%s", p.stdout())
	}
}
