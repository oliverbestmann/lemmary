package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

// testServer starts a listener on a loopback port and returns its address plus
// the spool root it files into.
func testServer(t *testing.T, aliases string, perHour int) (addr, root string) {
	t.Helper()

	root = t.TempDir()
	table := mustParseAliases(t, aliases)

	server := smtp.NewServer(&backend{
		settings: config{domain: "docs.example.com"},
		aliases:  table,
		spool:    &spool{root: root},
		logger:   discardLogger(),
		limiter:  newLimiter(perHour),
	})
	server.Domain = "docs.example.com"
	server.MaxRecipients = maxRecipients
	server.ReadTimeout = 10 * time.Second
	server.WriteTimeout = 10 * time.Second

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })

	return listener.Addr().String(), root
}

// send runs one SMTP transaction and returns the error the server replied with.
func send(t *testing.T, addr, to, message string) error {
	t.Helper()

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Hello("sender.example.com"); err != nil {
		t.Fatalf("helo: %v", err)
	}
	return client.SendMail("sender@example.com", []string{to}, strings.NewReader(message))
}

func ownerFiles(t *testing.T, root, owner string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, owner))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read owner dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// The whole path: a mail with an attachment arrives and a PDF appears in the
// owner's watch directory.
func TestDeliveryWritesAPDF(t *testing.T) {
	t.Parallel()

	addr, root := testServer(t, `{"in-abc": "ada@example.com"}`, defaultMaxPerHour)
	message := mimeMail("Invoice 1234", "Invoice attached.", testPDF(t))

	if err := send(t, addr, "in-abc@docs.example.com", message); err != nil {
		t.Fatalf("SendMail: %v", err)
	}

	files := ownerFiles(t, root, "ada@example.com")
	if len(files) != 1 {
		t.Fatalf("owner directory holds %v, want one PDF", files)
	}
	if !strings.HasSuffix(files[0], "Invoice 1234.pdf") {
		t.Errorf("file is %q, want it named after the subject", files[0])
	}
	data, err := os.ReadFile(filepath.Join(root, "ada@example.com", files[0]))
	if err != nil {
		t.Fatalf("read PDF: %v", err)
	}
	if !strings.HasPrefix(string(data), "%PDF-") {
		t.Error("the filed document is not a PDF")
	}
}

// The access control. An address that is not a live alias must be refused at
// RCPT TO, before the body is transferred, and must leave nothing behind.
func TestUnknownRecipientIsRefused(t *testing.T) {
	t.Parallel()

	addr, root := testServer(t, `{"in-abc": "ada@example.com"}`, defaultMaxPerHour)

	for _, recipient := range []string{
		"in-nothing@docs.example.com", // right domain, unknown alias
		"in-abc@elsewhere.example",    // known alias, wrong domain
		"postmaster@docs.example.com", // a name a scanner would try
	} {
		err := send(t, addr, recipient, mimeMail("Hello", "hi", nil))
		if err == nil {
			t.Errorf("send to %q was accepted, want a refusal", recipient)
			continue
		}
		var smtpErr *smtp.SMTPError
		if !asSMTPError(err, &smtpErr) || smtpErr.Code != 550 {
			t.Errorf("send to %q failed with %v, want a 550", recipient, err)
		}
	}

	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("spool root holds %d entries, want nothing written", len(entries))
	}
}

// A tagged alias is how a forwarding rule labels what it sends.
func TestTaggedAliasIsAccepted(t *testing.T) {
	t.Parallel()

	addr, root := testServer(t, `{"in-abc": "ada@example.com"}`, defaultMaxPerHour)
	if err := send(t, addr, "in-abc+scanner@docs.example.com", mimeMail("Scan", "hi", nil)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	if files := ownerFiles(t, root, "ada@example.com"); len(files) != 1 {
		t.Errorf("owner directory holds %v, want one PDF", files)
	}
}

// What bounds the damage of a leaked alias.
func TestRateLimitRefusesTheSecondMessage(t *testing.T) {
	t.Parallel()

	addr, root := testServer(t, `{"in-abc": "ada@example.com"}`, 1)

	if err := send(t, addr, "in-abc@docs.example.com", mimeMail("One", "hi", nil)); err != nil {
		t.Fatalf("first SendMail: %v", err)
	}
	err := send(t, addr, "in-abc@docs.example.com", mimeMail("Two", "hi", nil))
	if err == nil {
		t.Fatal("the second message was accepted, want it rate limited")
	}
	var smtpErr *smtp.SMTPError
	if !asSMTPError(err, &smtpErr) || smtpErr.Code != 451 {
		t.Errorf("second message failed with %v, want a 451", err)
	}
	// A 451 is temporary, so the sending server keeps the message; it must not
	// also have been filed.
	if files := ownerFiles(t, root, "ada@example.com"); len(files) != 1 {
		t.Errorf("owner directory holds %v, want only the first message", files)
	}
}

// The window is a sliding hour, and an address that goes quiet must not keep a
// slot in the map forever.
func TestRateLimitSweepsOldEntries(t *testing.T) {
	t.Parallel()

	now := time.Now()
	l := newLimiter(2)
	l.now = func() time.Time { return now }

	if !l.allow("10.0.0.1") || !l.allow("10.0.0.1") {
		t.Fatal("the first two messages must be allowed")
	}
	if l.allow("10.0.0.1") {
		t.Fatal("the third message must be refused")
	}
	// Another address is counted separately.
	if !l.allow("10.0.0.2") {
		t.Error("a different client must not inherit the first one's count")
	}

	now = now.Add(61 * time.Minute)
	if !l.allow("10.0.0.1") {
		t.Error("the window must expire")
	}
	if _, held := l.seen["10.0.0.2"]; held {
		t.Error("an address that went quiet must not keep a slot in the map")
	}
}

// mimeMail builds a message, optionally with one PDF attached.
func mimeMail(subject, body string, attachment []byte) string {
	if attachment == nil {
		return "From: sender@example.com\r\nSubject: " + subject + "\r\n\r\n" + body + "\r\n"
	}
	const boundary = "sep"
	var out strings.Builder
	out.WriteString("From: sender@example.com\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\n")
	out.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	out.WriteString("--" + boundary + "\r\nContent-Type: text/plain\r\n\r\n" + body + "\r\n")
	out.WriteString("--" + boundary + "\r\nContent-Type: application/pdf\r\n")
	out.WriteString("Content-Disposition: attachment; filename=\"invoice.pdf\"\r\n")
	out.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	out.WriteString(base64Wrapped(attachment) + "\r\n")
	out.WriteString("--" + boundary + "--\r\n")
	return out.String()
}
