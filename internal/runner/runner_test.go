//go:build unix

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

var bg = context.Background()

// The tests use the test program itself as the command to run. When it is
// started with RUNNER_HELPER set, TestHelperProcess behaves as the named
// mode instead of testing anything; otherwise it does nothing.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("RUNNER_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		fmt.Fprint(os.Stdout, "out text")
		fmt.Fprint(os.Stderr, "err text")
	case "exit":
		code, _ := strconv.Atoi(os.Getenv("RUNNER_CODE"))
		os.Exit(code)
	case "selfsignal":
		sig, _ := strconv.Atoi(os.Getenv("RUNNER_SIGNAL"))
		_ = syscall.Kill(os.Getpid(), syscall.Signal(sig))
		time.Sleep(10 * time.Second)
	case "env":
		env := os.Environ()
		sort.Strings(env)
		fmt.Print(strings.Join(env, "\n"))
	case "cwd":
		wd, _ := os.Getwd()
		fmt.Print(wd)
	case "stdin":
		n, _ := io.Copy(io.Discard, os.Stdin)
		fmt.Print(n)
	case "sleep":
		fmt.Print("started")
		time.Sleep(30 * time.Second)
	case "spawn":
		pidfile := os.Getenv("RUNNER_PIDFILE")
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = []string{"RUNNER_HELPER=sleeppid", raceEnv, "RUNNER_PIDFILE=" + pidfile}
		_ = child.Start()
		for i := 0; i < 200; i++ { // wait until the child has written its pid
			if _, err := os.Stat(pidfile); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		fmt.Print("started")
		time.Sleep(30 * time.Second)
	case "sleeppid":
		_ = os.WriteFile(os.Getenv("RUNNER_PIDFILE"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(30 * time.Second)
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
		fmt.Print("ready")
		time.Sleep(30 * time.Second)
	case "cleanup":
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		fmt.Print("ready")
		<-c
		fmt.Print("cleanup")
	case "flood":
		fmt.Print("FIRST LINE\n")
		line := strings.Repeat("x", 1023) + "\n"
		for i := 0; i < 40*1024; i++ { // 40 MiB
			fmt.Print(line)
		}
		fmt.Print("THE END\n")
	case "report":
		writeReport("REPORT-CONTENT")
	case "bigreport":
		writeReport(strings.Repeat("r", 5000))
	case "noreport":
	case "lingering":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = []string{"RUNNER_HELPER=holdopen", raceEnv}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr // keeps the output open
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		_ = child.Start()
		fmt.Print("parent done")
	case "holdopen":
		time.Sleep(5 * time.Second)
	case "printenv":
		fmt.Print(os.Getenv("RUNNER_VALUE"))
	}
	os.Exit(0)
}

func writeReport(content string) {
	for _, a := range os.Args {
		if path, ok := strings.CutPrefix(a, "--report="); ok {
			_ = os.WriteFile(path, []byte(content), 0o600)
		}
	}
}

// raceEnv switches off the race detector's one-second pause at exit. Programs
// built with -race wait a full second before exiting, which would make every
// helper process slow and push exits past the grace period in the stop tests.
// It has no effect on programs built without -race.
const raceEnv = "GORACE=atexit_sleep_ms=0"

func helper(mode string, env ...string) core.Invocation {
	return core.Invocation{
		// The "--" ends the test program's own flags, so extra arguments added
		// after it (such as --report=...) reach the helper instead of being
		// rejected as unknown flags.
		Args: []string{os.Args[0], "-test.run=^TestHelperProcess$", "--"},
		Env:  append([]string{"RUNNER_HELPER=" + mode, raceEnv}, env...),
	}
}

// newRunner is a runner with short waits, so the tests stay quick.
func newRunner() *Runner {
	return &Runner{Head: DefaultHeadBytes, Tail: DefaultTailBytes, Grace: 300 * time.Millisecond, WaitDelay: 300 * time.Millisecond}
}

func TestOutputAndExitCodes(t *testing.T) {
	r := newRunner()
	out, err := r.Run(bg, helper("echo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Stdout) != "out text" || string(out.Stderr) != "err text" {
		t.Errorf("stdout %q, stderr %q; want them captured separately", out.Stdout, out.Stderr)
	}
	if out.ExitCode != 0 || out.TimedOut || out.Truncated {
		t.Errorf("exit %d, timed out %v, truncated %v; want a clean exit", out.ExitCode, out.TimedOut, out.Truncated)
	}

	for _, code := range []int{1, 3, 42} {
		out, err := r.Run(bg, helper("exit", "RUNNER_CODE="+strconv.Itoa(code)))
		if err != nil {
			t.Fatalf("exit %d: a command that ran and failed must not be an error: %v", code, err)
		}
		if out.ExitCode != code {
			t.Errorf("exit code = %d; want %d", out.ExitCode, code)
		}
	}
}

func TestKilledBySignalHasTheShellConvention(t *testing.T) {
	for sig, want := range map[syscall.Signal]int{syscall.SIGTERM: 143, syscall.SIGKILL: 137} {
		out, err := newRunner().Run(bg, helper("selfsignal", "RUNNER_SIGNAL="+strconv.Itoa(int(sig))))
		if err != nil {
			t.Fatal(err)
		}
		if out.ExitCode != want {
			t.Errorf("signal %d gave exit code %d; want %d", sig, out.ExitCode, want)
		}
	}
}

func TestCouldNotStart(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missingDir := helper("echo")
	missingDir.Dir = "/no/such/folder"

	cases := map[string]core.Invocation{
		"no arguments":       {},
		"an empty program":   {Args: []string{""}},
		"a missing path":     {Args: []string{"/no/such/program"}},
		"not on the path":    {Args: []string{"no-such-program-anywhere"}},
		"not executable":     {Args: []string{notExecutable}},
		"a missing work dir": missingDir,
	}
	for name, inv := range cases {
		out, err := newRunner().Run(bg, inv)
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if out.Stdout != nil || out.ExitCode != 0 {
			t.Errorf("%s: output %+v; want an empty output", name, out)
		}
	}
}

func TestTheEnvironmentIsExactlyWhatWasGiven(t *testing.T) {
	t.Setenv("TESTER_SECRET_SHOULD_NOT_LEAK", "leaked")

	inv := helper("env", "A=1")
	given := append([]string(nil), inv.Env...)
	sort.Strings(given)
	out, err := newRunner().Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out.Stdout), strings.Join(given, "\n"); got != want {
		t.Errorf("the command saw %q; want exactly %q (the tester's own variables must not be inherited)", got, want)
	}

	// No environment at all means an empty one, not an inherited one.
	out, err = newRunner().Run(bg, core.Invocation{Args: []string{"env"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stdout) != 0 {
		t.Errorf("a command given no environment saw %q; want nothing", out.Stdout)
	}
}

func TestWorkingDirectoryAndStandardInput(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	inv := helper("cwd")
	inv.Dir = dir
	out, err := newRunner().Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(string(out.Stdout))
	if got != dir {
		t.Errorf("the command ran in %q; want %q", got, dir)
	}

	// Standard input is empty, so a command that reads it to the end finishes.
	inv = helper("stdin")
	inv.Timeout = 10 * time.Second
	out, err = newRunner().Run(bg, inv)
	if err != nil || out.TimedOut || string(out.Stdout) != "0" {
		t.Errorf("reading stdin: %q, timed out %v, %v; want 0 bytes and no hang", out.Stdout, out.TimedOut, err)
	}
}

func TestTimeoutStopsTheCommand(t *testing.T) {
	inv := helper("sleep")
	inv.Timeout = 2 * time.Second
	start := time.Now()
	out, err := newRunner().Run(bg, inv)
	if err != nil {
		t.Fatalf("a timeout is reported in the output, not as an error: %v", err)
	}
	if !out.TimedOut {
		t.Error("TimedOut is false")
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("took %v for a 2s timeout", elapsed)
	}
	if string(out.Stdout) != "started" {
		t.Errorf("output before the stop was lost: %q", out.Stdout)
	}
	if out.ExitCode == 0 {
		t.Error("a command stopped by a timeout must not have exit code 0")
	}
}

// processGone says whether a process no longer exists. A process that has
// died but is not yet collected by its parent (a zombie) counts as gone.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := strings.LastIndex(string(stat), ") "); i >= 0 && i+2 < len(stat) {
			return stat[i+2] == 'Z'
		}
	}
	return false
}

