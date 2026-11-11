package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

const projectsUsage = `Usage: tester projects list
       tester projects add [folder]
       tester projects remove <name or folder>
`

// cmdProjects manages the list of projects the program knows about.
func (a *app) cmdProjects(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, "tester: projects needs list, add or remove\n\n"+projectsUsage)
		return ExitError
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "--help":
		fmt.Fprint(a.Stdout, projectsUsage)
		return ExitOK
	case "list":
		return a.projectsList(rest)
	case "add":
		return a.projectsAdd(rest)
	case "remove":
		return a.projectsRemove(rest)
	}
	fmt.Fprintf(a.Stderr, "tester: unknown projects command %q\n\n%s", sub, projectsUsage)
	return ExitError
}

// projectList returns the service for the list file, or says why there is none.
func (a *app) projectList() (*projects.Service, error) {
	if a.ProjectsFile == "" {
		return nil, errors.New("the list of projects is not available: this user has no config folder")
	}
	return &projects.Service{File: a.ProjectsFile}, nil
}

func (a *app) projectsList(rest []string) int {
	if len(rest) > 0 {
		return a.errorf("projects list takes no arguments; %q is unexpected", rest[0])
	}
	svc, err := a.projectList()
	if err != nil {
		return a.errorf("%v", err)
	}
	entries, err := svc.List()
	if err != nil {
		return a.errorf("%v", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(a.Stdout, "No projects yet. A project is added when you use `tester run` or `tester history` in it, or with `tester projects add`.")
		return ExitOK
	}
	t := newTable(a.Stdout)
	fmt.Fprintln(t, "NAME\tFOLDER")
	for _, e := range entries {
		folder := e.Path
		if e.Missing {
			folder += "  (folder not found)"
		}
		fmt.Fprintf(t, "%s\t%s\n", dash(e.Name), folder)
	}
	t.Flush()
	return ExitOK
}

func (a *app) projectsAdd(rest []string) int {
	if len(rest) > 1 {
		return a.errorf("projects add takes one folder; %q is unexpected", rest[1])
	}
	dir := "."
	if len(rest) == 1 {
		dir = rest[0]
	}
	dir, err := a.inCurrentFolder(dir)
	if err != nil {
		return a.errorf("%v", err)
	}
	svc, err := a.projectList()
	if err != nil {
		return a.errorf("%v", err)
	}
	p, added, err := svc.Add(dir)
	if err != nil {
		return a.errorf("%v", err)
	}
	if added {
		fmt.Fprintf(a.Stdout, "Added %s (%s)\n", p.Name, p.Path)
	} else {
		fmt.Fprintf(a.Stdout, "Already in the list: %s (%s)\n", p.Name, p.Path)
	}
	return ExitOK
}

func (a *app) projectsRemove(rest []string) int {
	if len(rest) != 1 {
		return a.errorf("projects remove needs one name or folder")
	}
	key := rest[0]
	if looksLikeFolder(key) {
		abs, err := a.inCurrentFolder(key)
		if err != nil {
			return a.errorf("%v", err)
		}
		key = abs
	}
	svc, err := a.projectList()
	if err != nil {
		return a.errorf("%v", err)
	}
	p, err := svc.Remove(key)
	if err != nil {
		return a.errorf("%v", err)
	}
	fmt.Fprintf(a.Stdout, "Removed %s (%s)\n", p.Name, p.Path)
	return ExitOK
}

// inCurrentFolder makes a relative folder absolute from the folder the command
// runs in, which tests can set.
func (a *app) inCurrentFolder(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	wd, err := a.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, dir), nil
}

// looksLikeFolder tells a folder from a project name: a name has no path
// separator in it.
func looksLikeFolder(key string) bool {
	return key == "." || key == ".." || strings.ContainsRune(key, filepath.Separator)
}

// remember adds the project to the list of projects when it is used. The list
// is bookkeeping: if it cannot be updated, the command still does its work and
// a one-line warning goes to standard error.
func (a *app) remember(l *manifestfile.Loaded) {
	if a.ProjectsFile == "" {
		return
	}
	svc := &projects.Service{File: a.ProjectsFile}
	if _, err := svc.Remember(projects.Project{Name: l.Manifest.Project, Path: l.Root}); err != nil {
		fmt.Fprintf(a.Stderr, "tester: warning: the list of projects could not be updated: %v\n", err)
	}
}
