package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listFor gives a project its own project list file, in a folder that does not
// exist yet. Tests never use the real one.
func listFor(t *testing.T, p *project) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config", "tester", "projects.yaml")
	p.deps.ProjectsFile = path
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestProjectsAddListRemove(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)

	if code := p.do("projects", "list"); code != ExitOK {
		t.Fatalf("list: exit %d; %s", code, p.stderr())
	}
	contains(t, "empty list", p.stdout(), "No projects yet")

	if code := p.do("projects", "add", p.root); code != ExitOK {
		t.Fatalf("add: exit %d; %s", code, p.stderr())
	}
	contains(t, "add", p.stdout(), "Added example", p.root)
	p.do("projects", "add", p.root)
	contains(t, "second add", p.stdout(), "Already in the list: example")

	p.do("projects", "list")
	contains(t, "list", p.stdout(), "NAME", "FOLDER", "example", p.root)

	if code := p.do("projects", "remove", "example"); code != ExitOK {
		t.Fatalf("remove: exit %d; %s", code, p.stderr())
	}
	contains(t, "remove", p.stdout(), "Removed example", p.root)
	p.do("projects", "list")
	contains(t, "list after remove", p.stdout(), "No projects yet")
	if !exists(filepath.Join(p.root, "test-manifest.yaml")) {
		t.Error("removing a project touched the project's own files")
	}
}

func TestProjectsAddAndRemoveUseTheCurrentFolder(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)

	p.cwd = p.root
	if code := p.do("projects", "add"); code != ExitOK { // no folder given
		t.Fatalf("add with no folder: exit %d; %s", code, p.stderr())
	}
	contains(t, "add", p.stdout(), "Added example", p.root)

	p.cwd = filepath.Join(p.root, "tests", "sql") // a subfolder; ".." climbs out
	p.do("projects", "add", filepath.Join("..", ".."))
	contains(t, "add again", p.stdout(), "Already in the list: example")

	p.cwd = p.root
	if code := p.do("projects", "remove", "."); code != ExitOK {
		t.Fatalf("remove .: exit %d; %s", code, p.stderr())
	}
	contains(t, "remove .", p.stdout(), "Removed example")
}

func TestProjectsAddNeedsAManifest(t *testing.T) {
	p := newProject(t, twoEnvironments)
	list := listFor(t, p)

	if code := p.do("projects", "add", t.TempDir()); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "test-manifest.yaml")
	if exists(list) {
		t.Error("a failed add created the list file")
	}
}

func TestProjectsRemoveByASharedNameAsksForTheFolder(t *testing.T) {
	a := newProject(t, twoEnvironments)
	b := newProject(t, twoEnvironments) // another folder, the same project name
	list := listFor(t, a)
	b.deps.ProjectsFile = list
	a.do("projects", "add", a.root)
	b.do("projects", "add", b.root)

	if code := a.do("projects", "remove", "example"); code != ExitError {
		t.Errorf("remove of a shared name: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", a.stderr(), "give the folder", a.root, b.root)

	if code := a.do("projects", "remove", a.root); code != ExitOK {
		t.Fatalf("remove by folder: exit %d; %s", code, a.stderr())
	}
	a.do("projects", "list")
	contains(t, "list", a.stdout(), b.root)
	if strings.Contains(a.stdout(), a.root) {
		t.Errorf("the removed folder is still listed:\n%s", a.stdout())
	}
}

func TestProjectsRemoveOfSomethingNotListed(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", p.root)
	if code := p.do("projects", "remove", "nope"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), `no project in the list is named "nope"`)
}

func TestProjectsListMarksAMissingFolder(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", p.root)
	if err := os.RemoveAll(p.root); err != nil {
		t.Fatal(err)
	}
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "example", "(folder not found)")
}

