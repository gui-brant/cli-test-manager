package cli

import (
	"context"
	"io"
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
		{"projects", "rename"},
		{"projects", "rename", "a"},
		{"projects", "rename", "a", "b", "c"},
		{"projects", "add", "--bogus"},
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
	contains(t, "projects help", p.stdout(), "tester projects list", "tester projects add [--name nickname] [folder]",
		"tester projects rename <name or folder> <nickname>", "tester projects remove <name or folder>", `rename with an empty nickname ("") clears it`)
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
		"projects list, add, rename or forget the projects the program knows about",
		"projects add [--name nickname] [folder]", "projects rename <name or folder> <nickname>", "projects remove <name or folder>",
		"added to the list of projects when you use run or history in it", "you are asked for an optional nickname")
}

// ---- nicknames (entry 50) ----

const nicknameQuestion = `The project will be listed as "example". Add a nickname?`

// atATerminal makes the project's input a person typing these lines.
func atATerminal(p *project, typed string) {
	p.deps.IsTerminal = true
	p.deps.Stdin = strings.NewReader(typed)
}

func TestANewProjectOffersANicknameAtATerminal(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	atATerminal(p, "birdwatching\n")

	if code := p.do("history"); code != ExitOK {
		t.Fatalf("history: exit %d; %s", code, p.stderr())
	}
	contains(t, "the question", p.stderr(), nicknameQuestion, "(Enter to keep the name)")
	contains(t, "history still prints", p.stdout(), "No runs yet.")

	p.do("projects", "list")
	contains(t, "list", p.stdout(), "birdwatching (example)", p.root)

	// Already listed: never asked again.
	atATerminal(p, "other\n")
	p.do("history")
	if strings.Contains(p.stderr(), "nickname") {
		t.Errorf("a listed project was asked again:\n%s", p.stderr())
	}
	p.do("projects", "list")
	contains(t, "list after", p.stdout(), "birdwatching (example)")
}

func TestPressingEnterOrNoInputKeepsTheManifestName(t *testing.T) {
	for name, typed := range map[string]string{"Enter": "\n", "end of input": "", "only spaces": "   \n"} {
		t.Run(name, func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			listFor(t, p)
			atATerminal(p, typed)
			if code := p.do("history"); code != ExitOK {
				t.Fatalf("exit %d; %s", code, p.stderr())
			}
			p.do("projects", "list")
			contains(t, "list", p.stdout(), "example", p.root)
			if strings.Contains(p.stdout(), "(example)") {
				t.Errorf("a nickname was set:\n%s", p.stdout())
			}
		})
	}
}

func TestAnUnusableNicknameIsReportedAndTheNameKept(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	atATerminal(p, "a/b\n")
	if code := p.do("history"); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	contains(t, "stderr", p.stderr(), "cannot be used", `keeping the name "example"`)
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "example")
	if strings.Contains(p.stdout(), "a/b") {
		t.Errorf("the unusable nickname was stored:\n%s", p.stdout())
	}
}

func TestNobodyIsAskedWhereNobodyCanAnswerOrWhenToldNotTo(t *testing.T) {
	// Not a terminal (a push hook): no question, the manifest's name is used.
	p := newProject(t, twoEnvironments)
	list := listFor(t, p)
	p.deps.Stdin = strings.NewReader("birdwatching\n")
	p.do("history")
	if strings.Contains(p.stderr(), "nickname") {
		t.Errorf("asked without a terminal:\n%s", p.stderr())
	}
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "example")
	if strings.Contains(p.stdout(), "birdwatching") {
		t.Errorf("input that nobody was asked for became a nickname:\n%s", p.stdout())
	}

	// A terminal, but --yes: no question.
	q := newProject(t, twoEnvironments)
	q.deps.ProjectsFile = list
	atATerminal(q, "birdwatching\n")
	if code := q.do("run", "--env", "local", "--yes"); code != ExitOK {
		t.Fatalf("run --yes: exit %d; %s", code, q.stderr())
	}
	if strings.Contains(q.stderr(), "nickname") {
		t.Errorf("run --yes asked:\n%s", q.stderr())
	}
}

// The nickname question and "Are you sure?" read from the same input, in that order.
func TestTheNicknameQuestionAndTheConfirmationShareTheInput(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	atATerminal(p, "bw\ny\n")

	if code := p.do("run", "--env", "local"); code != ExitOK {
		t.Fatalf("run: exit %d; %s %s", code, p.stdout(), p.stderr())
	}
	question := strings.Index(p.stderr(), "Add a nickname?")
	sure := strings.Index(p.stderr(), "[y/N]")
	if question < 0 || sure < 0 || question > sure {
		t.Errorf("want the nickname question, then the confirmation:\n%s", p.stderr())
	}
	contains(t, "run", p.stdout(), "PASSED")
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "bw (example)")
}

func TestProjectsAddWithANicknameAndWithoutAsking(t *testing.T) {
	for name, args := range map[string]func(root string) []string{
		"flag before the folder": func(r string) []string { return []string{"projects", "add", "--name", "bw", r} },
		"flag after the folder":  func(r string) []string { return []string{"projects", "add", r, "--name", "bw"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			listFor(t, p)
			atATerminal(p, "should-not-be-read\n")
			if code := p.do(args(p.root)...); code != ExitOK {
				t.Fatalf("exit %d; %s", code, p.stderr())
			}
			contains(t, "add", p.stdout(), "Added bw", p.root)
			if strings.Contains(p.stderr(), "nickname") {
				t.Errorf("asked although --name was given:\n%s", p.stderr())
			}
			p.do("projects", "list")
			contains(t, "list", p.stdout(), "bw (example)")

			// On a listed project, --name changes the nickname.
			if code := p.do("projects", "add", "--name", "birds", p.root); code != ExitOK {
				t.Fatalf("second add: exit %d", code)
			}
			contains(t, "second add", p.stdout(), "Already in the list; now named birds")
		})
	}
}

