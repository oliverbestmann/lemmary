package watchimport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/duplicates"
	"lemmary/backend/internal/models"
	_ "lemmary/backend/migrations"
)

const ownerEmail = "owner@example.com"

func bootApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAppMigrations(); err != nil {
		t.Fatalf("run app migrations: %v", err)
	}
	// The checksum half of worker/processor.go's documents create hook. The
	// scan deliberately owns no dedup logic of its own -- it relies on that
	// hook exactly as an upload does -- so the behaviour under test here (a
	// rejected file still gets archived) needs the rejection to be real.
	app.OnRecordCreate("documents").BindFunc(func(e *core.RecordEvent) error {
		if err := duplicates.AssignChecksumFromUpload(e.App, e.Record); err != nil {
			return err
		}
		return e.Next()
	})
	return app
}

func makeUser(t *testing.T, app core.App, email string) string {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("email", email)
	record.SetPassword("test-password-123")
	if err := app.Save(record); err != nil {
		t.Fatalf("save user: %v", err)
	}
	return record.Id
}

// drop writes a settled file into the owner directory, ready to be scanned.
func drop(t *testing.T, root, email, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, email)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir owner dir: %v", err)
	}
	path := filepath.Join(dir, name)
	writeFile(t, path, content)
	backdate(t, path)
	return path
}

func documentsOf(t *testing.T, app core.App, userID string) []*core.Record {
	t.Helper()
	records, err := app.FindRecordsByFilter("documents", "user = {:user}", "created", 0, 0,
		map[string]any{"user": userID})
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	return records
}

func archivedNames(t *testing.T, root, email string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, email, ArchiveDirName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read archive dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestScanImportsAndArchives(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()
	drop(t, root, ownerEmail, "scan.pdf", "hello")

	scan(app, root)

	documents := documentsOf(t, app, userID)
	if len(documents) != 1 {
		t.Fatalf("got %d documents, want 1", len(documents))
	}
	if got := documents[0].GetString("processing_status"); got != models.DocStatusPending {
		t.Fatalf("processing_status = %q, want %q", got, models.DocStatusPending)
	}
	// PocketBase suffixes the stored name, so match on the stem.
	if got := documents[0].GetString("file"); !strings.HasPrefix(got, "scan") {
		t.Fatalf("stored file = %q, want it to keep the dropped name", got)
	}
	if _, err := os.Stat(filepath.Join(root, ownerEmail, "scan.pdf")); !os.IsNotExist(err) {
		t.Fatal("an imported file must not be left in the drop directory")
	}
	if got := archivedNames(t, root, ownerEmail); len(got) != 1 || got[0] != "scan.pdf" {
		t.Fatalf("archived = %v, want [scan.pdf]", got)
	}
}

// The second copy of a file is rejected by the checksum hook. It must still be
// archived: leaving it in place would make the scan retry it forever.
func TestScanArchivesDuplicatesWithoutImportingThem(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()

	drop(t, root, ownerEmail, "scan.pdf", "hello")
	scan(app, root)
	drop(t, root, ownerEmail, "again.pdf", "hello")
	scan(app, root)

	if documents := documentsOf(t, app, userID); len(documents) != 1 {
		t.Fatalf("got %d documents, want 1 -- the duplicate must not be imported", len(documents))
	}
	if got := archivedNames(t, root, ownerEmail); len(got) != 2 {
		t.Fatalf("archived = %v, want both files archived", got)
	}
}

// Two accounts hold their own libraries, and the same file in each is not a
// duplicate: dedup is keyed per user.
func TestScanImportsEachOwnerDirectoryAsThatOwner(t *testing.T) {
	app := bootApp(t)
	aliceID := makeUser(t, app, "alice@example.com")
	bobID := makeUser(t, app, "bob@example.com")
	root := t.TempDir()
	drop(t, root, "alice@example.com", "scan.pdf", "same bytes")
	drop(t, root, "bob@example.com", "scan.pdf", "same bytes")

	scan(app, root)

	if got := len(documentsOf(t, app, aliceID)); got != 1 {
		t.Fatalf("alice has %d documents, want 1", got)
	}
	if got := len(documentsOf(t, app, bobID)); got != 1 {
		t.Fatalf("bob has %d documents, want 1", got)
	}
}

// A directory named after no account is a misconfiguration, not a licence to
// import somebody's documents into the wrong library or to throw them away.
func TestScanLeavesFilesOfAnUnknownOwnerAlone(t *testing.T) {
	app := bootApp(t)
	makeUser(t, app, ownerEmail)
	root := t.TempDir()
	path := drop(t, root, "nobody@example.com", "scan.pdf", "hello")

	scan(app, root)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file must be left where it is: %v", err)
	}
	if got := archivedNames(t, root, "nobody@example.com"); len(got) != 0 {
		t.Fatalf("archived = %v, want nothing archived for an unknown owner", got)
	}
}

