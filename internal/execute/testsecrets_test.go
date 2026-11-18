package execute

import (
	"strings"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// withTestSecret gives the test "note-create" its own secret NOTE_KEY, and
// puts a value for NOTE_KEY in the tester's own environment, as a person's
// shell would hold it.
func withTestSecret(f *fixture) {
	f.manifest.Tests[2].Secrets = []string{"NOTE_KEY"}
	f.svc.Environ = func() []string { return append(parentEnv(), "NOTE_KEY=from-the-shell") }
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func hasName(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

func TestATestGetsOnlyItsOwnSecrets(t *testing.T) {
	f := newFixture(t)
	withTestSecret(f)
	f.svc.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc", "NOTE_KEY": "note-key-9876"}
	f.mustRun("deployed", Selection{TestIDs: []string{"login-flow", "note-create"}})

	if len(f.runner.calls) != 2 {
		t.Fatalf("%d commands ran, want 2", len(f.runner.calls))
	}
	login, note := f.runner.calls[0], f.runner.calls[1]
	if hasName(login.Env, "NOTE_KEY") {
		t.Errorf("login-flow got NOTE_KEY, a secret of another test: %v", login.Env)
	}
	if !hasEnv(note.Env, "NOTE_KEY=note-key-9876") {
		t.Errorf("note-create did not get its own secret: %v", note.Env)
	}
	if !hasEnv(note.Env, "ACCOUNT_PASSWORD=pw-12345-abc") {
		t.Errorf("note-create lost the environment's secret: %v", note.Env)
	}
}

func TestAnotherTestNeverInheritsASecretFromTheShell(t *testing.T) {
	// NOTE_KEY is in the shell and declared by a test; a run that does not
	// select that test must not pass it on to the others.
	f := newFixture(t)
	withTestSecret(f)
	f.mustRun("local", Selection{TestIDs: []string{"users-table"}})
	if hasName(f.runner.calls[0].Env, "NOTE_KEY") {
		t.Errorf("a test got a secret declared by another test: %v", f.runner.calls[0].Env)
	}
}

func TestAMissingTestSecretRefusesOnlyRunsThatSelectThatTest(t *testing.T) {
	f := newFixture(t)
	withTestSecret(f) // NOTE_KEY is in the shell, but the credentials source does not have it

	// A run of other tests is not affected.
	rep := f.mustRun("deployed", Selection{TestIDs: []string{"login-flow"}})
	if !rep.Saved || len(rep.Refused) != 0 || len(f.runner.calls) != 1 {
		t.Fatalf("a run that does not select note-create: saved %v, refused %v, commands %d", rep.Saved, rep.Refused, len(f.runner.calls))
	}

	// A run that selects the test is refused, and nothing starts.
	f.runner.calls = nil
	rep, err := f.run("deployed", Selection{TestIDs: []string{"login-flow", "note-create"}})
	if err != nil {
		t.Fatalf("a refusal is a saved run, not an error: %v", err)
	}
	if len(rep.Refused) != 1 || !rep.Refused[0].Secret || !strings.Contains(rep.Refused[0].Why, "NOTE_KEY") {
		t.Fatalf("Refused = %+v; want one refusal naming NOTE_KEY", rep.Refused)
	}
	if len(f.runner.calls) != 0 {
		t.Errorf("%d commands ran in a refused run", len(f.runner.calls))
	}
	if rep.Run.State != core.RunRefused || !rep.Saved {
		t.Errorf("state %q, saved %v; want a saved refused run", rep.Run.State, rep.Saved)
	}
	if !strings.Contains(rep.Run.Diagnostic, "secrets of note-create") {
		t.Errorf("the diagnostic does not say whose secret is missing: %q", rep.Run.Diagnostic)
	}
}

func TestATestSecretIsScrubbedFromEverythingKept(t *testing.T) {
	const key = "note-key-9876"
	f := newFixture(t)
	withTestSecret(f)
	f.svc.Creds = fakeCreds{"ACCOUNT_PASSWORD": "pw-12345-abc", "NOTE_KEY": key}
	f.runner.reply = func(_ int, inv core.Invocation) (core.Output, error) {
		return core.Output{Stdout: []byte("log: " + key), Stderr: []byte("fatal: " + key), ExitCode: 2}, nil
	}
	rep := f.mustRun("deployed", Selection{TestIDs: []string{"note-create"}})
	stored, err := f.store.GetRun(bg, rep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	text := stored.Diagnostic
	for _, r := range stored.Results {
		text += "\n" + r.Name + "\n" + r.Message
	}
	if strings.Contains(text, key) {
		t.Errorf("the saved run contains a test's secret:\n%s", text)
	}
}
