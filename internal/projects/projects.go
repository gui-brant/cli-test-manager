// Package projects keeps the list of projects the menu offers.
//
// The list is a small file: for each project, the name its manifest gives it,
// an optional nickname the owner chose, and the folder that holds the manifest.
// It holds nothing else; each project's manifest and history stay in the
// project. Losing the file loses only the list and the nicknames, and the list
// is rebuilt as projects are used.
package projects

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
)

const listFileName = "projects.yaml"

const listHeader = "# The projects this program knows about: names, nicknames and folders.\n" +
	"# Safe to delete; it is rebuilt as projects are used (nicknames are lost).\n"

// DefaultFile is where the list lives for this user: a "tester" folder inside
// the user's config folder (~/.config on Linux).
func DefaultFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("projects: this user has no config folder: %w", err)
	}
	return filepath.Join(dir, "tester", listFileName), nil
}

// Project is a project the program knows about.
type Project struct {
	// Name is the project's name from its manifest, refreshed each time the
	// project is used.
	Name string `yaml:"name"`
	// Nickname is a name the owner chose. It is never overwritten by the
	// manifest's name. Empty when there is none.
	Nickname string `yaml:"nickname,omitempty"`
	// Path is the absolute path of the folder that holds its manifest. It is
	// what identifies the project: two projects may share a name.
	Path string `yaml:"path"`
}

// Display is the name to show and to match: the nickname when there is one,
// else the manifest's name.
func (p Project) Display() string {
	if p.Nickname != "" {
		return p.Nickname
	}
	return p.Name
}

// Entry is a listed project, with whether its folder still has a manifest.
type Entry struct {
	Project
	// Missing is true when the folder or its manifest is gone, so the menu can
	// show it as "folder not found" instead of hiding it.
	Missing bool
}

// Asker is called when a project is newly added and no nickname was given. It
// is handed the name the project will be listed under and returns a nickname,
// or "" to keep the name. A nil Asker means nobody can be asked.
type Asker func(name string) string

