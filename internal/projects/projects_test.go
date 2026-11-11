package projects

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gui-brant/cli-test-manager/internal/core"
)

const manifestFor = `
manifest_version: 1
project: %s
history: .tester/history.sqlite
features: [birds]
environments:
  deployed: {}
runners:
  node:
    run: ["node", "{files}"]
    format: tap
    output: stdout
    environments: [deployed]
    timeout: 1m
tests:
  - { id: one, name: One, feature: birds, runner: node, file: a.mjs }
`

// project makes a folder with a valid manifest for a project of this name.
func project(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	text := strings.Replace(manifestFor, "%s", name, 1)
	if err := os.WriteFile(filepath.Join(dir, "test-manifest.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// service uses a list file in a folder that does not exist yet, as on first use.
func service(t *testing.T) *Service {
	t.Helper()
	return &Service{File: filepath.Join(t.TempDir(), "config", "tester", "projects.yaml")}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFirstUseCreatesTheListAndRemembersOnce(t *testing.T) {
	s := service(t)
	lab3 := project(t, "se3316-lab3-birdwatching")

	added, err := s.Remember(Project{Name: "se3316-lab3-birdwatching", Path: lab3})
	if err != nil || !added {
		t.Fatalf("first Remember: added %v, err %v; want added", added, err)
	}
	first := read(t, s.File)
	for _, want := range []string{"name: se3316-lab3-birdwatching", "path: " + lab3, "Safe to delete"} {
		if !strings.Contains(first, want) {
			t.Errorf("the list file lacks %q:\n%s", want, first)
		}
	}

	added, err = s.Remember(Project{Name: "se3316-lab3-birdwatching", Path: lab3})
	if err != nil || added {
		t.Fatalf("second Remember: added %v, err %v; want not added", added, err)
	}
	if again := read(t, s.File); again != first {
		t.Errorf("a second Remember changed the file:\n%s", again)
	}
}

func TestAddReadsTheManifestAndStoresAnAbsolutePath(t *testing.T) {
	s := service(t)
	dir := project(t, "photo-app")
	t.Chdir(dir)

	p, added, err := s.Add(".") // a relative path, from inside the project
	if err != nil || !added {
		t.Fatalf("Add: %v, added %v", err, added)
	}
	if p.Name != "photo-app" || !filepath.IsAbs(p.Path) {
		t.Errorf("project %+v; want the manifest's name and an absolute path", p)
	}
	got, err := s.List()
	if err != nil || len(got) != 1 || got[0].Name != "photo-app" || got[0].Missing {
		t.Errorf("List = %+v, %v; want photo-app, found", got, err)
	}
}

func TestAddNeedsAManifest(t *testing.T) {
	s := service(t)
	empty := t.TempDir()

	_, _, err := s.Add(empty)
	if err == nil || !strings.Contains(err.Error(), "test-manifest.yaml") {
		t.Fatalf("Add of a folder with no manifest: %v; want an error naming test-manifest.yaml", err)
	}
	if _, statErr := os.Stat(s.File); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a failed Add created the list file")
	}
}

func TestListKeepsTheOrderAndMarksAMissingFolder(t *testing.T) {
	s := service(t)
	lab3 := project(t, "se3316-lab3-birdwatching")
	photo := project(t, "photo-app")
	s.Remember(Project{Name: "se3316-lab3-birdwatching", Path: lab3})
	s.Remember(Project{Name: "photo-app", Path: photo})

	if err := os.RemoveAll(photo); err != nil {
		t.Fatal(err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "se3316-lab3-birdwatching" || got[0].Missing || got[1].Name != "photo-app" || !got[1].Missing {
		t.Errorf("List = %+v; want lab3 found, then photo-app missing, in the order added", got)
	}
}

func TestListOfNoFileIsEmpty(t *testing.T) {
	got, err := service(t).List()
	if err != nil || len(got) != 0 {
		t.Errorf("List with no file = %+v, %v; want empty", got, err)
	}
}

func TestRemoveByNameAndByFolderLeavesTheFolderAlone(t *testing.T) {
	s := service(t)
	lab3 := project(t, "se3316-lab3-birdwatching")
	photo := project(t, "photo-app")
	s.Remember(Project{Name: "se3316-lab3-birdwatching", Path: lab3})
	s.Remember(Project{Name: "photo-app", Path: photo})

	p, err := s.Remove("photo-app")
	if err != nil || p.Path != photo {
		t.Fatalf("Remove by name: %+v, %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(photo, "test-manifest.yaml")); err != nil {
		t.Errorf("the project's own manifest was touched: %v", err)
	}
	if got, _ := s.List(); len(got) != 1 || got[0].Name != "se3316-lab3-birdwatching" {
		t.Errorf("after removing photo-app, List = %+v; want only lab3", got)
	}

	if _, err := s.Remove(lab3); err != nil { // by folder
		t.Fatalf("Remove by folder: %v", err)
	}
	if got, _ := s.List(); len(got) != 0 {
		t.Errorf("List = %+v; want empty", got)
	}
}

func TestRemoveByANameSharedByTwoFoldersAsksForTheFolder(t *testing.T) {
	s := service(t)
	a, b := project(t, "photo-app"), project(t, "photo-app")
	s.Remember(Project{Name: "photo-app", Path: a})
	s.Remember(Project{Name: "photo-app", Path: b})

	_, err := s.Remove("photo-app")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !reflect.DeepEqual(amb.Paths, []string{a, b}) {
		t.Fatalf("Remove of a shared name: %v; want an *AmbiguousError naming both folders", err)
	}
	if !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Errorf("the message %q does not show both folders", err)
	}
	if got, _ := s.List(); len(got) != 2 {
		t.Errorf("an ambiguous Remove removed something: %+v", got)
	}

	if _, err := s.Remove(a); err != nil {
		t.Fatalf("Remove by folder: %v", err)
	}
	if got, _ := s.List(); len(got) != 1 || got[0].Path != b {
		t.Errorf("List = %+v; want only the second folder", got)
	}
}

func TestRemoveOfSomethingNotListed(t *testing.T) {
	s := service(t)
	s.Remember(Project{Name: "photo-app", Path: project(t, "photo-app")})
	if _, err := s.Remove("nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Remove of an unknown key: %v; want core.ErrNotFound", err)
	}
}

func TestARenamedProjectUpdatesItsName(t *testing.T) {
	s := service(t)
	dir := project(t, "photo-app")
	s.Remember(Project{Name: "photo-app", Path: dir})

	added, err := s.Remember(Project{Name: "photo-gallery", Path: dir})
	if err != nil || added {
		t.Fatalf("Remember under a new name: added %v, err %v; want not added", added, err)
	}
	if got, _ := s.List(); len(got) != 1 || got[0].Name != "photo-gallery" {
		t.Errorf("List = %+v; want the new name, still one project", got)
	}
}

func TestAListThatIsNotValidIsNeverOverwritten(t *testing.T) {
	cases := map[string]string{
		"not yaml":          "projects: [unterminated\n",
		"an unknown key":    "projects: []\nsurprise: 1\n",
		"a relative path":   "projects:\n  - { name: x, path: relative/folder }\n",
		"a list of strings": "projects:\n  - just-a-string\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := service(t)
			if err := os.MkdirAll(filepath.Dir(s.File), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.File, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := s.List(); err == nil || !strings.Contains(err.Error(), s.File) || !strings.Contains(err.Error(), "not a valid project list") {
				t.Errorf("List: %v; want an error that names the file and says it is not valid", err)
			}
			if _, err := s.Remember(Project{Name: "photo-app", Path: project(t, "photo-app")}); err == nil {
				t.Error("Remember wrote over a list that is not valid")
			}
			if _, err := s.Remove("x"); err == nil || errors.Is(err, core.ErrNotFound) {
				t.Errorf("Remove: %v; want the invalid-list error, not 'not found'", err)
			}
			if got := read(t, s.File); got != body {
				t.Errorf("the list file was changed:\n%s", got)
			}
		})
	}
}

func TestAnEmptyListFileIsAnEmptyList(t *testing.T) {
	s := service(t)
	os.MkdirAll(filepath.Dir(s.File), 0o755)
	os.WriteFile(s.File, nil, 0o644)
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Errorf("List of an empty file = %+v, %v", got, err)
	}
}

func TestDefaultFileIsInTheConfigFolder(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	got, err := DefaultFile()
	if err != nil || got != filepath.Join(cfg, "tester", "projects.yaml") {
		t.Errorf("DefaultFile = %q, %v", got, err)
	}
}

// A hook and the menu may both add a project at the same moment. A lost entry is
// harmless (it is added again the next time that project is used), but the
// file must never be left unreadable and no temporary file may be left behind.
func TestParallelWritesNeverCorruptTheFile(t *testing.T) {
	s := service(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		dir := project(t, "p")
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Remember(Project{Name: "p", Path: dir})
		}()
	}
	wg.Wait()

	got, err := s.List()
	if err != nil || len(got) < 1 {
		t.Fatalf("List after parallel writes = %+v, %v; want a readable list", got, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(s.File), ".projects-*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}
