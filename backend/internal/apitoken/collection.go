// Package apitoken implements long-lived, random API tokens that authenticate
// requests to the app API on behalf of a users-collection account, without a
// browser session.
//
// A token is 32 random bytes, handed to the caller once at creation. Only its
// SHA-256 hash is ever stored, mirroring how passwords are kept: possession of
// the hash alone cannot be turned back into the token.
package apitoken

import (
	"fmt"

	"github.com/pocketbase/pocketbase/core"
)

// CollectionName stores one API token per record.
const CollectionName = "api_tokens"

// UsersCollectionName is the auth collection tokens authenticate.
const UsersCollectionName = "users"

// EnsureCollection creates the tokens collection if it is missing and returns
// it either way, so the migration and a fresh boot share one definition.
//
// No API rules are set, which leaves them nil: PocketBase then serves the
// collection to superusers only, and /api/app/api-tokens is the sole access
// path. A token record is not user-editable data -- letting a session PATCH
// its own row through /api/collections would let it plant an arbitrary
// token_hash of its choosing.
func EnsureCollection(app core.App) (*core.Collection, error) {
	if collection, err := app.FindCollectionByNameOrId(CollectionName); err == nil {
		return collection, nil
	}

	users, err := app.FindCollectionByNameOrId(UsersCollectionName)
	if err != nil {
		return nil, fmt.Errorf("find %s collection: %w", UsersCollectionName, err)
	}

	collection := core.NewBaseCollection(CollectionName)
	collection.Fields.Add(
		&core.RelationField{
			Name:          "user",
			Required:      true,
			MaxSelect:     1,
			CollectionId:  users.Id,
			CascadeDelete: true,
		},
		// SHA-256 hex digest of the raw token. Hidden so the digest can never ride
		// along in a record serialization -- it is a bearer credential in its own
		// right, even hashed, since an exact match against it is exactly what
		// authenticates a request.
		&core.TextField{Name: "token_hash", Required: true, Max: 64, Hidden: true},
		&core.TextField{Name: "name", Max: 100},
		&core.DateField{Name: "last_used"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)
	// Unique: two tokens hashing to the same value would otherwise authenticate
	// as whichever record the lookup happens to return first.
	collection.AddIndex("idx_api_tokens_token_hash", true, "token_hash", "")
	collection.AddIndex("idx_api_tokens_user", false, "user", "")

	if err := app.Save(collection); err != nil {
		return nil, fmt.Errorf("create %s collection: %w", CollectionName, err)
	}
	return collection, nil
}