// CheckNickname trims a nickname and says whether it can be used. "" is valid and
// means no nickname. A nickname cannot contain a path separator or be "." or
// "..", so it can never be mistaken for a folder, nor contain control characters.
func CheckNickname(nick string) (string, error) {
	nick = strings.TrimSpace(nick)
	if nick == "." || nick == ".." || strings.ContainsAny(nick, `/\`) {
		return "", fmt.Errorf("projects: the nickname %q cannot be used: it must not contain a path separator or be . or ..", nick)
	}
	for _, r := range nick {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("projects: a nickname cannot contain control characters")
		}
	}
	return nick, nil
}

// AmbiguousError is returned when a name belongs to several listed projects.
type AmbiguousError struct {
	Name  string
	Paths []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("projects: %d projects are named %q; give the folder of the one you mean:\n  %s",
		len(e.Paths), e.Name, strings.Join(e.Paths, "\n  "))
}

// Service reads and writes the list file.
type Service struct {
	// File is the list file's path. Its folder is created when first needed.
	File string
}

type listFile struct {
	Projects []Project `yaml:"projects"`
}

// List returns the listed projects in the order they were added. A missing
// list file is an empty list.
func (s *Service) List() ([]Entry, error) {
	list, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(list))
	for _, p := range list {
		_, statErr := os.Stat(filepath.Join(p.Path, manifestfile.FileName))
		out = append(out, Entry{Project: p, Missing: statErr != nil})
	}
	return out, nil
}

// Add reads the manifest in dir and remembers the project. A nickname given here
// is set (also for a project already listed); with none, ask is called if the
// project is new. added is false when the folder was already listed. A folder
// with no manifest is an error and nothing is written.
func (s *Service) Add(dir, nickname string, ask Asker) (p Project, added bool, err error) {
	nickname, err = CheckNickname(nickname)
	if err != nil {
		return Project{}, false, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, false, fmt.Errorf("projects: %w", err)
	}
	loaded, err := manifestfile.Load(filepath.Join(abs, manifestfile.FileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Project{}, false, fmt.Errorf("projects: there is no %s in %s: %w", manifestfile.FileName, abs, err)
		}
		return Project{}, false, err
	}
	return s.remember(Project{Name: loaded.Manifest.Project, Path: loaded.Root}, nickname, ask)
}

// Use records that a project is being used: it lists the project if its folder
// is not listed yet (calling ask for a nickname if one is given), and refreshes
// the manifest's name when the folder is listed under an old one. A nickname is
// never overwritten. added is false when the folder was already listed. Nothing
// is written when nothing changed.
func (s *Service) Use(p Project, ask Asker) (Project, bool, error) {
	return s.remember(p, "", ask)
}

// Remember is Use with nobody to ask.
func (s *Service) Remember(p Project) (added bool, err error) {
	_, added, err = s.remember(p, "", nil)
	return added, err
}

func (s *Service) remember(p Project, nickname string, ask Asker) (Project, bool, error) {
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return Project{}, false, fmt.Errorf("projects: %w", err)
	}
	p.Path = abs

	list, err := s.read()
	if err != nil {
		return Project{}, false, err
	}
	for i, have := range list {
		if have.Path != p.Path {
			continue
		}
		changed := false
		if have.Name != p.Name {
			list[i].Name, changed = p.Name, true
		}
		if nickname != "" && nickname != have.Nickname {
			list[i].Nickname, changed = nickname, true
		}
		if !changed {
			return have, false, nil
		}
		return list[i], false, s.write(list)
	}

	if nickname == "" && ask != nil {
		nickname = ask(p.Name)
	}
	p.Nickname = nickname
	return p, true, s.write(append(list, p))
}

// Rename sets the nickname of one project, found by key (see Remove), and
// returns it with the name it was shown under before. An empty nickname clears
// it. The project's folder is never touched.
func (s *Service) Rename(key, nickname string) (p Project, previous string, err error) {
	nickname, err = CheckNickname(nickname)
	if err != nil {
		return Project{}, "", err
	}
	list, err := s.read()
	if err != nil {
		return Project{}, "", err
	}
	i, err := find(list, key)
	if err != nil {
		return Project{}, "", err
	}
	previous = list[i].Display()
	list[i].Nickname = nickname
	return list[i], previous, s.write(list)
}

// Remove forgets one project and returns it. The key is a folder, a nickname or
// a manifest name; a folder is exact, so it wins. A name shared by several
// projects is an *AmbiguousError and nothing is removed. A key that matches
// nothing wraps core.ErrNotFound. The project's folder is never touched.
func (s *Service) Remove(key string) (Project, error) {
	list, err := s.read()
	if err != nil {
		return Project{}, err
	}
	i, err := find(list, key)
	if err != nil {
		return Project{}, err
	}
	p := list[i]
	return p, s.write(append(list[:i:i], list[i+1:]...))
}

// find returns the index of the one project the key names.
func find(list []Project, key string) (int, error) {
	if abs, err := filepath.Abs(key); err == nil {
		for i, p := range list {
			if p.Path == abs {
				return i, nil
			}
		}
	}

	var at []int
	for i, p := range list {
		if p.Nickname == key || p.Name == key {
			at = append(at, i)
		}
	}
	switch len(at) {
	case 0:
		return 0, fmt.Errorf("projects: no project in the list is named %q or lives in that folder: %w", key, core.ErrNotFound)
	case 1:
		return at[0], nil
	}
	amb := &AmbiguousError{Name: key}
	for _, i := range at {
		amb.Paths = append(amb.Paths, list[i].Path)
	}
	return 0, amb
}

// read returns the list as written. A list that is not valid is an error that
// names the file, and the file is left as it is.
func (s *Service) read() ([]Project, error) {
	data, err := os.ReadFile(s.File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("projects: reading %s: %w", s.File, err)
	}

	var f listFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("projects: %s is not a valid project list (fix it or delete it): %w", s.File, err)
	}
	for _, p := range f.Projects {
		if !filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("projects: %s is not a valid project list (fix it or delete it): the path %q is not absolute", s.File, p.Path)
		}
	}
	return f.Projects, nil
}

// write replaces the list file in one step: the new text goes to a temporary
// file in the same folder, which is then renamed over the list, so a reader
// never sees half a file.
func (s *Service) write(list []Project) error {
	dir := filepath.Dir(s.File)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("projects: %w", err)
	}
	body, err := yaml.Marshal(listFile{Projects: list})
	if err != nil {
		return fmt.Errorf("projects: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".projects-*.tmp")
	if err != nil {
		return fmt.Errorf("projects: %w", err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.WriteString(listHeader + string(body))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("projects: writing %s: %w", s.File, err)
	}
	if err := os.Rename(tmpName, s.File); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("projects: writing %s: %w", s.File, err)
	}
	return nil
}