func TestProjectsAddAsksAtATerminalWhenNoNameIsGiven(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	atATerminal(p, "photos\n")
	if code := p.do("projects", "add", p.root); code != ExitOK {
		t.Fatalf("exit %d; %s", code, p.stderr())
	}
	contains(t, "question", p.stderr(), nicknameQuestion)
	contains(t, "add", p.stdout(), "Added photos")
}

func TestProjectsAddWithABadNicknameRefusesAndWritesNothing(t *testing.T) {
	p := newProject(t, twoEnvironments)
	list := listFor(t, p)
	if code := p.do("projects", "add", "--name", "a/b", p.root); code != ExitError {
		t.Errorf("exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), "cannot be used")
	if exists(list) {
		t.Error("a refused nickname created the list file")
	}
}

func TestProjectsRenameAtAnyTime(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", p.root) // no terminal: no question

	if code := p.do("projects", "rename", "example", "birdwatching"); code != ExitOK {
		t.Fatalf("rename: exit %d; %s", code, p.stderr())
	}
	contains(t, "rename", p.stdout(), "Renamed example to birdwatching", p.root)
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "birdwatching (example)")

	p.do("projects", "rename", "birdwatching", "birds") // found by its nickname
	contains(t, "rename again", p.stdout(), "Renamed birdwatching to birds")

	p.cwd = p.root
	if code := p.do("projects", "rename", ".", ""); code != ExitOK { // by folder; "" clears
		t.Fatalf("clear: exit %d; %s", code, p.stderr())
	}
	contains(t, "clear", p.stdout(), "Cleared the nickname of example")
	p.do("projects", "list")
	if strings.Contains(p.stdout(), "(example)") {
		t.Errorf("the nickname is still shown:\n%s", p.stdout())
	}

	if code := p.do("projects", "rename", "nope", "x"); code != ExitError {
		t.Errorf("rename of an unknown project: exit %d; want %d", code, ExitError)
	}
	contains(t, "stderr", p.stderr(), `no project in the list is named "nope"`)
	if code := p.do("projects", "rename", "example", "a/b"); code != ExitError {
		t.Errorf("rename to a bad nickname: exit %d; want %d", code, ExitError)
	}
}

func TestRemoveWorksByNicknameAndByManifestName(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", "--name", "bw", p.root)

	if code := p.do("projects", "remove", "bw"); code != ExitOK {
		t.Fatalf("remove by nickname: exit %d; %s", code, p.stderr())
	}
	contains(t, "remove", p.stdout(), "Removed bw")

	p.do("projects", "add", "--name", "bw", p.root)
	if code := p.do("projects", "remove", "example"); code != ExitOK { // the manifest's name still works
		t.Fatalf("remove by manifest name: exit %d; %s", code, p.stderr())
	}
}

func TestTheManifestNameRefreshesButNeverReplacesANickname(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", "--name", "bw", p.root)

	renamed := strings.Replace(twoEnvironments, "project: example", "project: example-renamed", 1)
	if err := os.WriteFile(filepath.Join(p.root, "test-manifest.yaml"), []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	p.do("history") // using the project refreshes its manifest name
	p.do("projects", "list")
	contains(t, "list", p.stdout(), "bw (example-renamed)")
}

// ---- Ctrl-C while being asked ----

// cancelsOnRead is an input that is interrupted by Ctrl-C the moment the program
// waits for a line: it cancels the program's context and then ends.
type cancelsOnRead struct{ cancel context.CancelFunc }

func (c cancelsOnRead) Read([]byte) (int, error) {
	c.cancel()
	return 0, io.EOF
}

// Ctrl-C at the nickname question stops the command: exit 130, and the project
// is not added (found by trying it on a real terminal).
func TestCtrlCAtTheNicknameQuestionCancelsAndAddsNothing(t *testing.T) {
	for _, args := range [][]string{
		{"history"},
		{"run", "--env", "local"},
		{"projects", "add"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := newProject(t, twoEnvironments)
			list := listFor(t, p)
			p.cwd = p.root
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p.deps.IsTerminal = true
			p.deps.Stdin = cancelsOnRead{cancel}

			if code := p.doCtx(ctx, args...); code != ExitCancelled {
				t.Errorf("exit %d; want %d (stderr: %s)", code, ExitCancelled, p.stderr())
			}
			contains(t, "stderr", p.stderr(), "Add a nickname?", "cancelled; nothing was changed")
			if exists(list) {
				t.Error("a cancelled question still added the project")
			}
			if len(p.runner.calls) != 0 || p.historyExists() {
				t.Error("a cancelled question still ran tests or created the history")
			}
		})
	}
}

// Ctrl-C at "Are you sure?" is a cancel (130), not a "no" (2), and nothing runs.
func TestCtrlCAtTheConfirmationCancels(t *testing.T) {
	p := newProject(t, twoEnvironments)
	listFor(t, p)
	p.do("projects", "add", p.root) // already listed: no nickname question

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.deps.IsTerminal = true
	p.deps.Stdin = cancelsOnRead{cancel}

	if code := p.doCtx(ctx, "run", "--env", "local"); code != ExitCancelled {
		t.Errorf("exit %d; want %d (stderr: %s)", code, ExitCancelled, p.stderr())
	}
	contains(t, "stderr", p.stderr(), "[y/N]", "cancelled; nothing was run")
	if strings.Contains(p.stderr(), "not confirmed") {
		t.Errorf("a Ctrl-C was reported as a 'no':\n%s", p.stderr())
	}
	if len(p.runner.calls) != 0 || p.historyExists() {
		t.Error("tests ran or the history was created")
	}
}
