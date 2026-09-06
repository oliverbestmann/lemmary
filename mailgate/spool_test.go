package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpoolWritesIntoTheOwnerDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := &spool{root: root}
	if err := s.check(); err != nil {
		t.Fatalf("check: %v", err)
	}

	path, err := s.write("ada@example.com", "2026-09-06 Invoice.pdf", []byte("%PDF-1.4\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(root, "ada@example.com", "2026-09-06 Invoice.pdf")
	if path != want {
		t.Errorf("write() = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "%PDF-1.4\n" {
		t.Errorf("contents = %q, want the PDF bytes", data)
	}
	// The importer reads the document and then moves it into the owner's
	// import-archived/, so it needs write on both the file and the directory it
	// sits in -- through the shared uid, or through a common group.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != sharedFileMode {
		t.Errorf("file permissions = %o, want %o", perm, sharedFileMode)
	}
	dir, err := os.Stat(filepath.Join(root, "ada@example.com"))
	if err != nil {
		t.Fatalf("stat owner dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != sharedDirMode {
		t.Errorf("directory permissions = %o, want %o", perm, sharedDirMode)
	}
}

// Two mails can legitimately share a name -- same subject, same day -- and they
// are different documents. Overwriting would lose one.
func TestSpoolDoesNotOverwrite(t *testing.T) {
	t.Parallel()

	s := &spool{root: t.TempDir()}
	first, err := s.write("ada@example.com", "Invoice.pdf", []byte("one"))
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	second, err := s.write("ada@example.com", "Invoice.pdf", []byte("two"))
	if err != nil {
		t.Fatalf("write second: %v", err)
	}
	if first == second {
		t.Fatal("the second write must land on a different name")
	}
	if !strings.HasSuffix(second, "Invoice (2).pdf") {
		t.Errorf("second = %q, want a suffixed name", second)
	}
	if data, _ := os.ReadFile(first); string(data) != "one" {
		t.Errorf("the first file was overwritten: %q", data)
	}
}

// The importer skips dotfiles and waits for a file to settle, so nothing may be
// left behind under a name it would pick up.
func TestSpoolLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := &spool{root: root}
	if _, err := s.write("ada@example.com", "Invoice.pdf", []byte("one")); err != nil {
		t.Fatalf("write: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "ada@example.com"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "Invoice.pdf" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only the finished PDF", names)
	}
}

// A misconfigured spool root has to be a refusal to start, not a mail accepted
// with a 250 and then dropped.
func TestSpoolCheckRejectsAnUnwritableRoot(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	root := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := (&spool{root: filepath.Join(root, "spool")}).check(); err == nil {
		t.Error("check accepted a root it cannot create")
	}
}
