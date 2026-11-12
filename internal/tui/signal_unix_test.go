//go:build unix

package tui

import (
	"context"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
	"github.com/gui-brant/cli-test-manager/internal/projects"
)

// A signal that stops the program must close the menu at once. Bubble Tea's own
// signal handler used to race the program's: when it lost, it waited forever to
// hand over a message nobody was left to read, and the menu never closed.
func TestASignalWhileTheMenuIsOpenClosesItAtOnce(t *testing.T) {
	for i := 0; i < 20; i++ {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		in, out := io.Pipe() // a terminal that stays silent
		deps := Deps{Projects: &projects.Service{File: filepath.Join(t.TempDir(), "projects.yaml")}, Load: manifestfile.Load}
		done := make(chan error, 1)
		go func() { done <- Run(ctx, in, io.Discard, deps) }()

		time.Sleep(100 * time.Millisecond)
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("the menu did not close %d runs in, after a signal", i)
		}
		stop()
		out.Close()
	}
}
