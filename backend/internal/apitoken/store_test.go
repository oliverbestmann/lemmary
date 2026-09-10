package apitoken_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/apitoken"
	// Registers the migration that creates the api_tokens collection, and the
	// one that creates users, which it relates to.
	_ "lemmary/backend/migrations"
)

func bootTestApp(t *testing.T) *pocketbase.PocketBase {
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

func makeUser(t *testing.T, app core.App, email string) string {
	t.Helper()
	users, err := app.FindCollectionByNameOrId(apitoken.UsersCollectionName)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	user := core.NewRecord(users)
	user.Set("email", email)
	user.SetPassword("test-password-123")
	if err := app.Save(user); err != nil {
		t.Fatalf("save user: %v", err)
	}
	return user.Id
}

func TestCreateReturnsRawTokenOnceAndStoresOnlyItsHash(t *testing.T) {
	t.Parallel()
	app := bootTestApp(t)
	userID := makeUser(t, app, "owner@example.test")

	record, raw, err := apitoken.Create(app, userID, "my laptop")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if !strings.HasPrefix(raw, "lmk_") {
		t.Fatalf("raw token = %q, want lmk_ prefix", raw)
	}

	// The collection field is hidden, but PocketBase still lets Go code read it
	// directly off the record -- the point of the test is that it isn't the raw
	// value, not that it's inaccessible.
	stored := record.GetString("token_hash")
	if stored == "" || stored == raw {
		t.Fatalf("token_hash = %q, must be a hash, not the raw token", stored)
	}
}

func TestAuthenticateResolvesTheOwningUser(t *testing.T) {
	t.Parallel()
	app := bootTestApp(t)
	userID := makeUser(t, app, "owner2@example.test")

	_, raw, err := apitoken.Create(app, userID, "ci")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	for _, header := range []string{
		raw,
		"Bearer " + raw,
		"Token " + raw,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
		req.Header.Set("Authorization", header)

		user, err := apitoken.Authenticate(app, req)
		if err != nil {
			t.Fatalf("Authenticate(%q) error: %v", header, err)
		}
		if user.Id != userID {
			t.Fatalf("Authenticate(%q) resolved user %q, want %q", header, user.Id, userID)
		}
	}
}

func TestAuthenticateRejectsWrongOrMissingToken(t *testing.T) {
	t.Parallel()
	app := bootTestApp(t)
	userID := makeUser(t, app, "owner3@example.test")
	if _, _, err := apitoken.Create(app, userID, "ci"); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	cases := []string{
		"",
		"lmk_not-a-real-token",
		"Bearer lmk_not-a-real-token",
		"some-other-scheme-entirely",
	}
	for _, header := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if _, err := apitoken.Authenticate(app, req); err == nil {
			t.Fatalf("Authenticate(%q) succeeded, want error", header)
		}
	}
}

func TestAuthenticateTouchesLastUsed(t *testing.T) {
	t.Parallel()
	app := bootTestApp(t)
	userID := makeUser(t, app, "owner4@example.test")
	record, raw, err := apitoken.Create(app, userID, "ci")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if got := apitoken.ToInfo(record).LastUsed; got != "" {
		t.Fatalf("freshly created token LastUsed = %q, want empty", got)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/app/split/upload", nil)
	req.Header.Set("Authorization", raw)
	if _, err := apitoken.Authenticate(app, req); err != nil {
		t.Fatalf("Authenticate() error: %v", err)
	}

	reloaded, err := app.FindRecordById(apitoken.CollectionName, record.Id)
	if err != nil {
		t.Fatalf("reload token record: %v", err)
	}
	if got := apitoken.ToInfo(reloaded).LastUsed; got == "" {
		t.Fatal("LastUsed still empty after Authenticate()")
	}
}

func TestFindOwnedScopesToTheOwningUser(t *testing.T) {
	t.Parallel()
	app := bootTestApp(t)
	owner := makeUser(t, app, "owner5@example.test")
	other := makeUser(t, app, "other5@example.test")

	record, _, err := apitoken.Create(app, owner, "ci")
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if _, err := apitoken.FindOwned(app, owner, record.Id); err != nil {
		t.Fatalf("FindOwned(owner) error: %v", err)
	}
	if _, err := apitoken.FindOwned(app, other, record.Id); err == nil {
		t.Fatal("FindOwned(other) succeeded, want error: a token must not be reachable by a different account")
	}
}

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "trims", in: "  CI  ", want: "CI"},
		{name: "empty falls back", in: "", want: "API token"},
		{name: "whitespace falls back", in: "   ", want: "API token"},
		{name: "kept as-is", in: "Home server", want: "Home server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := apitoken.NormalizeName(tc.in); got != tc.want {
				t.Fatalf("NormalizeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "bare", header: "lmk_abc", want: "lmk_abc"},
		{name: "bearer", header: "Bearer lmk_abc", want: "lmk_abc"},
		{name: "token", header: "Token lmk_abc", want: "lmk_abc"},
		{name: "case insensitive scheme", header: "bearer lmk_abc", want: "lmk_abc"},
		{name: "empty", header: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := apitoken.ExtractToken(tc.header); got != tc.want {
				t.Fatalf("ExtractToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
