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
       tester projects add [--name nickname] [folder]
       tester projects rename <name or folder> <nickname>
       tester projects remove <name or folder>

A nickname is shown instead of the name in the project's manifest, and can be
used wherever a name is. rename with an empty nickname ("") clears it.
`

// cmdProjects manages the list of projects the program knows about.
func (a *app) cmdProjects(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, "tester: projects needs list, add, rename or remove\n\n"+projectsUsage)
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
	case "rename":
		return a.projectsRename(rest)
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
		name := dash(e.Display())
		if e.Nickname != "" && e.Name != "" {
			name += " (" + e.Name + ")" // the nickname first, the manifest's name after it
		}
		folder := e.Path
		if e.Missing {
			folder += "  (folder not found)"
		}
		fmt.Fprintf(t, "%s\t%s\n", name, folder)
	}
	t.Flush()
	return ExitOK
}

func (a *app) projectsAdd(args []string) int {
	fs := a.flags("projects add")
	name := fs.String("name", "", "a nickname for the project, shown instead of the name in its manifest")
	if code, stop := a.parseFlags(fs, args, 100); stop {
		return code
	}
	rest := fs.Args()
	if len(rest) > 1 { // flags may also follow the folder
		if code, stop := a.parseFlags(fs, rest[1:], 0); stop {
			return code
		}
		rest = rest[:1]
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

	var ask projects.Asker
	if *name == "" {
		ask = a.nicknameAsker(false)
	}
	p, added, err := svc.Add(dir, *name, ask)
	switch {
	case err != nil:
		return a.errorf("%v", err)
	case added:
		fmt.Fprintf(a.Stdout, "Added %s (%s)\n", p.Display(), p.Path)
	case *name != "":
		fmt.Fprintf(a.Stdout, "Already in the list; now named %s (%s)\n", p.Display(), p.Path)
	default:
		fmt.Fprintf(a.Stdout, "Already in the list: %s (%s)\n", p.Display(), p.Path)
	}
	return ExitOK
}

func (a *app) projectsRename(rest []string) int {
	if len(rest) != 2 {
		return a.errorf(`projects rename needs a name or folder and a new nickname ("" clears it)`)
	}
	key, err := a.projectKey(rest[0])
	if err != nil {
		return a.errorf("%v", err)
	}
	svc, err := a.projectList()
	if err != nil {
		return a.errorf("%v", err)
	}
	p, previous, err := svc.Rename(key, rest[1])
	if err != nil {
		return a.errorf("%v", err)
	}
	if p.Nickname == "" {
		fmt.Fprintf(a.Stdout, "Cleared the nickname of %s (%s)\n", p.Display(), p.Path)
	} else {
		fmt.Fprintf(a.Stdout, "Renamed %s to %s (%s)\n", previous, p.Display(), p.Path)
	}
	return ExitOK
}

func (a *app) projectsRemove(rest []string) int {
	if len(rest) != 1 {
		return a.errorf("projects remove needs one name or folder")
	}
	key, err := a.projectKey(rest[0])
	if err != nil {
		return a.errorf("%v", err)
	}
	svc, err := a.projectList()
	if err != nil {
		return a.errorf("%v", err)
	}
	p, err := svc.Remove(key)
	if err != nil {
		return a.errorf("%v", err)
	}
	fmt.Fprintf(a.Stdout, "Removed %s (%s)\n", p.Display(), p.Path)
	return ExitOK
}

// projectKey turns what the owner typed into what the list matches on: a folder
// (made absolute from the folder the command runs in) or a name as it is.
func (a *app) projectKey(key string) (string, error) {
	if looksLikeFolder(key) {
		return a.inCurrentFolder(key)
	}
	return key, nil
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

// nicknameAsker returns the question to ask when a project is newly added, or
// nil when nobody can be asked: only a person at a real terminal can answer
// (a push hook cannot), and skip is set when the owner said not to ask.
func (a *app) nicknameAsker(skip bool) projects.Asker {
	if skip || !a.IsTerminal {
		return nil
	}
	return func(name string) string {
		fmt.Fprintf(a.Stderr, "The project will be listed as %q. Add a nickname? (Enter to keep the name): ", name)
		line, ok := a.readLine()
		if !ok {
			return ""
		}
		nick, err := projects.CheckNickname(line)
		if err != nil {
			fmt.Fprintf(a.Stderr, "tester: %v; keeping the name %q\n", err, name)
			return ""
		}
		return nick
	}
}

// remember records that the project is being used. The list is bookkeeping: if
// it cannot be updated, the command still does its work and a one-line warning
// goes to standard error. A project new to the list asks for a nickname when a
// person at a terminal can answer, unless skipAsk is set.
func (a *app) remember(l *manifestfile.Loaded, skipAsk bool) {
	if a.ProjectsFile == "" {
		return
	}
	svc := &projects.Service{File: a.ProjectsFile}
	if _, _, err := svc.Use(projects.Project{Name: l.Manifest.Project, Path: l.Root}, a.nicknameAsker(skipAsk)); err != nil {
		fmt.Fprintf(a.Stderr, "tester: warning: the list of projects could not be updated: %v\n", err)
	}
}
