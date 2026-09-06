package main

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-smtp"

	"lemmary/mailgate/mailpdf"
)

// smtpErrNoSuchUser is the permanent refusal for an address we do not host. It
// is deliberately the same reply for a malformed address, an unknown alias and
// a wrong domain, so probing cannot tell them apart.
var smtpErrNoSuchUser = &smtp.SMTPError{
	Code:         550,
	EnhancedCode: smtp.EnhancedCode{5, 1, 1},
	Message:      "No such recipient here",
}

var smtpErrTooManyMessages = &smtp.SMTPError{
	Code:         451,
	EnhancedCode: smtp.EnhancedCode{4, 7, 0},
	Message:      "Too many messages, try again later",
}

var smtpErrTemporary = &smtp.SMTPError{
	Code:         451,
	EnhancedCode: smtp.EnhancedCode{4, 3, 0},
	Message:      "Message could not be filed, try again later",
}

var smtpErrUnreadable = &smtp.SMTPError{
	Code:         554,
	EnhancedCode: smtp.EnhancedCode{5, 6, 0},
	Message:      "Message could not be converted to a document",
}

type backend struct {
	settings config
	aliases  *aliasTable
	spool    *spool
	logger   *slog.Logger
	limiter  *limiter
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{backend: b, remote: remoteHost(c)}, nil
}

// session holds one SMTP transaction.
type session struct {
	backend *backend
	remote  string
	from    string
	owners  []string
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}

// Rcpt resolves the recipient to an owner and refuses everything else. This is
// the whole of the access control: nothing that is not a live alias on the
// configured domain ever gets as far as DATA.
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	local, domain, ok := splitAddress(to)
	if !ok || domain != s.backend.settings.domain {
		return smtpErrNoSuchUser
	}
	owner, ok := s.backend.aliases.lookup(aliasOf(local))
	if !ok {
		s.backend.logger.Debug("recipient refused", "remote", s.remote, "to", to)
		return smtpErrNoSuchUser
	}
	for _, existing := range s.owners {
		if existing == owner {
			return nil
		}
	}
	s.owners = append(s.owners, owner)
	return nil
}

// Data converts the message and writes it to the spool for every accepted
// recipient.
//
// It returns only after the files are in place, so the 250 the sender sees
// means the document is where Lemmary will find it.
func (s *session) Data(r io.Reader) error {
	if len(s.owners) == 0 {
		return smtpErrNoSuchUser
	}
	if !s.backend.limiter.allow(s.remote) {
		s.backend.logger.Warn("rate limited", "remote", s.remote)
		return smtpErrTooManyMessages
	}

	parsed, rendered, err := mailpdf.Convert(r)
	if err != nil {
		s.backend.logger.Error("convert failed",
			"remote", s.remote, "from", s.from, slog.Any("error", err))
		return smtpErrUnreadable
	}

	for _, owner := range s.owners {
		path, err := s.backend.spool.write(owner, parsed.FileName(), rendered)
		if err != nil {
			s.backend.logger.Error("write failed", "owner", owner, slog.Any("error", err))
			return smtpErrTemporary
		}
		s.backend.logger.Info("filed",
			"owner", owner, "path", path, "from", s.from,
			"subject", parsed.Subject, "attachments", len(parsed.Attachments))
	}
	return nil
}

func (s *session) Reset() {
	s.from = ""
	s.owners = nil
}

func (s *session) Logout() error { return nil }

// remoteHost is the client address a rate limit is keyed on.
func remoteHost(c *smtp.Conn) string {
	if c == nil || c.Conn() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(c.Conn().RemoteAddr().String())
	if err != nil {
		return c.Conn().RemoteAddr().String()
	}
	return host
}

// limiter counts accepted messages per client address in a sliding hour.
type limiter struct {
	mu      sync.Mutex
	perHour int
	seen    map[string][]time.Time
	now     func() time.Time
}

func newLimiter(perHour int) *limiter {
	return &limiter{perHour: perHour, seen: map[string][]time.Time{}, now: time.Now}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-time.Hour)
	// Every key is swept, not only this one, so an address that sends once and
	// never returns does not hold a slot in the map forever.
	for seenKey, times := range l.seen {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(l.seen, seenKey)
			continue
		}
		l.seen[seenKey] = kept
	}

	if len(l.seen[key]) >= l.perHour {
		return false
	}
	l.seen[key] = append(l.seen[key], now)
	return true
}
