package appapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/ai"
	"lemmary/backend/internal/config"
	"lemmary/backend/internal/importjob"
	"lemmary/backend/internal/inflight"
	"lemmary/backend/internal/models"
	"lemmary/backend/internal/strutil"
	"lemmary/backend/internal/worker"
)

type tagRebuildResult struct {
	Documents int      `json:"documents"`
	Tagged    int      `json:"tagged"`
	Failed    int      `json:"failed"`
	Created   int      `json:"created"`
	Errors    []string `json:"errors,omitempty"`
}

var tagRebuildJobs = importjob.NewRegistry[tagRebuildResult](importjob.DefaultRetention)

func tagRebuildDocumentIDs(db dbx.Builder, ownerID string) ([]string, error) {
	var ids []string
	err := db.NewQuery(`SELECT d.id FROM documents d
		WHERE d.user = {:user}
		AND COALESCE(d.ocr_text, '') != ''
		AND d.processing_status NOT IN ({:pending}, {:processing})
		ORDER BY d.created`).
		Bind(tagAssignCandidateParams(ownerID)).
		Column(&ids)
	return ids, err
}

// One document at a time, oldest first: each sees the tags the ones before it
// created, which is what keeps the vocabulary from repeating itself. An early
// document never sees a later one's tags, so a second run is what offers them.
func runTagRebuild(ctx context.Context, app core.App, extractor ai.Extractor, cfg config.Config, ownerID string, report func(done, total int)) (tagRebuildResult, error) {
	var result tagRebuildResult

	before, err := app.CountRecords("tags", dbx.HashExp{"user": ownerID})
	if err != nil {
		return result, err
	}

	ids, err := tagRebuildDocumentIDs(app.DB(), ownerID)
	if err != nil {
		return result, err
	}
	result.Documents = len(ids)
	if report != nil {
		report(0, len(ids))
	}

	for i, id := range ids {
		title, err := rebuildOneDocument(ctx, app, extractor, cfg, id)
		if err != nil {
			result.Failed++
			result.Errors = importjob.AppendError(result.Errors, fmt.Sprintf("%s: %s", title, err))
		} else {
			result.Tagged++
		}
		if report != nil {
			report(i+1, len(ids))
		}
	}

	after, err := app.CountRecords("tags", dbx.HashExp{"user": ownerID})
	if err != nil {
		return result, err
	}
	result.Created = max(int(after-before), 0)
	return result, nil
}

func rebuildOneDocument(ctx context.Context, app core.App, extractor ai.Extractor, cfg config.Config, id string) (string, error) {
	record, err := app.FindRecordById("documents", id)
	if err != nil {
		return id, err
	}
	title := strutil.FirstNonEmpty(record.GetString("title"), "Untitled document")
	switch record.GetString("processing_status") {
	case models.DocStatusPending, models.DocStatusProcessing:
		return title, nil
	}
	if cfg.OpenAITimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.OpenAITimeout)
		defer cancel()
	}
	return title, worker.RebuildDocumentTags(ctx, app, extractor, record)
}

func clearTags(app core.App, ownerID string) (int, error) {
	tags, err := app.FindAllRecords("tags", dbx.HashExp{"user": ownerID})
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, tag := range tags {
		if err := app.Delete(tag); err != nil {
			return deleted, fmt.Errorf("delete tag %s: %w", tag.GetString("name"), err)
		}
		deleted++
	}
	return deleted, nil
}

func handlePostTagClear(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		ownerID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		if tagRebuildJobs.Busy(ownerID) || tagAssignJobs.Busy(ownerID) {
			return writeError(e, http.StatusConflict, "A tag job is already running.")
		}
		deleted, err := clearTags(app, ownerID)
		if err != nil {
			app.Logger().Error("clear tags failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to delete the tags.")
		}
		return writeJSON(e, http.StatusOK, map[string]any{"deleted": deleted})
	}
}

func handlePostTagRebuild(app core.App, rt *config.Runtime) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		ownerID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		snap := rt.Snapshot()
		if snap.AI == nil {
			return writeError(e, http.StatusBadRequest, "Rebuilding tags needs a model; configure one in Settings.")
		}
		if tagAssignJobs.Busy(ownerID) {
			return writeError(e, http.StatusConflict, "A tag assignment is already running.")
		}

		ctx := context.WithoutCancel(e.Request.Context())
		jobID, err := tagRebuildJobs.Start(ownerID, func(report func(done, total int)) (tagRebuildResult, error) {
			defer inflight.Begin()()
			return runTagRebuild(ctx, app, snap.AI, snap.Cfg, ownerID, report)
		})
		switch {
		case errors.Is(err, importjob.ErrBusy):
			return writeError(e, http.StatusConflict, "A tag rebuild is already running.")
		case err != nil:
			return writeErrorf(e, http.StatusBadRequest, "Could not start: %v", err)
		}
		return writeJSON(e, http.StatusAccepted, map[string]any{
			"job_id": jobID,
			"status": importjob.StatusRunning,
		})
	}
}

func handleGetTagRebuildStatus(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		jobID := strings.TrimSpace(e.Request.URL.Query().Get("job_id"))
		if jobID == "" {
			return writeError(e, http.StatusBadRequest, "job_id is required.")
		}
		ownerID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		job, ok := tagRebuildJobs.Get(jobID)
		if !ok || job.OwnerUserID != ownerID {
			return writeError(e, http.StatusNotFound, "Tag rebuild job not found.")
		}
		return writeJSON(e, http.StatusOK, jobPayload(job.ID, job.Status, job.Progress, job.Error, job.Result))
	}
}
