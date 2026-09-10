package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
	"lemmary/backend/internal/apitoken"
)

// api_tokens stores one long-lived, random credential per row so an account
// can authenticate uploads and the rest of the app API without a browser
// session.
//
// A separate collection rather than a column on users, for the same reasons
// 1730000014 gave passkey_credentials a collection of their own: an account
// wants more than one token (one per script or device, revocable on its own),
// and a relation with CascadeDelete means deleting a user takes their tokens
// with it. The schema lives in internal/apitoken.EnsureCollection so this
// migration and a fresh boot cannot drift apart.
//
// The collection gets no API rules, which leaves them nil and keeps it off
// /api/collections entirely -- every legitimate access goes through
// /api/app/api-tokens.
func init() {
	m.Register(func(app core.App) error {
		_, err := apitoken.EnsureCollection(app)
		return err
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(apitoken.CollectionName)
		if err != nil {
			return nil
		}
		return app.Delete(collection)
	})
}
