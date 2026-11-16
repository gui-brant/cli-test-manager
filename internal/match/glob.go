package match

import (
	"path"
	"strings"
)

// Glob reports whether name matches pattern. Both are slash-separated paths
// relative to the project, as the manifest writes them. A segment is matched as
// path.Match does (`*`, `?`, `[a-z]`), and a segment that is exactly `**`
// matches any number of whole folders, none included:
//
//	tests/**           every file under tests, at any depth
//	supabase/**/*.sql  a .sql file anywhere under supabase
//	**/login*          a file whose name starts with login, in any folder
//
// A malformed pattern matches nothing. The manifest checks patterns when it is
// loaded, so that never reaches a run.
func Glob(pattern, name string) bool {
	return globSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func globSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if globSegments(pat, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// GlobAny reports whether name matches any of the patterns.
func GlobAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if Glob(p, name) {
			return true
		}
	}
	return false
}
