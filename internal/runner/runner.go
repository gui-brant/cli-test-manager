// Package runner starts test commands as child processes. It is the adapter
// behind core.Runner: the only code in the tester that starts a process.
//
// A command runs in its own process group, with exactly the environment it is
// given, an empty standard input, and a bounded amount of captured output. A
// command that overruns its timeout, or whose run is cancelled, is stopped
// together with everything it started: a polite terminate signal first, then a
// forced kill after a grace period.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const (
	// DefaultHeadBytes and DefaultTailBytes are how much of the start and of
	// the end of each output stream is kept.
	DefaultHeadBytes = 8 << 20
	DefaultTailBytes = 8 << 20
	// DefaultGrace is how long a stopping command has to clean up before it is
	// killed.
	DefaultGrace = 5 * time.Second
	// DefaultWaitDelay bounds how long the runner waits for output to close
	// after the command itself has exited, so a background process that holds
	// the output open cannot hang the run.
	DefaultWaitDelay = 2 * time.Second
)

// Runner starts commands. The zero value works: zero fields mean the defaults.
type Runner struct {
	// Head and Tail are the bytes kept from the start and the end of each
	// output stream, and of the result file together. When both are zero the
	// defaults apply.
	Head, Tail int
	// Grace is how long a command being stopped has before it is killed.
	Grace time.Duration
	// WaitDelay is how long to wait for output to close after the command has
	// exited.
	WaitDelay time.Duration
}

var _ core.Runner = (*Runner)(nil)

// New returns a Runner with the default limits.
func New() *Runner {
	return &Runner{Head: DefaultHeadBytes, Tail: DefaultTailBytes, Grace: DefaultGrace, WaitDelay: DefaultWaitDelay}
}

func (r *Runner) limits() (head, tail int, grace, waitDelay time.Duration) {
	head, tail, grace, waitDelay = r.Head, r.Tail, r.Grace, r.WaitDelay
	if head <= 0 && tail <= 0 {
		head, tail = DefaultHeadBytes, DefaultTailBytes
	}
	if grace <= 0 {
		grace = DefaultGrace
	}
	if waitDelay <= 0 {
		waitDelay = DefaultWaitDelay
	}
	return head, tail, grace, waitDelay
}

