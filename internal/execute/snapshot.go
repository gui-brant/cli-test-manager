package execute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/gui-brant/cli-test-manager/internal/core"
	"github.com/gui-brant/cli-test-manager/internal/match"
)

const (
	// MaxSnapshotFileBytes is the largest file whose text is kept. A bigger one is
	// listed, with its hash, and the note says why it is not kept.
	MaxSnapshotFileBytes = 1 << 20

	// MaxSnapshotRunBytes is the most text one run keeps. Files past it are
	// listed but not kept. (The history stores each distinct text once, so a file
	// that has not changed since the last run adds no text.)
	MaxSnapshotRunBytes = 32 << 20

	// DefaultManifestFile is the manifest's name when the request gives none.
	DefaultManifestFile = "test-manifest.yaml"
)

// What a file's note says when its text is not kept.
const (
	noteBinary   = "binary"
	noteTooLarge = "larger than 1 MiB"
	noteSecret   = "contains a secret value"
	noteRunLimit = "the run's snapshot limit (32 MiB) was reached"
)

// wantedFile is a file the snapshot will record, before it is read.
type wantedFile struct {
	path   string
	role   core.SnapshotRole
	testID string
}

// snapshot records the files the run is made with, before its first test: the
// manifest, the file of each test that will run, and every project file that
// matches the path patterns of the features those tests belong to.
//
// A file is always listed with its hash. Its text is kept unless it is binary,
// larger than MaxSnapshotFileBytes, holds the value of a secret the run knows,
// or would take the run past MaxSnapshotRunBytes; the note says which. A file
// that cannot be read is listed with the reason. It never fails the run: what
// cannot be recorded is said in the returned notes.
func (s *Service) snapshot(ctx context.Context, req Request, p *plan) (files []core.SnapshotFile, notes []string) {
	wanted := make(map[string]wantedFile)
	raw := make(map[string][]byte) // text already in hand: the manifest

	if len(req.ManifestRaw) > 0 {
		name := req.ManifestFile
		if name == "" {
			name = DefaultManifestFile
		}
		wanted[name] = wantedFile{path: name, role: core.RoleManifest}
		raw[name] = req.ManifestRaw
	}

	// Without a way to read the project's files only the manifest can be kept.
	if s.Tree != nil {
		features := make(map[string]bool)
		for _, t := range p.tests {
			wanted[t.File] = wantedFile{path: t.File, role: core.RoleTest, testID: t.ID}
			features[t.Feature] = true
		}

		// The files of each feature that ran, found through its path patterns.
		var patterns []string
		for f := range features {
			patterns = append(patterns, req.Manifest.Features[f].Paths...)
		}
		if len(patterns) > 0 {
			all, ok := s.Tree.Files(ctx, req.Root)
			if !ok {
				notes = append(notes, "the project's files could not be listed (is it under git?), so only the manifest and the tests' files were recorded")
			}
			for _, f := range all {
				if _, have := wanted[f]; !have && match.GlobAny(patterns, f) {
					wanted[f] = wantedFile{path: f, role: core.RoleFeature}
				}
			}
		}
	}

	list := make([]wantedFile, 0, len(wanted))
	for _, w := range wanted {
		list = append(list, w)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].path < list[j].path })
	return s.collect(ctx, req, p, list, raw), notes
}

// collect reads each wanted file and applies the rules about keeping its text.
func (s *Service) collect(ctx context.Context, req Request, p *plan, list []wantedFile, raw map[string][]byte) []core.SnapshotFile {
	files := make([]core.SnapshotFile, 0, len(list))
	kept := 0
	for _, w := range list {
		if ctx.Err() != nil {
			break // the run is being cancelled; nothing it records will be saved
		}
		f := core.SnapshotFile{Path: w.path, Role: w.role, TestID: w.testID}
		data, have := raw[w.path]
		if !have {
			var err error
			if data, err = s.Tree.Read(req.Root, w.path); err != nil {
				f.Note = "could not be read: " + p.redactor.Redact(err.Error())
				files = append(files, f)
				continue
			}
		}
		sum := sha256.Sum256(data)
		f.Hash, f.Size = hex.EncodeToString(sum[:]), int64(len(data))
		switch {
		case bytes.IndexByte(data, 0) >= 0:
			f.Note = noteBinary
		case len(data) > MaxSnapshotFileBytes:
			f.Note = noteTooLarge
		case p.redactor.Redact(string(data)) != string(data):
			f.Note = noteSecret
		case kept+len(data) > MaxSnapshotRunBytes:
			f.Note = noteRunLimit
		default:
			f.Content = data
			kept += len(data)
		}
		files = append(files, f)
	}
	return files
}
