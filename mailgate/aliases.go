package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// aliasTable maps recipient local parts to the account that owns them.
//
// It comes from the environment rather than a lookup against Lemmary's
// database, which is the whole point of running separately: this process needs
// no credentials for the archive it feeds. It is read once at startup and never
// changes, so adding an account means restarting the container -- the same as
// every other setting here.
type aliasTable struct {
	entries map[string]string
}

// parseAliases reads the MAILGATE_ALIASES JSON object: alias to owner email.
//
//	{"in-9f2c7a41b0e83d56a1c4f70b2e9d8351": "alice@example.com"}
//
// Every problem is an error rather than a skipped entry. A typo that silently
// dropped one mapping would look like working software until somebody noticed
// months of mail had been refused.
func parseAliases(raw string) (*aliasTable, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("MAILGATE_ALIASES is empty")
	}

	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("MAILGATE_ALIASES is not a JSON object of alias to owner email: %w", err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("MAILGATE_ALIASES contains no aliases")
	}

	entries := make(map[string]string, len(decoded))
	for alias, owner := range decoded {
		// Lowercased on the way in, because SMTP local parts arrive in
		// whatever case the sender typed.
		alias = strings.ToLower(strings.TrimSpace(alias))
		owner = strings.TrimSpace(owner)
		if alias == "" {
			return nil, fmt.Errorf("MAILGATE_ALIASES has an empty alias")
		}
		// The owner names a directory under the spool root, so it has to be one
		// path element and no other. This is the one place a configured value
		// reaches the filesystem.
		if !safeOwner(owner) {
			return nil, fmt.Errorf("MAILGATE_ALIASES: %q is not usable as an owner directory name (want an email address)", owner)
		}
		if existing, clash := entries[alias]; clash && existing != owner {
			return nil, fmt.Errorf("MAILGATE_ALIASES: alias %q is mapped to both %q and %q", alias, existing, owner)
		}
		entries[alias] = owner
	}
	return &aliasTable{entries: entries}, nil
}

// lookup resolves a local part to an owner.
func (t *aliasTable) lookup(local string) (string, bool) {
	owner, ok := t.entries[local]
	return owner, ok
}

// count reports how many aliases are configured, for the startup log and -check.
func (t *aliasTable) count() int { return len(t.entries) }

// owners lists the accounts mail can arrive for, sorted, so -check prints
// something an operator can compare against what they meant to configure.
func (t *aliasTable) owners() []string {
	seen := map[string]bool{}
	list := make([]string, 0, len(t.entries))
	for _, owner := range t.entries {
		if seen[owner] {
			continue
		}
		seen[owner] = true
		list = append(list, owner)
	}
	sort.Strings(list)
	return list
}

// safeOwner keeps a mapping from escaping the spool root.
func safeOwner(owner string) bool {
	if owner == "" || owner == "." || owner == ".." || strings.HasPrefix(owner, ".") {
		return false
	}
	if !strings.Contains(owner, "@") {
		return false
	}
	return !strings.ContainsAny(owner, `/\`) && !strings.ContainsRune(owner, os.PathSeparator)
}

// aliasPrefix marks an address as one of ours at a glance, in a log line or in
// a mail client's recipient list.
const aliasPrefix = "in-"

// aliasBytes is how much randomness a generated alias carries.
//
// The alias is the credential. The listener is meant to sit on the public
// internet, where anything reachable on port 25 is dictionary-scanned within
// hours, and there is no password to fall back on: the only thing standing
// between a scanner and somebody's archive is that the address cannot be
// guessed. Sixteen bytes is far past the point where that is worth attempting.
const aliasBytes = 16

// newAlias mints a local part to paste into MAILGATE_ALIASES.
func newAlias() (string, error) {
	buffer := make([]byte, aliasBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate alias: %w", err)
	}
	return aliasPrefix + hex.EncodeToString(buffer), nil
}

// splitAddress cuts an envelope recipient into its local part and domain,
// lowercased. Anything that is not exactly one @ is rejected by the caller.
func splitAddress(address string) (local, domain string, ok bool) {
	address = strings.Trim(strings.TrimSpace(address), "<>")
	at := strings.LastIndex(address, "@")
	if at <= 0 || at == len(address)-1 {
		return "", "", false
	}
	return strings.ToLower(address[:at]), strings.ToLower(address[at+1:]), true
}

// aliasOf drops any +tag a sender or a forwarding rule appended to the local
// part, so a mail rule can label what it forwards without breaking the lookup.
func aliasOf(local string) string {
	if plus := strings.Index(local, "+"); plus >= 0 {
		local = local[:plus]
	}
	return local
}
