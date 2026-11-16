package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var bg = context.Background()

// git runs a real git command in dir, with no user or system configuration, so
// the tests behave the same on every machine.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo makes a repository with one committed file.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	write(t, dir, "a.txt", "one\n")
	write(t, dir, ".gitignore", "ignored.log\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "--quiet", "-m", "first")
	return dir
}

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestACleanRepositoryHasACommitAndIsNotDirty(t *testing.T) {
	commit, dirty := New().State(bg, repo(t))
	if !sha.MatchString(commit) || dirty {
		t.Errorf("State = %q, %v; want a 40-character commit and not dirty", commit, dirty)
	}
}

func TestChangesMakeItDirty(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"an edited tracked file": func(t *testing.T, dir string) { write(t, dir, "a.txt", "two\n") },
		"an untracked file":      func(t *testing.T, dir string) { write(t, dir, "new.txt", "x") },
		"a staged file": func(t *testing.T, dir string) {
			write(t, dir, "b.txt", "x")
			git(t, dir, "add", "b.txt")
		},
		"a deleted tracked file": func(t *testing.T, dir string) { _ = os.Remove(filepath.Join(dir, "a.txt")) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir := repo(t)
			change(t, dir)
			commit, dirty := New().State(bg, dir)
			if !sha.MatchString(commit) || !dirty {
				t.Errorf("State = %q, %v; want the commit and dirty", commit, dirty)
			}
		})
	}
}

// The history file and similar are ignored; they must never make every run
// look dirty.
func TestAnIgnoredFileDoesNotMakeItDirty(t *testing.T) {
	dir := repo(t)
	write(t, dir, "ignored.log", "x")
	if _, dirty := New().State(bg, dir); dirty {
		t.Error("an ignored file made the tree dirty")
	}
}

func TestStateAfterACommitIsCleanAgainWithANewCommit(t *testing.T) {
	dir := repo(t)
	before, _ := New().State(bg, dir)
	write(t, dir, "a.txt", "two\n")
	git(t, dir, "commit", "--quiet", "-am", "second")
	after, dirty := New().State(bg, dir)
	if after == before || !sha.MatchString(after) || dirty {
		t.Errorf("before %q, after %q, dirty %v; want a new commit and clean", before, after, dirty)
	}
}

// A repository with no commits has no HEAD; its untracked files are still
// uncommitted changes.
func TestARepositoryWithNoCommits(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	if commit, dirty := New().State(bg, dir); commit != "" || dirty {
		t.Errorf("empty repository: State = %q, %v; want empty and clean", commit, dirty)
	}
	write(t, dir, "a.txt", "x")
	if commit, dirty := New().State(bg, dir); commit != "" || !dirty {
		t.Errorf("empty repository with a file: State = %q, %v; want no commit and dirty", commit, dirty)
	}
}

func TestASubfolderReportsItsRepository(t *testing.T) {
	dir := repo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if commit, _ := New().State(bg, sub); !sha.MatchString(commit) {
		t.Errorf("a subfolder gave %q; want the repository's commit", commit)
	}
}

// Everything git cannot answer is "not known", never an error.
func TestNotKnownCases(t *testing.T) {
	notARepo := t.TempDir()
	cases := map[string]func(t *testing.T) (context.Context, string){
		"not a repository": func(t *testing.T) (context.Context, string) { return bg, notARepo },
		"a missing folder": func(t *testing.T) (context.Context, string) { return bg, filepath.Join(notARepo, "nope") },
		"git not installed": func(t *testing.T) (context.Context, string) {
			dir := repo(t)
			t.Setenv("PATH", t.TempDir())
			return bg, dir
		},
		"a cancelled context": func(t *testing.T) (context.Context, string) {
			ctx, cancel := context.WithCancel(bg)
			cancel()
			return ctx, repo(t)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, dir := setup(t)
			if commit, dirty := New().State(ctx, dir); commit != "" || dirty {
				t.Errorf("State = %q, %v; want empty and false", commit, dirty)
			}
		})
	}
}

// Asking must not change the repository: a hook asks on every push.
func TestAskingDoesNotTouchTheIndex(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "two\n")
	index := filepath.Join(dir, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	New().State(bg, dir)
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("asking for the state rewrote the index")
	}
}
