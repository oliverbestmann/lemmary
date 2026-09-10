package appapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"lemmary/backend/internal/models"
)

type uploadResponse struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
}

// handlePostUpload is the scripted-upload counterpart to the split/archive
// flows the SPA drives: those stage bytes for a review step before any
// document exists, which is right for a UI walking through pages or an
// archive's contents, but wrong for a script whose one PDF *is* the document.
// This creates the documents record directly, synchronously, from a single
// multipart file -- the same shape ngxapi's post_document uses for
// paperless-ngx clients, minus the paperless-specific metadata fields and
// response shape a curl script has no reason to speak.
//
// OCR and the rest of the pipeline still run asynchronously afterward: the
// generic OnRecordCreate("documents") hooks in internal/worker queue the
// processing job, so the response here reports the document as pending, not
// finished.
func handlePostUpload(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		userID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}

		files, err := e.FindUploadedFiles("file")
		if err != nil || len(files) == 0 {
			return writeError(e, http.StatusBadRequest, "Missing PDF file in the \"file\" field.")
		}
		file := files[0]
		if !strings.EqualFold(fileExt(file.Name), ".pdf") &&
			!strings.EqualFold(fileExt(file.OriginalName), ".pdf") {
			return writeError(e, http.StatusBadRequest, "Only PDF uploads are accepted.")
		}

		collection, err := app.FindCollectionByNameOrId("documents")
		if err != nil {
			app.Logger().Error("upload: documents collection lookup failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to save the document.")
		}

		record := core.NewRecord(collection)
		record.Set("user", userID)
		record.Set("file", file)
		record.Set("processing_status", models.DocStatusPending)

		if err := app.Save(record); err != nil {
			return writeUploadSaveError(app, e, err)
		}

		return writeJSON(e, http.StatusCreated, uploadResponse{
			ID:               record.Id,
			ProcessingStatus: record.GetString("processing_status"),
		})
	}
}

// fileExt mirrors filepath.Ext without importing it just for a dot-suffix
// check -- the file field's own Name/OriginalName are never a path, only a
// base name, so the distinction filepath.Ext exists for does not apply.
func fileExt(name string) string {
	if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
		return name[idx:]
	}
	return ""
}

// writeUploadSaveError maps a documents record validation failure (bad
// mimetype, over the size limit, over a plan limit) to 400 rather than the
// generic 500, matching ngxapi's saveError -- a script sending the wrong file
// deserves a message it can print, not "internal server error".
func writeUploadSaveError(app core.App, e *core.RequestEvent, err error) error {
	var vErrs validation.Errors
	if errors.As(err, &vErrs) {
		return writeError(e, http.StatusBadRequest, vErrs.Error())
	}
	var apiErr *router.ApiError
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
		return writeError(e, http.StatusBadRequest, apiErr.Message)
	}
	app.Logger().Error("upload: save document failed", "error", err)
	return writeError(e, http.StatusInternalServerError, "Failed to save the document.")
}
