package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// tokenPrefix marks a string as a lemmary API token rather than a PocketBase
// session JWT, so a client or a log line can tell the two apart at a glance.
const tokenPrefix = "lmk_"

// tokenRandomBytes is the entropy behind each token.
const tokenRandomBytes = 32

// ErrNotFound is returned when no token record matches a lookup, whether
// because the record does not exist or belongs to someone else -- the same
// error for both, so a caller cannot use the distinction to probe other
// accounts' token ids.
var ErrNotFound = errors.New("api token not found")

// Info is the client-facing view of a token record. The hash never leaves the
// server, and the raw token is only ever shown once, at creation.
type Info struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Created  string `json:"created"`
	LastUsed string `json:"last_used"`
}

// ToInfo projects a token record for the API.
func ToInfo(record *core.Record) Info {
	lastUsed := ""
	if value := record.GetDateTime("last_used"); !value.IsZero() {
		lastUsed = value.String()
	}
	return Info{
		ID:       record.Id,
		Name:     record.GetString("name"),
		Created:  record.GetDateTime("created").String(),
		LastUsed: lastUsed,
	}
}

// generate returns a fresh raw token and the hash stored for it.
func generate() (raw, hash string, err error) {
	buf := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Create mints a new token for userID and stores only its hash. The raw token
// is returned once and cannot be recovered afterwards.
func Create(app core.App, userID, name string) (*core.Record, string, error) {
	collection, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return nil, "", err
	}
	raw, hash, err := generate()
	if err != nil {
		return nil, "", err
	}

	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("token_hash", hash)
	record.Set("name", NormalizeName(name))
	if err := app.Save(record); err != nil {
		return nil, "", err
	}
	return record, raw, nil
}

// NormalizeName trims a label and falls back to something recognizable, so the
// management list never shows a blank row.
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "API token"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	return name
}

// List returns the account's tokens, newest first.
func List(app core.App, userID string) ([]*core.Record, error) {
	records := []*core.Record{}
	err := app.RecordQuery(CollectionName).
		AndWhere(dbx.HashExp{"user": userID}).
		OrderBy("created DESC").
		All(&records)
	if err != nil {
		return nil, err
	}
	return records, nil
}

// FindOwned resolves one of the account's own tokens. Scoping the query by
// user is what keeps a session from revoking another account's token by
// guessing a record id.
func FindOwned(app core.App, userID, recordID string) (*core.Record, error) {
	records := []*core.Record{}
	err := app.RecordQuery(CollectionName).
		AndWhere(dbx.HashExp{"id": recordID, "user": userID}).
		Limit(1).
		All(&records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrNotFound
	}
	return records[0], nil
}

// findByHash resolves the token record matching a hash.
func findByHash(app core.App, hash string) (*core.Record, error) {
	records := []*core.Record{}
	err := app.RecordQuery(CollectionName).
		AndWhere(dbx.HashExp{"token_hash": hash}).
		Limit(1).
		All(&records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrNotFound
	}
	return records[0], nil
}

// ExtractToken pulls a bearer/token credential out of an Authorization header,
// mirroring ngxapi's Token/Bearer/bare handling so both app APIs accept
// whichever style a client already sends.
func ExtractToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	lower := strings.ToLower(header)
	switch {
	case strings.HasPrefix(lower, "bearer "):
		return strings.TrimSpace(header[len("bearer "):])
	case strings.HasPrefix(lower, "token "):
		return strings.TrimSpace(header[len("token "):])
	default:
		return header
	}
}

// Authenticate resolves the users record an API token in the request's
// Authorization header authorizes. It returns ErrNotFound both when the
// header carries no lemmary-shaped token and when the token does not match any
// record, so a caller can fall through to session auth either way without the
// distinction leaking anything.
func Authenticate(app core.App, r *http.Request) (*core.Record, error) {
	raw := ExtractToken(r.Header.Get("Authorization"))
	if !strings.HasPrefix(raw, tokenPrefix) {
		return nil, ErrNotFound
	}

	record, err := findByHash(app, hashToken(raw))
	if err != nil {
		return nil, err
	}

	touch(app, record)

	user, err := app.FindRecordById(UsersCollectionName, record.GetString("user"))
	if err != nil {
		return nil, err
	}
	return user, nil
}

// touch stamps last_used on a best-effort basis. A failed write here must not
// turn an otherwise valid token into a failed request.
func touch(app core.App, record *core.Record) {
	record.Set("last_used", types.NowDateTime())
	_ = app.Save(record)
}
