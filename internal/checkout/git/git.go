// Package git is a core.Checkout that asks git which commit a folder is at and
// whether it has uncommitted changes. It is the only code that starts git, so
// the services stay free of it and a project kept some other way can bring its
// own implementation.
package git

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// timeout bounds each git command, so a stuck repository (a network drive, a
// lock) cannot hold up a push hook.
const timeout = 10 * time.Second

// Git implements core.Checkout with the git program on the search path.
type Git struct{}

var _ core.Checkout = Git{}

// New returns a Git checkout.
func New() Git { return Git{} }

// State returns HEAD's commit and whether the working tree has uncommitted
// changes, untracked files included and ignored files not. Anything git cannot
// answer (not a repository, no commits yet, git missing, a timeout) gives an
// empty commit and false: "not known", never an error.
//
// It reports the repository that contains dir, so a project kept inside a
// larger repository gets that repository's commit.
func (Git) State(ctx context.Context, dir string) (commit string, dirty bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if out, ok := run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); ok {
		commit = strings.TrimSpace(out)
	}
	// Without a commit the tree is not "dirty" in a way a run can use, but
	// untracked files in a new repository still are changes.
	if out, ok := run(ctx, dir, "status", "--porcelain", "--untracked-files=normal"); ok {
		dirty = strings.TrimSpace(out) != ""
	}
	return commit, dirty
}

// run starts git in dir and returns its standard output. ok is false when git
// could not start or exited non-zero.
//
// GIT_OPTIONAL_LOCKS=0 stops `status` from refreshing the index, so asking
// never changes the repository (and never races a commit running beside it).
func run(ctx context.Context, dir string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}
