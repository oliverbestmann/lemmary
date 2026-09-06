// Command mailgate receives documents by email and drops them into Lemmary's
// watch directory.
//
// It is deliberately not part of the server. Lemmary already imports anything
// that appears under $WATCH_DIR/<account-email>/, so receiving mail needs no
// database access, no collections and no hooks -- only a file written to the
// right directory. Keeping it a separate process also keeps a listener that
// faces the public internet out of the process that holds the archive.
//
//	mail --> mailgate --> $WATCH_DIR/alice@example.com/2026-09-06 Invoice.pdf
//	                          |
//	                          '--> Lemmary's watch import (dedup, OCR, metadata)
//
// # Facing the internet
//
// This is meant to be the MX for a domain, so anything on the internet can
// open a connection to it. Three properties keep that safe:
//
//   - It never relays. A recipient that is not a configured alias is refused
//     at RCPT TO, before the message body is transferred at all.
//   - The alias is the credential. There is no authentication, so an alias must
//     be long and random; -alias mints one. See README.md.
//   - Nothing is accepted before it is stored. The 250 goes out after the PDF
//     is in the spool directory, so a failure leaves the message in the sending
//     server's queue instead of dropping it.
//
// The sender is not authenticated and a From header is trivially forged. That
// is tolerable because the alias decides ownership and the mail is only ever
// filed, never acted on.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-smtp"
)

const (
	// defaultMaxMessageBytes caps one message. Sending servers are told this
	// through the SIZE extension, so an oversized mail is refused at MAIL FROM
	// and never transferred.
	defaultMaxMessageBytes int64 = 50 << 20
	// defaultMaxPerHour caps deliveries per client address. A leaked alias
	// otherwise fills the archive as fast as somebody can send.
	defaultMaxPerHour = 60
	// maxRecipients bounds one transaction. Aliases are secret and handed out
	// one per account, so a mail addressed to many of them is far more likely
	// to be a scan than a real delivery.
	maxRecipients = 5

	readTimeout  = 2 * time.Minute
	writeTimeout = 2 * time.Minute
)

// config is the resolved environment.
type config struct {
	addr            string
	domain          string
	aliases         *aliasTable
	spoolDir        string
	maxMessageBytes int64
	maxPerHour      int
}

func main() {
	check := flag.Bool("check", false, "validate the configuration, then exit")
	mint := flag.Bool("alias", false, "print a fresh random alias for MAILGATE_ALIASES, then exit")
	flag.Parse()

	if *mint {
		alias, err := newAlias()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(alias)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

	settings, err := loadConfig()
	if err != nil {
		logger.Error("configuration", slog.Any("error", err))
		os.Exit(2)
	}

	if *check {
		fmt.Printf("ok: listening on %s for @%s, spooling to %s\n",
			settings.addr, settings.domain, settings.spoolDir)
		fmt.Printf("%d aliases for %d accounts: %s\n",
			settings.aliases.count(), len(settings.aliases.owners()),
			strings.Join(settings.aliases.owners(), ", "))
		return
	}

	if err := run(settings, logger); err != nil {
		logger.Error("mailgate stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(settings config, logger *slog.Logger) error {
	spool := &spool{root: settings.spoolDir}
	if err := spool.check(); err != nil {
		return err
	}

	server := smtp.NewServer(&backend{
		settings: settings,
		aliases:  settings.aliases,
		spool:    spool,
		logger:   logger,
		limiter:  newLimiter(settings.maxPerHour),
	})
	server.Addr = settings.addr
	server.Domain = settings.domain
	server.MaxMessageBytes = settings.maxMessageBytes
	server.MaxRecipients = maxRecipients
	server.ReadTimeout = readTimeout
	server.WriteTimeout = writeTimeout
	// Nothing here authenticates, so there is no credential a cleartext
	// connection could leak. Mail from the internet arrives in cleartext or not
	// at all.
	server.AllowInsecureAuth = false
	server.EnableSMTPUTF8 = true

	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stopping
		logger.Info("shutting down")
		// Close rather than a drain: a message still being received is safer
		// abandoned than half-filed, because the sending server still holds it
		// and will try again.
		_ = server.Close()
	}()

	logger.Info("mailgate listening",
		"addr", settings.addr, "domain", settings.domain,
		"spool", settings.spoolDir, "aliases", settings.aliases.count())

	if err := server.ListenAndServe(); err != nil && err != smtp.ErrServerClosed {
		return err
	}
	return nil
}

// loadConfig reads the environment.
func loadConfig() (config, error) {
	settings := config{
		addr:            envOr("MAILGATE_ADDR", ":2525"),
		domain:          strings.ToLower(strings.TrimSpace(os.Getenv("MAILGATE_DOMAIN"))),
		spoolDir:        strings.TrimSpace(os.Getenv("MAILGATE_SPOOL")),
		maxMessageBytes: defaultMaxMessageBytes,
		maxPerHour:      defaultMaxPerHour,
	}
	if settings.domain == "" {
		return config{}, fmt.Errorf("MAILGATE_DOMAIN is required")
	}
	aliases, err := parseAliases(os.Getenv("MAILGATE_ALIASES"))
	if err != nil {
		return config{}, err
	}
	settings.aliases = aliases
	if settings.spoolDir == "" {
		return config{}, fmt.Errorf("MAILGATE_SPOOL is required (Lemmary's WATCH_DIR)")
	}

	// A malformed limit is refused rather than silently replaced by the
	// default: both of these bound what a stranger can do to the archive, and
	// an operator who set one meant to.
	if raw := strings.TrimSpace(os.Getenv("MAILGATE_MAX_SIZE")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return config{}, fmt.Errorf("MAILGATE_MAX_SIZE must be a positive number of bytes, got %q", raw)
		}
		settings.maxMessageBytes = value
	}
	if raw := strings.TrimSpace(os.Getenv("MAILGATE_MAX_PER_HOUR")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return config{}, fmt.Errorf("MAILGATE_MAX_PER_HOUR must be a positive number, got %q", raw)
		}
		settings.maxPerHour = value
	}
	return settings, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func logLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAILGATE_LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
