// Package projects keeps the list of projects the menu offers.
//
// The list is a small file of locations: a project's name and the folder that
// holds its manifest. It holds nothing else; each project's manifest and
// history stay in the project. Losing the file loses only the list, which is
// rebuilt as projects are used.
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

	"go.yaml.in/yaml/v3"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/manifestfile"
)

const listFileName = "projects.yaml"

const listHeader = "# The projects this program knows about: locations only.\n" +
	"# Safe to delete; it is rebuilt as projects are used.\n"

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
	// Name is the project's name, from its manifest when it was last used.
	Name string `yaml:"name"`
	// Path is the absolute path of the folder that holds its manifest. It is
	// what identifies the project: two projects may share a name.
	Path string `yaml:"path"`
}

// Entry is a listed project, with whether its folder still has a manifest.
type Entry struct {
	Project
	// Missing is true when the folder or its manifest is gone, so the menu can
	// show it as "folder not found" instead of hiding it.
	Missing bool
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

// Add reads the manifest in dir and remembers the project. added is false when
// the folder was already listed. A folder with no manifest is an error and
// nothing is written.
func (s *Service) Add(dir string) (p Project, added bool, err error) {
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
	p = Project{Name: loaded.Manifest.Project, Path: loaded.Root}
	added, err = s.Remember(p)
	return p, added, err
}

// Remember lists the project if its folder is not listed yet, and refreshes the
// name when the folder is listed under an old one. added is false when the
// folder was already listed. Nothing is written when nothing changed.
func (s *Service) Remember(p Project) (added bool, err error) {
	abs, err := filepath.Abs(p.Path)
	if err != nil {
		return false, fmt.Errorf("projects: %w", err)
	}
	p.Path = abs

	list, err := s.read()
	if err != nil {
		return false, err
	}
	for i, have := range list {
		if have.Path != p.Path {
			continue
		}
		if have.Name == p.Name {
			return false, nil
		}
		list[i].Name = p.Name
		return false, s.write(list)
	}
	return true, s.write(append(list, p))
}

// Remove forgets one project and returns it. The key is a folder or a name; a
// folder is exact, so it wins. A name shared by several projects is an
// *AmbiguousError and nothing is removed. A key that matches nothing wraps
// core.ErrNotFound. The project's folder is never touched.
func (s *Service) Remove(key string) (Project, error) {
	list, err := s.read()
	if err != nil {
		return Project{}, err
	}

	if abs, absErr := filepath.Abs(key); absErr == nil {
		for i, p := range list {
			if p.Path == abs {
				return p, s.write(append(list[:i:i], list[i+1:]...))
			}
		}
	}

	var at []int
	for i, p := range list {
		if p.Name == key {
			at = append(at, i)
		}
	}
	switch len(at) {
	case 0:
		return Project{}, fmt.Errorf("projects: no project in the list is named %q or lives in that folder: %w", key, core.ErrNotFound)
	case 1:
		p := list[at[0]]
		return p, s.write(append(list[:at[0]:at[0]], list[at[0]+1:]...))
	}
	amb := &AmbiguousError{Name: key}
	for _, i := range at {
		amb.Paths = append(amb.Paths, list[i].Path)
	}
	return Project{}, amb
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