// Anything that fails for a reason other than being a duplicate is retried on
// the next pass rather than archived, so a transient fault cannot quietly
// swallow a document.
func TestScanLeavesAFailedImportInPlace(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()
	path := drop(t, root, ownerEmail, "scan.pdf", "hello")

	refuse := true
	app.OnRecordCreate("documents").BindFunc(func(e *core.RecordEvent) error {
		if refuse {
			return errors.New("injected failure")
		}
		return e.Next()
	})

	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 0 {
		t.Fatalf("got %d documents, want 0", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a failed file must stay put for the next pass: %v", err)
	}

	// Next pass, once the fault clears.
	refuse = false
	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 1 {
		t.Fatalf("got %d documents after the retry, want 1", got)
	}
	if got := archivedNames(t, root, ownerEmail); len(got) != 1 {
		t.Fatalf("archived = %v, want the file archived once it succeeded", got)
	}
}

// A file still being written is not imported until it settles.
func TestScanSkipsUnsettledFiles(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()
	dir := filepath.Join(root, ownerEmail)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "scan.pdf")
	writeFile(t, path, "still writing")

	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 0 {
		t.Fatalf("got %d documents, want 0 -- the file had not settled", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the file must still be waiting: %v", err)
	}

	backdate(t, path)
	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 1 {
		t.Fatalf("got %d documents after the file settled, want 1", got)
	}
}

// A stray file at the top level has no owner, and the archive directory is not
// an owner either. Neither may be imported.
func TestScanIgnoresNonOwnerEntriesAtTheRoot(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()
	stray := filepath.Join(root, "loose.pdf")
	writeFile(t, stray, "hello")
	backdate(t, stray)
	drop(t, root, ownerEmail, "scan.pdf", "hello")

	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 1 {
		t.Fatalf("got %d documents, want only the owned one", got)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("a stray root-level file must be left alone: %v", err)
	}
}

// A missing watch directory is an operator error to be logged, not a crash on
// every tick.
func TestScanToleratesAMissingRoot(t *testing.T) {
	app := bootApp(t)
	scan(app, filepath.Join(t.TempDir(), "does-not-exist"))
}

// Nobody should have to create a directory by hand, or spell an email exactly
// right, for a drop to be picked up.
func TestScanCreatesADirectoryForEveryAccount(t *testing.T) {
	app := bootApp(t)
	makeUser(t, app, "alice@example.com")
	makeUser(t, app, "bob@example.com")
	root := filepath.Join(t.TempDir(), "watch")

	scan(app, root)

	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		info, err := os.Stat(filepath.Join(root, email))
		if err != nil {
			t.Fatalf("no directory for %s: %v", email, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", email)
		}
	}
}

// An account that registers after the watcher started gets its directory on the
// next pass.
func TestScanCreatesADirectoryForAnAccountAddedLater(t *testing.T) {
	app := bootApp(t)
	root := t.TempDir()
	scan(app, root)

	makeUser(t, app, "late@example.com")
	scan(app, root)

	if _, err := os.Stat(filepath.Join(root, "late@example.com")); err != nil {
		t.Fatalf("no directory for the new account: %v", err)
	}
}

// Files waiting in a directory must survive the pass that ensures it exists.
func TestScanKeepsAnExistingOwnerDirectoryIntact(t *testing.T) {
	app := bootApp(t)
	userID := makeUser(t, app, ownerEmail)
	root := t.TempDir()
	drop(t, root, ownerEmail, "scan.pdf", "hello")

	scan(app, root)

	if got := len(documentsOf(t, app, userID)); got != 1 {
		t.Fatalf("got %d documents, want 1", got)
	}
}

func TestSafeDirNameRejectsPathsAndTheArchiveDir(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", ".", "..", ArchiveDirName, "a/b", "../escape", `a\b`} {
		if safeDirName(name) {
			t.Fatalf("safeDirName(%q) = true, want false", name)
		}
	}
	if !safeDirName("owner@example.com") {
		t.Fatal("an ordinary email must be usable as a directory name")
	}
}
