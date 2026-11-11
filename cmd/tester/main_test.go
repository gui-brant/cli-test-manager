//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A script, cron job or push hook gives the program /dev/null or a pipe as its
// input. Neither is a person who can answer a prompt, and /dev/null is a
// character device just like a terminal, so it must be told apart by name.
func TestOnlyATerminalCountsAsATerminal(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Error("/dev/null was taken for a terminal: a hook would stop to ask a question nobody can answer")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) {
		t.Error("a pipe was taken for a terminal")
	}

	file, err := os.Create(filepath.Join(t.TempDir(), "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if isTerminal(file) {
		t.Error("a regular file was taken for a terminal")
	}

	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if isTerminal(closed) {
		t.Error("a closed file was taken for a terminal")
	}
}

// This test builds the real program and runs it as a user would, with the real
// runner, the real history file and a real shell command as the test.
func TestTheRealProgram(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tester")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	project := t.TempDir()
	manifest := `
manifest_version: 1
project: smoke
history: .tester/history.sqlite
features: [shell]
environments:
  local: {}
runners:
  sh:
    run: ["sh", "-c", "echo 1..1; echo 'ok 1 - from the shell'", "{files}"]
    format: tap
    output: stdout
    environments: [local]
    timeout: 1m
tests:
  - { id: hello, name: Hello, feature: shell, runner: sh, file: hello.sh }
`
	for name, content := range map[string]string{"test-manifest.yaml": manifest, "hello.sh": "echo hi\n"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The program keeps a list of projects in the user's config folder. Point it
	// at a temporary one so the test never writes to the real one.
	config := t.TempDir()

	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+config)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("running %v: %v", args, err)
		}
		return string(out), code
	}

	if out, code := run("help"); code != 0 || !strings.Contains(out, "Usage: tester") {
		t.Errorf("help: exit %d\n%s", code, out)
	}
	if _, code := run(); code != 2 {
		t.Errorf("no arguments: exit %d; want 2", code)
	}
	if out, code := run("check"); code != 0 || !strings.Contains(out, "Manifest OK") {
		t.Errorf("check: exit %d\n%s", code, out)
	}
	if out, code := run("run"); code != 0 || !strings.Contains(out, "Run 1: PASSED") {
		t.Errorf("run: exit %d\n%s", code, out)
	}
	if out, code := run("history"); code != 0 || !strings.Contains(out, "PASS") {
		t.Errorf("history: exit %d\n%s", code, out)
	}
	if out, code := run("counts"); code != 0 || !strings.Contains(out, "hello") {
		t.Errorf("counts: exit %d\n%s", code, out)
	}

	// `run` and `history` remembered the project, in the temporary config folder.
	if out, code := run("projects", "list"); code != 0 || !strings.Contains(out, "smoke") || !strings.Contains(out, project) {
		t.Errorf("projects list: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(config, "tester", "projects.yaml")); err != nil {
		t.Errorf("the list was not written where XDG_CONFIG_HOME says: %v", err)
	}
	if out, code := run("projects", "remove", "smoke"); code != 0 || !strings.Contains(out, "Removed smoke") {
		t.Errorf("projects remove: exit %d\n%s", code, out)
	}
}
