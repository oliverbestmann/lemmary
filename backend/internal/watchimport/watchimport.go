// Package watchimport imports files dropped into a watched directory.
//
// The directory holds one subdirectory per owner, named after that owner's
// account email; every file placed in such a subdirectory is imported as that
// user's document and then moved into the subdirectory's import-archived/ —
// whether it was imported or skipped as a checksum duplicate, so a settled
// drop directory means "nothing left to do" rather than "nothing worked".
package watchimport

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"

	"lemmary/backend/internal/duplicates"
	"lemmary/backend/internal/inflight"
	"lemmary/backend/internal/models"
)

// ArchiveDirName is the per-owner subdirectory processed files are moved into.
const ArchiveDirName = "import-archived"

const pollInterval = 10 * time.Second

// settleAge is how long a file must have been untouched before it is imported.
// A scanner or an scp still writing its output would otherwise be imported
// half-complete, and the truncated copy would then take the checksum slot the
// full file needs.
const settleAge = 5 * time.Second

// Register starts the watch-directory poller. It is a no-op when WATCH_DIR is
// unset, which is what keeps the feature off for installs that never asked
// for it.
func Register(app core.App) {
	root := strings.TrimSpace(os.Getenv("WATCH_DIR"))
	if root == "" {
		return
	}

	stop := make(chan struct{})
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		go run(app, root, stop)
		app.Logger().Info("watch import registered", "dir", root, "interval", pollInterval.String())
		return e.Next()
	})
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		close(stop)
		return e.Next()
	})
}

func run(app core.App, root string, stop <-chan struct{}) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Counted as in-flight work so an encrypted shutdown waits for a
			// scan that is mid-import instead of sealing the archive under it.
			func() {
				defer inflight.Begin()()
				scan(app, root)
			}()
		}
	}
}

// scan imports one round of every owner directory under root.
func scan(app core.App, root string) {
	ensureOwnerDirs(app, root)

	owners, err := os.ReadDir(root)
	if err != nil {
		app.Logger().Error("watch import: read watch dir", "dir", root, slog.Any("error", err))
		return
	}

	collection, err := app.FindCollectionByNameOrId("documents")
	if err != nil {
		app.Logger().Error("watch import: documents collection", slog.Any("error", err))
		return
	}

	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		scanOwner(app, collection, filepath.Join(root, owner.Name()), owner.Name())
	}
}

// ensureOwnerDirs creates the watch root and a directory for every account, so
// somewhere to drop files exists without anybody having to make it by hand and
// spell an email correctly. It runs every pass, which is also what gives a
// newly registered account its directory. Directories of accounts that no
// longer exist are left alone rather than removed: they may still hold files
// nobody has collected.
func ensureOwnerDirs(app core.App, root string) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		app.Logger().Error("watch import: create watch dir", "dir", root, slog.Any("error", err))
		return
	}
	users, err := app.FindAllRecords("users")
	if err != nil {
		app.Logger().Error("watch import: list users", slog.Any("error", err))
		return
	}
	for _, user := range users {
		email := user.Email()
		if !safeDirName(email) {
			app.Logger().Warn("watch import: account email is not usable as a directory name",
				"user", user.Id)
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, email), 0o755); err != nil {
			app.Logger().Error("watch import: create owner dir", "email", email, slog.Any("error", err))
		}
	}
}

// safeDirName keeps a value out of the path unless it names one directory and
// no other. An email cannot normally contain a separator, but the watch root is
// a host directory and this is the one place a database value reaches it.
func safeDirName(name string) bool {
	if name == "" || name == "." || name == ".." || name == ArchiveDirName {
		return false
	}
	return !strings.ContainsAny(name, `/\`) && !strings.ContainsRune(name, os.PathSeparator)
}

func scanOwner(app core.App, collection *core.Collection, dir, email string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		app.Logger().Error("watch import: read owner dir", "dir", dir, slog.Any("error", err))
		return
	}
	pending := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !importable(entry) {
			continue
		}
		pending = append(pending, entry.Name())
	}
	if len(pending) == 0 {
		return
	}

	// Resolved only once there is something to import, so a directory named
	// after an account that no longer exists is silent until it is used.
	user, err := app.FindAuthRecordByEmail("users", email)
	if err != nil {
		app.Logger().Error("watch import: unknown owner", "dir", dir, "email", email, slog.Any("error", err))
		return
	}

	for _, name := range pending {
		path := filepath.Join(dir, name)
		if err := importFile(app, collection, user.Id, path, name); err != nil {
			var dup *duplicates.ErrDuplicate
			if !errors.As(err, &dup) {
				app.Logger().Error("watch import: import failed", "file", path, slog.Any("error", err))
				continue
			}
			app.Logger().Info("watch import: duplicate skipped", "file", path, "duplicate_of", dup.ExistingID)
		} else {
			app.Logger().Info("watch import: imported", "file", path, "owner", email)
		}
		if err := archive(dir, name); err != nil {
			app.Logger().Error("watch import: archive failed", "file", path, slog.Any("error", err))
		}
	}
}

func importable(entry os.DirEntry) bool {
	if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
		return false
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return time.Since(info.ModTime()) >= settleAge
}

// importFile creates the document. The documents create hook does the checksum
// dedup and queues the processing job, exactly as it does for an upload.
func importFile(app core.App, collection *core.Collection, ownerUserID, path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	fsFile, err := filesystem.NewFileFromBytes(data, name)
	if err != nil {
		return fmt.Errorf("prepare file: %w", err)
	}

	record := core.NewRecord(collection)
	record.Set("user", ownerUserID)
	record.Set("file", fsFile)
	record.Set("processing_status", models.DocStatusPending)

	return duplicates.NormalizeSaveError(app, record, app.Save(record))
}

// archive moves a processed file into the owner directory's archive, giving it
// a suffix rather than overwriting a file of the same name archived earlier.
func archive(dir, name string) error {
	archiveDir := filepath.Join(dir, ArchiveDirName)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	src := filepath.Join(dir, name)
	dst, err := freeName(archiveDir, name)
	if err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// A watch directory bind-mounted from another volume cannot be renamed
	// across, so fall back to a copy that only unlinks the source once the
	// archived copy is safely on disk.
	return copyOver(src, dst)
}

func freeName(archiveDir, name string) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		candidate := filepath.Join(archiveDir, name)
		if i > 0 {
			candidate = filepath.Join(archiveDir, fmt.Sprintf("%s-%d%s", base, i, ext))
		}
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("stat archive target: %w", err)
		}
	}
}

func copyOver(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("copy to archive: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("close archive file: %w", err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove source: %w", err)
	}
	return nil
}