func TestTimeoutStopsTheWholeGroup(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	inv := helper("spawn", "RUNNER_PIDFILE="+pidfile)
	inv.Timeout = 4 * time.Second
	out, err := newRunner().Run(bg, inv)
	if err != nil || !out.TimedOut {
		t.Fatalf("Run = %+v, %v; want a timeout", out, err)
	}

	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("the command never started its child: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(3 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL) // do not leave it behind
			t.Fatalf("the child process %d outlived its parent's timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestThePoliteStopComesFirst(t *testing.T) {
	inv := helper("cleanup")
	inv.Timeout = 3 * time.Second
	out, err := newRunner().Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out.Stdout), "ready") {
		t.Fatalf("the command was not up in time: %q", out.Stdout)
	}
	if !strings.HasSuffix(string(out.Stdout), "cleanup") || out.ExitCode != 0 || !out.TimedOut {
		t.Errorf("output %q, exit %d, timed out %v; want it to have cleaned up when asked to terminate", out.Stdout, out.ExitCode, out.TimedOut)
	}
}

func TestAStubbornCommandIsKilledAfterTheGracePeriod(t *testing.T) {
	r := newRunner()
	inv := helper("ignoreterm")
	inv.Timeout = 3 * time.Second
	start := time.Now()
	out, err := r.Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Stdout) != "ready" {
		t.Fatalf("the command was not up in time: %q", out.Stdout)
	}
	if !out.TimedOut || out.ExitCode != 137 {
		t.Errorf("timed out %v, exit %d; want a timeout and 137 (killed)", out.TimedOut, out.ExitCode)
	}
	if elapsed := time.Since(start); elapsed < inv.Timeout+r.Grace || elapsed > inv.Timeout+r.Grace+4*time.Second {
		t.Errorf("took %v; want about the timeout plus the grace period", elapsed)
	}
}

