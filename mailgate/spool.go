package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// spool writes finished documents into Lemmary's watch directory.
//
// The layout is Lemmary's, not ours: one directory per account, named after
// that account's email, holding the files to import. Everything after the
// rename is the server's job.
//
// Both processes write here. The importer does not only read a document, it
// moves it into the owner's import-archived/ afterwards, so it needs write on
// every directory this creates. The images share uid 1000 for that reason, and
// everything is created group-writable as well, so an operator who runs the two
// under different accounts can put them in a common group instead.
type spool struct {
	root string
}

// check fails early on a root that is missing or not writable, so a
// misconfigured deployment is a refusal to start rather than a mail accepted
// with a 250 and then dropped.
func (s *spool) check() error {
	if err := makeSharedDir(s.root); err != nil {
		return fmt.Errorf("spool root %s: %w", s.root, err)
	}
	probe, err := os.CreateTemp(s.root, ".mailgate-probe-")
	if err != nil {
		return fmt.Errorf("spool root %s is not writable: %w", s.root, err)
	}
	probe.Close()
	return os.Remove(probe.Name())
}

// write puts one PDF in an owner's directory and returns where it landed.
//
// The file is written under a dotted name and renamed into place. Lemmary's
// watch import skips dotfiles and waits for a file to stop changing before it
// reads one, so a rename is what makes a partly written PDF impossible to
// import -- and the checksum slot a truncated copy would take is the one thing
// that cannot be undone by trying again.
func (s *spool) write(owner, name string, data []byte) (string, error) {
	dir := filepath.Join(s.root, owner)
	if err := makeSharedDir(dir); err != nil {
		return "", fmt.Errorf("create owner dir: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".mailgate-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) // no-op once the rename has succeeded

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	// Flushed before the rename: a crash between the two would otherwise leave
	// a correctly named file with no contents, which the importer would happily
	// pick up.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", fmt.Errorf("sync temp file: %w", err)
	}
	// CreateTemp makes the file 0600, and the importer is a different process
	// on the other side of a shared volume. A document it cannot open would sit
	// in the directory forever; one it cannot move would be imported again on
	// every pass.
	if err := temp.Chmod(sharedFileMode); err != nil {
		temp.Close()
		return "", fmt.Errorf("set permissions: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	target, err := freeName(dir, name)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tempName, target); err != nil {
		return "", fmt.Errorf("rename into place: %w", err)
	}
	return target, nil
}

const (
	// sharedDirMode and sharedFileMode are what the importer needs to move a
	// document out of the directory once it has read it.
	sharedDirMode  os.FileMode = 0o775
	sharedFileMode os.FileMode = 0o664
)

// makeSharedDir creates a directory the importer can also write.
//
// Chmod after MkdirAll rather than relying on the mode argument, because that
// one is masked by the process umask -- 022 by default, which is exactly the
// group write bit this needs to keep.
func makeSharedDir(path string) error {
	if err := os.MkdirAll(path, sharedDirMode); err != nil {
		return err
	}
	return os.Chmod(path, sharedDirMode)
}

// maxNameAttempts bounds the search for an unused name.
const maxNameAttempts = 1000

// freeName finds a name nothing occupies yet.
//
// Two mails can legitimately share a name -- same subject, same day -- and they
// are different documents. Overwriting would lose one; suffixing keeps both and
// lets Lemmary's own checksum dedup decide whether they are really the same.
func freeName(dir, name string) (string, error) {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate, nil
	}

	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for attempt := 2; attempt <= maxNameAttempts; attempt++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, attempt, extension))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name for %q in %s", name, dir)
}
