package execute

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const (
	// versionTimeout is how long a runner's version command may run. It is not
	// a manifest setting: a version is printed at once or something is wrong.
	versionTimeout = 10 * time.Second

	// maxVersionRunes cuts a version line that is not one, so a command that
	// prints a banner or a page cannot swell the history.
	maxVersionRunes = 200

	// noVersionCommand is recorded for a runner that declares none, so the gap
	// is visible in history instead of the tool list looking complete.
	noVersionCommand = "unknown: no version command declared"
)

// toolVersions reads the version of each runner the run is about to use, once,
// before the first test, in runner-name order. A runner that declares no
// version command, or whose command cannot be read, is recorded as
// "unknown: <reason>". Reading a version never fails the run.
//
// Each command gets exactly the environment the tests get, in the project
// folder, and its output is scrubbed of the run's secrets before it is kept.
func (s *Service) toolVersions(ctx context.Context, req Request, p *plan) []core.Tool {
	used := make(map[string]bool)
	for _, t := range p.tests {
		used[t.Runner] = true
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)

	tools := make([]core.Tool, 0, len(names))
	for _, name := range names {
		cmd := req.Manifest.Runners[name].Version
		if len(cmd) == 0 {
			tools = append(tools, core.Tool{Name: name, Version: noVersionCommand})
			continue
		}
		out, err := s.Runner.Run(ctx, core.Invocation{Args: cmd, Env: p.env, Dir: req.Root, Timeout: versionTimeout})
		tools = append(tools, core.Tool{Name: name, Version: p.versionText(out, err)})
	}
	return tools
}

// versionText is the first line the version command printed (standard output
// first, then standard error, since some tools print their version there), or
// "unknown: <why>" when there is none to use.
func (p *plan) versionText(out core.Output, err error) string {
	switch {
	case err != nil:
		return "unknown: could not run the command: " + p.redactor.Redact(err.Error())
	case out.TimedOut:
		return fmt.Sprintf("unknown: timed out after %s", versionTimeout)
	case out.ExitCode != 0:
		return fmt.Sprintf("unknown: the command exited with code %d", out.ExitCode)
	}
	for _, stream := range [][]byte{out.Stdout, out.Stderr} {
		if line := firstLine(p.redactor.Redact(string(stream))); line != "" {
			return line
		}
	}
	return "unknown: the command printed nothing"
}

// firstLine returns the first non-empty line of text, trimmed and cut to
// maxVersionRunes (with an ellipsis when cut).
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxVersionRunes {
			return string([]rune(line)[:maxVersionRunes]) + "…"
		}
		return line
	}
	return ""
}