func TestOutputIsCapped(t *testing.T) {
	r := &Runner{Head: 1 << 10, Tail: 1 << 10, Grace: 300 * time.Millisecond, WaitDelay: 300 * time.Millisecond}
	inv := helper("flood")
	inv.Timeout = 60 * time.Second
	out, err := r.Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 0 || out.TimedOut {
		t.Fatalf("exit %d, timed out %v; a flooding command must still finish normally", out.ExitCode, out.TimedOut)
	}
	if !out.Truncated {
		t.Error("Truncated is false after 40 MiB of output")
	}
	s := string(out.Stdout)
	if len(s) > 4<<10 {
		t.Errorf("kept %d bytes; want about 2 KiB plus a marker", len(s))
	}
	if !strings.HasPrefix(s, "FIRST LINE") || !strings.HasSuffix(s, "THE END\n") || !strings.Contains(s, "bytes omitted") {
		t.Errorf("start, end or marker missing; got %.40q ... %.40q", s, s[max(0, len(s)-40):])
	}

	small, err := newRunner().Run(bg, helper("echo"))
	if err != nil || small.Truncated {
		t.Errorf("small output: truncated %v, %v", small.Truncated, err)
	}
}

func TestTheResultFile(t *testing.T) {
	leftovers := func() int {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "tester-report-*"))
		return len(m)
	}
	before := leftovers()

	inv := helper("report")
	inv.Args = append(inv.Args, "--report={report}") // the placeholder inside a longer argument
	inv.WantsReport = true
	out, err := newRunner().Run(bg, inv)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Report) != "REPORT-CONTENT" {
		t.Errorf("report = %q; want the file's content", out.Report)
	}
	if got := leftovers(); got != before {
		t.Errorf("%d temporary result folders left behind", got-before)
	}

	// A command that writes no file gives no report, and no error.
	inv = helper("noreport")
	inv.WantsReport = true
	out, err = newRunner().Run(bg, inv)
	if err != nil || out.Report != nil {
		t.Errorf("no file written: report %q, %v; want none and no error", out.Report, err)
	}

	// Not asked for: the placeholder is left exactly as it was.
	dir := t.TempDir()
	inv = helper("report")
	inv.Args = append(inv.Args, "--report={report}")
	inv.Dir = dir
	out, err = newRunner().Run(bg, inv)
	if err != nil || out.Report != nil {
		t.Errorf("not asked for: report %q, %v", out.Report, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "{report}")); err != nil {
		t.Errorf("the placeholder was substituted although no report was asked for: %v", err)
	}

	// A report over the cap is cut and flagged.
	r := &Runner{Head: 500, Tail: 500, Grace: 300 * time.Millisecond, WaitDelay: 300 * time.Millisecond}
	inv = helper("bigreport")
	inv.Args = append(inv.Args, "--report={report}")
	inv.WantsReport = true
	out, err = r.Run(bg, inv)
	if err != nil || len(out.Report) != 1000 || !out.Truncated {
		t.Errorf("big report: %d bytes, truncated %v, %v; want 1000 bytes and truncated", len(out.Report), out.Truncated, err)
	}
}

