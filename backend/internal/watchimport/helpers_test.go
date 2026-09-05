package watchimport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// backdate makes a file look older than settleAge, which is what the scan
// requires before it will touch it.
func backdate(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func entryFor(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("no entry named %q in %s", name, dir)
	return nil
}

// A file still being written must not be imported: a scanner's half-flushed
// output would take the checksum slot the complete file needs.
func TestImportableWaitsForTheFileToSettle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "scan.pdf")
	writeFile(t, path, "fresh")

	if importable(entryFor(t, dir, "scan.pdf")) {
		t.Fatal("a just-written file must not be importable yet")
	}

	backdate(t, path)
	if !importable(entryFor(t, dir, "scan.pdf")) {
		t.Fatal("a settled file must be importable")
	}
}

func TestImportableSkipsDirectoriesAndDotfiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ArchiveDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	hidden := filepath.Join(dir, ".partial.pdf")
	writeFile(t, hidden, "x")
	backdate(t, hidden)

	if importable(entryFor(t, dir, ArchiveDirName)) {
		t.Fatalf("%s must never be re-imported", ArchiveDirName)
	}
	if importable(entryFor(t, dir, ".partial.pdf")) {
		t.Fatal("dotfiles must be skipped")
	}
}

func TestArchiveMovesTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "scan.pdf"), "body")

	if err := archive(dir, "scan.pdf"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "scan.pdf")); !os.IsNotExist(err) {
		t.Fatal("the source file must be gone after archiving")
	}
	got, err := os.ReadFile(filepath.Join(dir, ArchiveDirName, "scan.pdf"))
	if err != nil {
		t.Fatalf("read archived file: %v", err)
	}
	if string(got) != "body" {
		t.Fatalf("archived contents = %q, want %q", got, "body")
	}
}

// Two drops of the same name must both survive: the second is the evidence that
// something was sent twice, and overwriting it would destroy that.
func TestArchiveSuffixesRatherThanOverwriting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for i, content := range []string{"first", "second", "third"} {
		writeFile(t, filepath.Join(dir, "scan.pdf"), content)
		if err := archive(dir, "scan.pdf"); err != nil {
			t.Fatalf("archive %d: %v", i, err)
		}
	}

	for name, want := range map[string]string{
		"scan.pdf":   "first",
		"scan-1.pdf": "second",
		"scan-2.pdf": "third",
	} {
		got, err := os.ReadFile(filepath.Join(dir, ArchiveDirName, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

// The rename fallback for a watch directory mounted from another device.
func TestCopyOverMovesContentsAndUnlinksTheSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "scan.pdf")
	dst := filepath.Join(dir, "copied.pdf")
	writeFile(t, src, "body")

	if err := copyOver(src, dst); err != nil {
		t.Fatalf("copyOver: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatal("the source file must be gone")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "body" {
		t.Fatalf("destination = %q, want %q", got, "body")
	}
}

// A destination that already exists must not be clobbered by the fallback path.
func TestCopyOverRefusesAnExistingDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "scan.pdf")
	dst := filepath.Join(dir, "taken.pdf")
	writeFile(t, src, "body")
	writeFile(t, dst, "keep me")

	if err := copyOver(src, dst); err == nil {
		t.Fatal("expected an error for an existing destination")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "keep me" {
		t.Fatalf("destination was overwritten: %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the source must survive a failed copy: %v", err)
	}
}
