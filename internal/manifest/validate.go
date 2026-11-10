package manifest

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

var (
	// namePattern is what features, environments, runners and test ids may
	// look like: lowercase, digits, hyphen and underscore, starting with a
	// letter or digit.
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	// varPattern is what an environment variable name may look like.
	varPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// repoPattern is "owner/name".
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	// secretLike matches variable names that suggest a secret. The manifest is
	// committed, so such a name under `vars` is rejected: it belongs under
	// `secrets`, where only the name is written.
	secretLike = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY)`)
)

// Validate checks a parsed manifest and returns EVERY problem it finds, each
// naming the part of the file it is about (for example
// `tests[33] (login-flow): runner "http2" is not defined`). It returns nil
// when the manifest is sound. The order is stable, so the same manifest always
// gives the same report.
func Validate(m *core.Manifest) []error {
	v := &validator{m: m}
	v.header()
	v.features()
	v.environments()
	v.runners()
	v.tests()
	v.pull()
	return v.errs
}

// Report joins problems into one error for display, or returns nil when there
// are none.
func Report(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	var b strings.Builder
	noun := "problems"
	if len(errs) == 1 {
		noun = "problem"
	}
	fmt.Fprintf(&b, "manifest has %d %s:", len(errs), noun)
	for _, e := range errs {
		b.WriteString("\n  - ")
		b.WriteString(e.Error())
	}
	return errors.New(b.String())
}

type validator struct {
	m    *core.Manifest
	errs []error
}

func (v *validator) add(where, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
}

func (v *validator) header() {
	m := v.m
	switch m.Version {
	case 1:
	case 0:
		v.add("manifest_version", "is missing; this tester reads version 1")
	default:
		v.add("manifest_version", "is %d; this tester reads version 1", m.Version)
	}
	if strings.TrimSpace(m.Project) == "" {
		v.add("project", "is required")
	}
	if why := pathProblem(m.History); why != "" {
		v.add("history", "%s", why)
	}
}

func (v *validator) features() {
	seen := make(map[string]bool)
	for i, f := range v.m.Features {
		where := fmt.Sprintf("features[%d]", i)
		if !namePattern.MatchString(f) {
			v.add(where, "%q is not a valid name (lowercase letters, digits, hyphen, underscore)", f)
			continue
		}
		if seen[f] {
			v.add(where, "%q is listed twice", f)
		}
		seen[f] = true
	}
}

func (v *validator) environments() {
	envs := v.m.Environments
	if len(envs) == 0 {
		v.add("environments", "at least one environment is required")
		return
	}
	for _, name := range sortedKeys(envs) {
		env := envs[name]
		where := "environments." + name
		if !namePattern.MatchString(name) {
			v.add(where, "%q is not a valid name (lowercase letters, digits, hyphen, underscore)", name)
		}

		for _, key := range sortedKeys(env.Vars) {
			switch {
			case !varPattern.MatchString(key):
				v.add(where+".vars", "%q is not a valid variable name", key)
			case secretLike.MatchString(key):
				v.add(where+".vars", "%q looks like a secret; declare it under secrets, where only its name is written", key)
			}
		}

		seen := make(map[string]bool)
		for _, s := range env.Secrets {
			switch {
			case !varPattern.MatchString(s):
				v.add(where+".secrets", "%q is not a valid variable name (a name only, never name=value)", s)
			case seen[s]:
				v.add(where+".secrets", "%q is listed twice", s)
			}
			if _, isVar := env.Vars[s]; isVar {
				v.add(where, "%q is both a variable and a secret", s)
			}
			seen[s] = true
		}
	}
}

func (v *validator) runners() {
	runners := v.m.Runners
	if len(runners) == 0 {
		v.add("runners", "at least one runner is required")
		return
	}
	for _, name := range sortedKeys(runners) {
		r := runners[name]
		where := "runners." + name
		if !namePattern.MatchString(name) {
			v.add(where, "%q is not a valid name (lowercase letters, digits, hyphen, underscore)", name)
		}

		hasReport := v.command(where, r.Run)

		switch r.Format {
		case core.FormatTAP, core.FormatJUnit:
		default:
			v.add(where+".format", "%q is not %q or %q", r.Format, core.FormatTAP, core.FormatJUnit)
		}
		switch r.Output {
		case core.OutputStdout:
			if hasReport {
				v.add(where+".run", "contains %s but output is %q; set output to %q or remove it", core.PlaceholderReport, r.Output, core.OutputFile)
			}
		case core.OutputFile:
			if !hasReport {
				v.add(where+".run", "output is %q, so the command must contain %s", r.Output, core.PlaceholderReport)
			}
		default:
			v.add(where+".output", "%q is not %q or %q", r.Output, core.OutputStdout, core.OutputFile)
		}

		if len(r.Environments) == 0 {
			v.add(where+".environments", "at least one environment is required")
		}
		seen := make(map[string]bool)
		for _, e := range r.Environments {
			if _, ok := v.m.Environments[e]; !ok {
				v.add(where+".environments", "environment %q is not defined", e)
			}
			if seen[e] {
				v.add(where+".environments", "%q is listed twice", e)
			}
			seen[e] = true
		}

		if r.Timeout < MinTimeout {
			v.add(where+".timeout", "is %v; it must be at least %v (for example 10m)", r.Timeout, MinTimeout)
		}
	}
}

// command checks a runner's argument list and reports whether it contains the
// report placeholder.
func (v *validator) command(where string, args []string) (hasReport bool) {
	if len(args) == 0 {
		v.add(where+".run", "the command is empty")
		return false
	}
	if strings.TrimSpace(args[0]) == "" {
		v.add(where+".run[0]", "the program name is empty")
	}
	files := 0
	for i, a := range args {
		switch {
		case a == core.PlaceholderFiles:
			files++
		case strings.Contains(a, core.PlaceholderFiles):
			v.add(fmt.Sprintf("%s.run[%d]", where, i), "%s must be an argument of its own, not part of %q", core.PlaceholderFiles, a)
		}
		if strings.Contains(a, core.PlaceholderReport) {
			hasReport = true
		}
	}
	switch {
	case files == 0:
		v.add(where+".run", "must contain %s as an argument of its own", core.PlaceholderFiles)
	case files > 1:
		v.add(where+".run", "contains %s %d times; use it once", core.PlaceholderFiles, files)
	}
	return hasReport
}

func (v *validator) tests() {
	ids := make(map[string]int)
	files := make(map[string]int)
	for i, t := range v.m.Tests {
		label := t.ID
		if label == "" {
			label = "no id"
		}
		where := fmt.Sprintf("tests[%d] (%s)", i, label)

		switch {
		case t.ID == "":
			v.add(where, "id is required")
		case !namePattern.MatchString(t.ID):
			v.add(where, "id %q is not valid (lowercase letters, digits, hyphen, underscore)", t.ID)
		default:
			if first, dup := ids[t.ID]; dup {
				v.add(where, "id %q is already used by tests[%d]", t.ID, first)
			} else {
				ids[t.ID] = i
			}
		}

		if strings.TrimSpace(t.Name) == "" {
			v.add(where, "name is required")
		}

		switch {
		case t.Feature == "":
			v.add(where, "feature is required (use %q for a check that belongs to no feature)", core.GeneralFeature)
		case !v.m.HasFeature(t.Feature):
			v.add(where, "feature %q is not listed under features", t.Feature)
		}

		switch _, ok := v.m.Runners[t.Runner]; {
		case t.Runner == "":
			v.add(where, "runner is required")
		case !ok:
			v.add(where, "runner %q is not defined", t.Runner)
		}

		if why := pathProblem(t.File); why != "" {
			v.add(where, "file %s", why)
		} else if first, dup := files[t.File]; dup {
			v.add(where, "file %q is already used by tests[%d]", t.File, first)
		} else {
			files[t.File] = i
		}
	}
}

func (v *validator) pull() {
	p := v.m.Pull
	if p == nil {
		return
	}
	if p.Provider != "github" {
		v.add("pull.provider", "%q is not supported; only %q", p.Provider, "github")
	}
	if !repoPattern.MatchString(p.Repo) {
		v.add("pull.repo", "%q is not of the form owner/name", p.Repo)
	}
	if strings.TrimSpace(p.Workflow) == "" {
		v.add("pull.workflow", "is required")
	}
	if strings.TrimSpace(p.Artifact) == "" {
		v.add("pull.artifact", "is required")
	}
	switch _, ok := v.m.Environments[p.Environment]; {
	case p.Environment == "":
		v.add("pull.environment", "is required: pulled results must say which environment they belong to")
	case !ok:
		v.add("pull.environment", "environment %q is not defined", p.Environment)
	}
	if p.LookbackDays < 1 {
		v.add("pull.lookback_days", "is %d; it must be at least 1", p.LookbackDays)
	}
	if !varPattern.MatchString(p.Token) {
		v.add("pull.token", "%q is not a valid variable name (a name only, never the value)", p.Token)
	} else {
		for _, name := range sortedKeys(v.m.Environments) {
			for _, s := range v.m.Environments[name].Secrets {
				if s == p.Token {
					v.add("pull.token", "%q is also a secret of environment %q; the pull token must never be handed to a test command", p.Token, name)
				}
			}
		}
	}
}

// pathProblem says what is wrong with a manifest path, or returns "" when it
// is a clean relative path that stays inside the project. The wording
// continues a sentence such as "file ..." or "history: ...".
func pathProblem(p string) string {
	switch {
	case p == "":
		return "is empty"
	case path.IsAbs(p):
		return fmt.Sprintf("%q is absolute; use a path relative to the project", p)
	}
	c := path.Clean(p)
	switch {
	case c == ".." || strings.HasPrefix(c, "../"):
		return fmt.Sprintf("%q climbs out of the project folder", p)
	case c == ".":
		return fmt.Sprintf("%q is the project folder itself, not a file", p)
	case c != p:
		return fmt.Sprintf("%q is not a clean relative path (no ./ or .., doubled or trailing slash); write %q", p, c)
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
