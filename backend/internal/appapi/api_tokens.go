package appapi

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/apitoken"
)

type apiTokenCreateRequest struct {
	Name string `json:"name"`
}

func handleListAPITokens(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		userID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		records, err := apitoken.List(app, userID)
		if err != nil {
			app.Logger().Error("api token list failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to list API tokens.")
		}
		infos := make([]apitoken.Info, 0, len(records))
		for _, record := range records {
			infos = append(infos, apitoken.ToInfo(record))
		}
		return writeJSON(e, http.StatusOK, map[string]any{"api_tokens": infos})
	}
}

// handlePostAPIToken mints a new token and returns the raw value once. It is
// never shown again -- only its hash is kept.
func handlePostAPIToken(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		var req apiTokenCreateRequest
		if err := e.BindBody(&req); err != nil {
			return writeError(e, http.StatusBadRequest, "Invalid request body.")
		}
		userID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		record, raw, err := apitoken.Create(app, userID, req.Name)
		if err != nil {
			app.Logger().Error("api token create failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to create the API token.")
		}
		return writeJSON(e, http.StatusCreated, map[string]any{
			"api_token": apitoken.ToInfo(record),
			"token":     raw,
		})
	}
}

func handleDeleteAPIToken(app core.App) func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		userID, err := resolveOwnerUserID(app, e)
		if err != nil {
			return writeOwnerError(e, err)
		}
		record, err := apitoken.FindOwned(app, userID, e.Request.PathValue("id"))
		if err != nil {
			if errors.Is(err, apitoken.ErrNotFound) {
				return writeError(e, http.StatusNotFound, "API token not found.")
			}
			app.Logger().Error("api token lookup failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to look up the API token.")
		}
		if err := app.Delete(record); err != nil {
			app.Logger().Error("api token delete failed", "error", err)
			return writeError(e, http.StatusInternalServerError, "Failed to delete the API token.")
		}
		e.Response.WriteHeader(http.StatusNoContent)
		return nil
	}
}