func TestABackgroundProcessCannotHangTheRun(t *testing.T) {
	start := time.Now()
	out, err := newRunner().Run(bg, helper("lingering"))
	if err != nil {
		t.Fatalf("a lingering background process must not be an error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("took %v; the lingering child holds the output for 5s and must not be waited for", elapsed)
	}
	if string(out.Stdout) != "parent done" || out.ExitCode != 0 {
		t.Errorf("output %q, exit %d", out.Stdout, out.ExitCode)
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	go func() {
		time.Sleep(2 * time.Second)
		cancel()
	}()
	start := time.Now()
	out, err := newRunner().Run(ctx, helper("sleep"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want one wrapping context.Canceled", err)
	}
	if out.TimedOut {
		t.Error("a cancellation must not be marked as a timeout")
	}
	if string(out.Stdout) != "started" {
		t.Errorf("output so far was lost: %q", out.Stdout)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("took %v to stop after cancellation", elapsed)
	}

	// A context that is already cancelled never starts anything.
	done, cancelNow := context.WithCancel(bg)
	cancelNow()
	out, err = newRunner().Run(done, helper("echo"))
	if err == nil || out.Stdout != nil {
		t.Errorf("an already-cancelled context started the command: %q, %v", out.Stdout, err)
	}
}

func TestZeroValueRunnerWorks(t *testing.T) {
	out, err := (&Runner{}).Run(bg, helper("echo"))
	if err != nil || string(out.Stdout) != "out text" {
		t.Errorf("the zero Runner: %q, %v; want the defaults, with output kept", out.Stdout, err)
	}
}

func TestConcurrentRuns(t *testing.T) {
	r := newRunner()
	var wg sync.WaitGroup
	errs := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := "value-" + strconv.Itoa(i)
			out, err := r.Run(bg, helper("printenv", "RUNNER_VALUE="+want))
			if err != nil || string(out.Stdout) != want {
				errs <- fmt.Sprintf("run %d: %q, %v; want %q", i, out.Stdout, err, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

func TestNoFileHandlesAreLeaked(t *testing.T) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd on this system")
	}
	before := len(entries)
	for i := 0; i < 100; i++ {
		if _, err := newRunner().Run(bg, helper("echo")); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ = os.ReadDir("/proc/self/fd")
	if after := len(entries); after > before+3 {
		t.Errorf("%d open files before and %d after 100 runs; handles are leaking", before, after)
	}
}

func TestBoundedBuffer(t *testing.T) {
	write := func(b *boundedBuffer, chunks ...string) {
		for _, c := range chunks {
			if n, err := b.Write([]byte(c)); n != len(c) || err != nil {
				t.Fatalf("Write(%q) = %d, %v; want the full length and no error", c, n, err)
			}
		}
	}
	marker := func(n int) string { return fmt.Sprintf("\n[... %d bytes omitted ...]\n", n) }

	b := newBounded(4, 4)
	write(b, "ab", "cd", "ef", "gh")
	if got := string(b.Bytes()); got != "abcdefgh" || b.Truncated() {
		t.Errorf("exact fit: %q, truncated %v; want everything and no marker", got, b.Truncated())
	}

	b = newBounded(4, 4)
	write(b, "ab", "cd", "ef", "gh", "ij")
	if got, want := string(b.Bytes()), "abcd"+marker(2)+"ghij"; got != want || !b.Truncated() {
		t.Errorf("small writes: %q; want %q", got, want)
	}

	b = newBounded(4, 4)
	write(b, "abcdefghijklmnop") // one write larger than the tail
	if got, want := string(b.Bytes()), "abcd"+marker(8)+"mnop"; got != want {
		t.Errorf("one big write: %q; want %q", got, want)
	}

	b = newBounded(4, 4)
	write(b, "abcd", "12", "34", "56") // the tail wraps around
	if got, want := string(b.Bytes()), "abcd"+marker(2)+"3456"; got != want {
		t.Errorf("wrap-around: %q; want %q", got, want)
	}

	if got := newBounded(4, 4).Bytes(); got != nil {
		t.Errorf("nothing written gave %q; want nil", got)
	}
}