// Run starts the command and waits for it. See core.Runner for the contract.
func (r *Runner) Run(ctx context.Context, inv core.Invocation) (core.Output, error) {
	if len(inv.Args) == 0 || inv.Args[0] == "" {
		return core.Output{}, errors.New("runner: the command is empty")
	}
	if err := ctx.Err(); err != nil {
		return core.Output{}, fmt.Errorf("runner: not started: %w", err)
	}
	head, tail, grace, waitDelay := r.limits()
	args := append([]string(nil), inv.Args...)

	// The result file lives in a private folder of its own, and the tool is
	// given the path of a file that does not exist yet, which some tools need.
	var reportPath string
	if inv.WantsReport {
		dir, err := os.MkdirTemp("", "tester-report-")
		if err != nil {
			return core.Output{}, fmt.Errorf("runner: making a place for the result file: %w", err)
		}
		defer os.RemoveAll(dir)
		reportPath = filepath.Join(dir, "report")
		for i, a := range args {
			args[i] = strings.ReplaceAll(a, core.PlaceholderReport, reportPath)
		}
	}

	stdout, stderr := newBounded(head, tail), newBounded(head, tail)
	cmd := exec.Command(args[0], args[1:]...) // stopped by us below, not by the context
	cmd.Dir = inv.Dir
	cmd.Env = append([]string{}, inv.Env...) // never nil: nil would inherit the tester's environment
	cmd.Stdout, cmd.Stderr = stdout, stderr  // standard input stays empty
	cmd.WaitDelay = waitDelay
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return core.Output{}, fmt.Errorf("runner: starting %q: %w", args[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeout <-chan time.Time
	if inv.Timeout > 0 {
		timer := time.NewTimer(inv.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	var waitErr error
	timedOut, cancelled := false, false
	select {
	case waitErr = <-done:
	case <-timeout:
		timedOut = true
		waitErr = stop(cmd, done, grace)
	case <-ctx.Done():
		cancelled = true
		waitErr = stop(cmd, done, grace)
	}

	code := 0
	if ps := cmd.ProcessState; ps != nil {
		code = ps.ExitCode()
		if code < 0 {
			code = signalExitCode(ps)
		}
	}
	out := core.Output{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		ExitCode:  code,
		TimedOut:  timedOut,
		Truncated: stdout.Truncated() || stderr.Truncated(),
	}
	if inv.WantsReport {
		report, cut := readReport(reportPath, head+tail)
		out.Report = report
		out.Truncated = out.Truncated || cut
	}

	// A non-zero exit and a command output that outlived it are reported in the
	// output, not as errors. Anything else from waiting is a real failure.
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return out, fmt.Errorf("runner: waiting for %q: %w", args[0], waitErr)
	}
	if cancelled {
		return out, fmt.Errorf("runner: stopped: %w", ctx.Err())
	}
	return out, nil
}

// stop ends a running command: a terminate signal to its whole group, then a
// wait for the group to go away, then a kill of whatever is left once the grace
// period is over. It returns what Wait returned for the command.
func stop(cmd *exec.Cmd, done <-chan error, grace time.Duration) error {
	terminateGroup(cmd)

	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	var waitErr error
	exited := false
	for {
		select {
		case waitErr = <-done:
			exited, done = true, nil // a nil channel is never ready again
		case <-tick.C:
		case <-deadline.C:
			killGroup(cmd)
			if !exited {
				waitErr = <-done
			}
			return waitErr
		}
		if exited && !groupAlive(cmd) {
			return waitErr
		}
	}
}

// readReport reads the temporary result file, keeping at most limit bytes of
// its start. A file the command did not write is no report, not an error.
func readReport(path string, limit int) (data []byte, truncated bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false
	}
	if len(data) > limit {
		return data[:limit], true
	}
	return data, false
}

// boundedBuffer keeps the first head bytes and the last tail bytes written to
// it and counts what it drops in between. It never blocks and never fails, so
// a command that prints without limit is drained, not stalled by a full pipe.
// It is used by one goroutine at a time.
type boundedBuffer struct {
	head, tail int
	headBuf    bytes.Buffer
	ring       []byte // the last tail bytes, as a circular buffer; allocated only when needed
	w          int    // next write position in ring
	filled     int    // bytes held in ring
	dropped    int64
}

func newBounded(head, tail int) *boundedBuffer { return &boundedBuffer{head: head, tail: tail} }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.head - b.headBuf.Len(); room > 0 {
		take := min(room, len(p))
		b.headBuf.Write(p[:take])
		p = p[take:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if b.tail <= 0 {
		b.dropped += int64(len(p))
		return n, nil
	}
	if b.ring == nil {
		b.ring = make([]byte, b.tail)
	}
	if len(p) >= b.tail {
		// One write fills the whole tail: everything held so far, and the front
		// of this write, is dropped.
		b.dropped += int64(b.filled) + int64(len(p)-b.tail)
		copy(b.ring, p[len(p)-b.tail:])
		b.w, b.filled = 0, b.tail
		return n, nil
	}
	if over := b.filled + len(p) - b.tail; over > 0 {
		b.dropped += int64(over) // the oldest bytes are overwritten
	}
	for len(p) > 0 {
		c := copy(b.ring[b.w:], p)
		p = p[c:]
		b.w = (b.w + c) % b.tail
		b.filled = min(b.tail, b.filled+c)
	}
	return n, nil
}

// Bytes returns the kept start, then a marker line when something was
// dropped, then the kept end.
func (b *boundedBuffer) Bytes() []byte {
	out := append([]byte(nil), b.headBuf.Bytes()...)
	if b.dropped > 0 {
		out = append(out, fmt.Sprintf("\n[... %d bytes omitted ...]\n", b.dropped)...)
	}
	switch {
	case b.filled == 0:
	case b.filled < b.tail:
		out = append(out, b.ring[:b.filled]...)
	default: // full: the oldest byte is at w
		out = append(out, b.ring[b.w:]...)
		out = append(out, b.ring[:b.w]...)
	}
	return out
}

// Truncated reports whether any output was dropped.
func (b *boundedBuffer) Truncated() bool { return b.dropped > 0 }
