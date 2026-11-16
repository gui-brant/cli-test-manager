package tree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var bg = context.Background()

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

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo has tracked, untracked and ignored files, and a name with a space.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--quiet")
	write(t, root, ".gitignore", "node_modules/\n*.log\n.tester/\n")
	write(t, root, "supabase/migrations/001_a.sql", "create table a ();")
	write(t, root, "tests/my test.sh", "echo hi")
	git(t, root, "add", ".")
	git(t, root, "commit", "--quiet", "-m", "first")

	write(t, root, "supabase/migrations/002_untracked.sql", "create table b ();")
	write(t, root, "node_modules/pkg/index.js", "ignored")
	write(t, root, "debug.log", "ignored")
	write(t, root, ".tester/history.sqlite", "ignored")
	return root
}

func TestFilesAreTrackedPlusUntrackedButNotIgnored(t *testing.T) {
	root := repo(t)
	files, ok := New().Files(bg, root)
	if !ok {
		t.Fatal("git could not list the files")
	}
	sort.Strings(files)
	want := []string{".gitignore", "supabase/migrations/001_a.sql", "supabase/migrations/002_untracked.sql", "tests/my test.sh"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %q; want %q", files, want)
	}
}

// A project inside a larger repository lists only its own files, relative to
// itself.
func TestASubfolderListsItsOwnFilesRelativeToItself(t *testing.T) {
	root := repo(t)
	sub := filepath.Join(root, "supabase")
	files, ok := New().Files(bg, sub)
	if !ok {
		t.Fatal("git could not list the files")
	}
	sort.Strings(files)
	want := []string{"migrations/001_a.sql", "migrations/002_untracked.sql"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %q; want %q", files, want)
	}
}

func TestFilesAreNotKnownWhenGitCannotSay(t *testing.T) {
	root := repo(t)

	cancelled, cancel := context.WithCancel(bg)
	cancel()
	if _, ok := New().Files(cancelled, root); ok {
		t.Error("a cancelled context still listed files")
	}
	if files, ok := New().Files(bg, t.TempDir()); ok || files != nil {
		t.Errorf("a folder that is not a repository gave %v, %v; want not known", files, ok)
	}
	if _, ok := New().Files(bg, filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("a missing folder was listed")
	}
	t.Setenv("PATH", t.TempDir()) // git is not installed
	if _, ok := New().Files(bg, root); ok {
		t.Error("files were listed without git")
	}
}

func TestReadReturnsTheFilesContent(t *testing.T) {
	root := repo(t)
	got, err := New().Read(root, "tests/my test.sh")
	if err != nil || string(got) != "echo hi" {
		t.Errorf("Read = %q, %v", got, err)
	}
	// Binary content comes back as it is.
	write(t, root, "bin.dat", "a\x00b")
	if got, err := New().Read(root, "bin.dat"); err != nil || string(got) != "a\x00b" {
		t.Errorf("Read of a binary file = %q, %v", got, err)
	}
}

func TestReadRefusesAnythingThatLeavesTheProject(t *testing.T) {
	root := repo(t)
	outside := t.TempDir()
	write(t, outside, "secret.txt", "outside")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link-out.txt")); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	if err := os.Symlink(filepath.Join(root, "tests", "my test.sh"), filepath.Join(root, "link-in.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-out")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, rel, want string
	}{
		{"parent folder", "../x", "not a path inside the project"},
		{"hidden parent", "a/../../x", "not a path inside the project"},
		{"absolute", "/etc/passwd", "not a path inside the project"},
		{"not clean", "tests//my test.sh", "not a path inside the project"},
		{"empty", "", "not a path inside the project"},
		{"link to a file outside", "link-out.txt", "resolves to a place outside the project"},
		{"file through a folder link", "dir-out/secret.txt", "resolves to a place outside the project"},
		{"a folder", "tests", "not a regular file"},
		{"missing", "nope.sql", "no such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := New().Read(root, c.rel); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Read(%q) = %q, %v; want an error containing %q", c.rel, got, err, c.want)
			}
		})
	}
	// A link that stays inside the project is fine.
	if got, err := New().Read(root, "link-in.sh"); err != nil || string(got) != "echo hi" {
		t.Errorf("an inside link: %q, %v", got, err)
	}
}

func TestReadRefusesAFileTooLargeToHold(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.dat", strings.Repeat("x", 100))
	old := maxRead
	maxRead = 99
	t.Cleanup(func() { maxRead = old })
	if _, err := New().Read(root, "big.dat"); err == nil || !strings.Contains(err.Error(), "is larger than") {
		t.Errorf("a file over the limit: %v", err)
	}
	maxRead = 100
	if _, err := New().Read(root, "big.dat"); err != nil {
		t.Errorf("a file at the limit: %v", err)
	}
}

func TestReadOfAMissingProjectFolder(t *testing.T) {
	if _, err := New().Read(filepath.Join(t.TempDir(), "gone"), "a.sql"); err == nil || !strings.Contains(err.Error(), "project folder") {
		t.Errorf("a missing project folder: %v", err)
	}
}
