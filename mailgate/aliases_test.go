package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustParseAliases(t *testing.T, raw string) *aliasTable {
	t.Helper()
	table, err := parseAliases(raw)
	if err != nil {
		t.Fatalf("parseAliases(%q): %v", raw, err)
	}
	return table
}

func TestAliasTableLookup(t *testing.T) {
	t.Parallel()

	table := mustParseAliases(t, `{"in-abc": "ada@example.com", "IN-DEF": "ben@example.com"}`)

	if owner, ok := table.lookup("in-abc"); !ok || owner != "ada@example.com" {
		t.Errorf("lookup(in-abc) = %q, %v; want ada@example.com, true", owner, ok)
	}
	// Aliases are matched lowercased, because SMTP local parts arrive in
	// whatever case the sender typed.
	if owner, ok := table.lookup("in-def"); !ok || owner != "ben@example.com" {
		t.Errorf("lookup(in-def) = %q, %v; want ben@example.com, true", owner, ok)
	}
	if _, ok := table.lookup("in-nothing"); ok {
		t.Error("an unknown alias must not resolve")
	}
	if table.count() != 2 {
		t.Errorf("count() = %d, want 2", table.count())
	}
}

// Two aliases for one account is a normal thing to configure -- one per scanner,
// so a leak can be revoked without disturbing the other.
func TestAliasTableAllowsSeveralAliasesPerOwner(t *testing.T) {
	t.Parallel()

	table := mustParseAliases(t, `{"in-abc": "ada@example.com", "in-def": "ada@example.com"}`)
	if got := table.owners(); len(got) != 1 || got[0] != "ada@example.com" {
		t.Errorf("owners() = %v, want one account", got)
	}
	if table.count() != 2 {
		t.Errorf("count() = %d, want 2", table.count())
	}
}

// A typo that silently dropped one mapping would look like working software
// until somebody noticed months of mail had been refused.
func TestAliasesRejectBadConfiguration(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":              "",
		"not JSON":           "in-abc ada@example.com",
		"not an object":      `["in-abc"]`,
		"no aliases":         "{}",
		"empty alias":        `{"": "ada@example.com"}`,
		"path in owner":      `{"in-abc": "../escape@example.com"}`,
		"hidden owner":       `{"in-abc": ".hidden@example.com"}`,
		"owner not an email": `{"in-abc": "notanemail"}`,
		"owner empty":        `{"in-abc": ""}`,
	}
	for name, raw := range cases {
		if _, err := parseAliases(raw); err == nil {
			t.Errorf("%s: parseAliases(%q) accepted it, want an error", name, raw)
		}
	}
}

// The same alias twice with different owners is a genuine ambiguity about whose
// archive a document belongs in.
func TestAliasesRejectAConflictingMapping(t *testing.T) {
	t.Parallel()

	if _, err := parseAliases(`{"in-abc": "ada@example.com", "IN-ABC": "ben@example.com"}`); err == nil {
		t.Error("two owners for one alias were accepted, want an error")
	}
}

// Surrounding whitespace is what a multi-line value in a compose file or a
// systemd unit carries in.
func TestAliasesTolerateWhitespace(t *testing.T) {
	t.Parallel()

	table := mustParseAliases(t, "\n  {\"  in-abc  \": \"  ada@example.com  \"}\n ")
	if owner, ok := table.lookup("in-abc"); !ok || owner != "ada@example.com" {
		t.Errorf("lookup(in-abc) = %q, %v; want the value trimmed", owner, ok)
	}
}

// The alias is the credential, so a generated one has to be unguessable and
// distinct every time.
func TestNewAlias(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		alias, err := newAlias()
		if err != nil {
			t.Fatalf("newAlias(): %v", err)
		}
		if !strings.HasPrefix(alias, aliasPrefix) || len(alias) != len(aliasPrefix)+2*aliasBytes {
			t.Fatalf("newAlias() = %q, want %s plus %d hex characters", alias, aliasPrefix, 2*aliasBytes)
		}
		if seen[alias] {
			t.Fatalf("newAlias() repeated %q", alias)
		}
		seen[alias] = true
	}
}

func TestSplitAddress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in     string
		local  string
		domain string
		ok     bool
	}{
		{"in-abc@Docs.Example.com", "in-abc", "docs.example.com", true},
		{"<in-abc@docs.example.com>", "in-abc", "docs.example.com", true},
		{"no-at-sign", "", "", false},
		{"@docs.example.com", "", "", false},
		{"in-abc@", "", "", false},
	}
	for _, c := range cases {
		local, domain, ok := splitAddress(c.in)
		if local != c.local || domain != c.domain || ok != c.ok {
			t.Errorf("splitAddress(%q) = %q, %q, %v; want %q, %q, %v",
				c.in, local, domain, ok, c.local, c.domain, c.ok)
		}
	}
}

// A forwarding rule may label what it sends, and that must not break the
// lookup.
func TestAliasOfDropsATag(t *testing.T) {
	t.Parallel()

	if got := aliasOf("in-abc+scans"); got != "in-abc" {
		t.Errorf("aliasOf() = %q, want in-abc", got)
	}
	if got := aliasOf("in-abc"); got != "in-abc" {
		t.Errorf("aliasOf() = %q, want in-abc", got)
	}
}
