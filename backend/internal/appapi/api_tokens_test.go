package appapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/apitoken"
	// Registers the migrations that create users and api_tokens.
	_ "lemmary/backend/migrations"
)

func bootAPITokenTestApp(t *testing.T) *pocketbase.PocketBase {
	t.Helper()
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:  t.TempDir(),
		HideStartBanner: true,
	})
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.ResetBootstrapState() })
	if err := app.RunAppMigrations(); err != nil {
		t.Fatalf("run app migrations: %v", err)
	}
	return app
}

func makeAPITokenTestUser(t *testing.T, app core.App, email string) *core.Record {
	t.Helper()
	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	user := core.NewRecord(users)
	user.Set("email", email)
	user.SetPassword("test-password-123")
	if err := app.Save(user); err != nil {
		t.Fatalf("save user: %v", err)
	}
	return user
}

// bindAuth is what protects the upload endpoints (import/amazon/upload,
// import/archive/upload, split/upload); this exercises it directly rather than
// through a full route registration.
func TestBindAuthAcceptsAPIToken(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "uploader@example.test")
	_, raw, err := apitoken.Create(app, user.Id, "upload script")
	if err != nil {
		t.Fatalf("apitoken.Create() error: %v", err)
	}

	called := false
	handler := bindAuth(func(e *core.RequestEvent) error {
		called = true
		if e.Auth == nil || e.Auth.Id != user.Id {
			t.Fatalf("handler saw auth %+v, want user %s", e.Auth, user.Id)
		}
		return nil
	})

	e := &core.RequestEvent{}
	e.App = app
	e.Request = httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
	e.Request.Header.Set("Authorization", "Bearer "+raw)
	e.Response = httptest.NewRecorder()

	if err := handler(e); err != nil {
		t.Fatalf("bindAuth() error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called for a valid API token")
	}
}

func TestBindAuthRejectsInvalidAPIToken(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)

	handler := bindAuth(func(e *core.RequestEvent) error {
		t.Fatal("handler must not run without valid auth")
		return nil
	})

	e := &core.RequestEvent{}
	e.App = app
	e.Request = httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
	e.Request.Header.Set("Authorization", "Bearer lmk_not-a-real-token")
	e.Response = httptest.NewRecorder()

	if err := handler(e); err != nil {
		t.Fatalf("bindAuth() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPostAPITokenReturnsRawTokenOnce(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "minter@example.test")

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = user
	body := `{"name":"CI runner"}`
	e.Request = httptest.NewRequest(http.MethodPost, "/api/app/api-tokens", strings.NewReader(body))
	e.Request.Header.Set("Content-Type", "application/json")
	e.Response = httptest.NewRecorder()

	if err := handlePostAPIToken(app)(e); err != nil {
		t.Fatalf("handlePostAPIToken() error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token    string `json:"token"`
		APIToken struct {
			Name string `json:"name"`
		} `json:"api_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("response carried no raw token")
	}
	if resp.APIToken.Name != "CI runner" {
		t.Fatalf("name = %q, want %q", resp.APIToken.Name, "CI runner")
	}

	// The minted token has to actually authenticate, not just look plausible.
	req := httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
	req.Header.Set("Authorization", resp.Token)
	authedUser, err := apitoken.Authenticate(app, req)
	if err != nil {
		t.Fatalf("Authenticate() with minted token failed: %v", err)
	}
	if authedUser.Id != user.Id {
		t.Fatalf("minted token authenticated as %q, want %q", authedUser.Id, user.Id)
	}
}

func TestDeleteAPITokenRevokesIt(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "revoker@example.test")
	record, raw, err := apitoken.Create(app, user.Id, "throwaway")
	if err != nil {
		t.Fatalf("apitoken.Create() error: %v", err)
	}

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = user
	e.Request = httptest.NewRequest(http.MethodDelete, "/api/app/api-tokens/"+record.Id, nil)
	e.Request.SetPathValue("id", record.Id)
	e.Response = httptest.NewRecorder()

	if err := handleDeleteAPIToken(app)(e); err != nil {
		t.Fatalf("handleDeleteAPIToken() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
	req.Header.Set("Authorization", raw)
	if _, err := apitoken.Authenticate(app, req); err == nil {
		t.Fatal("revoked token still authenticates")
	}
}

func TestDeleteAPITokenRejectsOtherAccountsToken(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	owner := makeAPITokenTestUser(t, app, "owner6@example.test")
	other := makeAPITokenTestUser(t, app, "other6@example.test")
	record, _, err := apitoken.Create(app, owner.Id, "ci")
	if err != nil {
		t.Fatalf("apitoken.Create() error: %v", err)
	}

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = other
	e.Request = httptest.NewRequest(http.MethodDelete, "/api/app/api-tokens/"+record.Id, nil)
	e.Request.SetPathValue("id", record.Id)
	e.Response = httptest.NewRecorder()

	if err := handleDeleteAPIToken(app)(e); err != nil {
		t.Fatalf("handleDeleteAPIToken() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when deleting another account's token", rec.Code)
	}
}
