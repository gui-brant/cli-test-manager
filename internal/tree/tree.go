// Package tree is a core.Tree: it lists a project's files with git and reads
// them from disk. It is the only code that does either for a snapshot, so the
// services stay free of both.
package tree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

// listTimeout bounds `git ls-files`, so a stuck repository cannot hold up a
// push hook.
const listTimeout = 30 * time.Second

// maxRead is the largest file Read returns. A bigger one is an error: the
// snapshot only needs to know it is too large, and reading a huge file into
// memory to say so would be a cost with no use.
var maxRead int64 = 64 << 20

// Tree implements core.Tree.
type Tree struct{}

var _ core.Tree = Tree{}

// New returns a Tree.
func New() Tree { return Tree{} }

// Files lists what git tracks plus what it does not track and does not ignore,
// under root, as slash paths relative to root. ok is false when git cannot say.
// A file git tracks but that has been deleted from disk is still listed; Read
// reports it.
func (Tree) Files(ctx context.Context, root string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	seen := make(map[string]bool)
	var files []string
	for _, name := range strings.Split(stdout.String(), "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		files = append(files, name)
	}
	return files, true
}

// Read returns the content of root/rel. rel must be a clean relative path with
// slashes that stays inside root, and the file must be a regular file whose real
// location (after following symbolic links) is also inside root.
func (Tree) Read(root, rel string) ([]byte, error) {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fmt.Errorf("%q is not a path inside the project", rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("project folder: %w", unwrapPath(err))
	}
	real, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil, unwrapPath(err)
	}
	if r, err := filepath.Rel(realRoot, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%q resolves to a place outside the project", rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, unwrapPath(err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", rel)
	}
	if info.Size() > maxRead {
		return nil, fmt.Errorf("%q is larger than %d MB", rel, maxRead>>20)
	}
	return os.ReadFile(real)
}

// unwrapPath keeps the cause of a file error without the path, which the
// caller already knows.
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