func TestProjectsUsageErrors(t *testing.T) {
	cases := [][]string{
		{"projects"},
		{"projects", "nope"},
		{"projects", "list", "extra"},
		{"projects", "add", "a", "b"},
		{"projects", "remove"},
		{"projects", "remove", "a", "b"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			list := listFor(t, p)
			if code := p.do(args...); code != ExitError {
				t.Errorf("exit %d; want %d", code, ExitError)
			}
			if p.stderr() == "" {
				t.Error("no message on standard error")
			}
			if exists(list) {
				t.Error("a request that was refused created the list file")
			}
		})
	}
	p := newProject(t, twoEnvironments)
	p.do("projects", "help")
	contains(t, "projects help", p.stdout(), "tester projects list", "tester projects add [folder]", "tester projects remove <name or folder>")
}

func TestProjectsSaysWhenThereIsNoList(t *testing.T) {
	p := newProject(t, twoEnvironments) // ProjectsFile is empty
	if code := p.do("projects", "list"); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "not available")
}

// Entry 48: only run and history add a project; a request that cannot start adds
// nothing, and the read-only commands leave no trace.
func TestRunAndHistoryRememberTheProjectAndNothingElseDoes(t *testing.T) {
	p := newProject(t, twoEnvironments)
	list := listFor(t, p)

	p.deps.Creds = fakeCreds{} // so a run against "deployed" cannot start
	for _, args := range [][]string{
		{"check"}, {"tests"}, {"counts"},
		{"run"},                      // no --env: refused
		{"run", "--env", "nowhere"},  // refused
		{"run", "--env", "deployed"}, // refused: a secret is not set
	} {
		p.do(args...)
		if exists(list) {
			t.Fatalf("%v added the project", args)
		}
	}
	p.deps.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc"}

	if code := p.do("history"); code != ExitOK {
		t.Fatalf("history: exit %d; %s", code, p.stderr())
	}
	p.do("projects", "list")
	contains(t, "after history", p.stdout(), "example", p.root)

	// A run adds its project too.
	q := newProject(t, twoEnvironments)
	q.deps.ProjectsFile = list
	if code := q.do("run", "--env", "local", "--yes"); code != ExitOK {
		t.Fatalf("run: exit %d; %s %s", code, q.stdout(), q.stderr())
	}
	q.do("projects", "list")
	contains(t, "after run", q.stdout(), p.root, q.root)
}

// Entry 46: the list is bookkeeping. A list that cannot be written, or is not
// valid, never changes what the command does or its exit code; it only warns.
func TestAProjectListThatCannotBeUpdatedOnlyWarns(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(broken, []byte("projects: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, file := range map[string]string{
		"its folder is a file": filepath.Join(blocker, "projects.yaml"),
		"it is not valid":      broken,
	} {
		t.Run(name, func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			p.deps.ProjectsFile = file

			if code := p.do("history"); code != ExitOK {
				t.Errorf("history: exit %d; want %d", code, ExitOK)
			}
			contains(t, "history", p.stdout(), "No runs yet.")
			contains(t, "history stderr", p.stderr(), "warning: the list of projects could not be updated")

			if code := p.do("run", "--env", "local", "--yes"); code != ExitOK {
				t.Errorf("run: exit %d; want %d", code, ExitOK)
			}
			contains(t, "run", p.stdout(), "PASSED")
			contains(t, "run stderr", p.stderr(), "warning: the list of projects could not be updated")
		})
	}
	if got, _ := os.ReadFile(broken); string(got) != "projects: [unterminated\n" {
		t.Errorf("the invalid list was changed:\n%s", got)
	}
}

func TestWithNoListConfiguredNothingIsListedAndNothingIsSaid(t *testing.T) {
	p := newProject(t, twoEnvironments) // ProjectsFile is empty
	if code := p.do("history"); code != ExitOK {
		t.Errorf("exit %d; want %d", code, ExitOK)
	}
	if p.stderr() != "" {
		t.Errorf("stderr %q; want nothing", p.stderr())
	}
}

func TestHelpListsTheProjectsCommand(t *testing.T) {
	p := newProject(t, twoEnvironments)
	p.do("help")
	contains(t, "help", p.stdout(),
		"projects list, add or forget the projects the program knows about",
		"projects add [folder]", "projects remove <name or folder>",
		"added to the list of projects when you use run or history in it")
}
