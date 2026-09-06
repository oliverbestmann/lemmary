// Package mailpdf turns one received email into one PDF.
//
// A mail is a body plus attachments, and the archive stores documents, so the
// two have to be flattened into a single file before anything downstream --
// checksum dedup, OCR, AI metadata -- can treat it like every other upload.
// The result is always one PDF: a cover page carrying the envelope and the
// message text, followed by each attachment's pages in the order the mail
// listed them.
//
// Rendering is pure Go on purpose. The Dockerfile's only native runtime
// dependency is poppler-utils, and reaching for a headless browser to lay out
// a two-line "here is the invoice" would multiply the image size for the least
// interesting page in the document.
package mailpdf

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	gomessage "github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers the non-UTF-8 decoders
	gomail "github.com/emersion/go-message/mail"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func init() {
	// pdfcpu otherwise writes a configuration directory under $XDG_CONFIG_HOME
	// on first use. The container has no business keeping one, and the write
	// fails on a read-only or absent home.
	model.ConfigPath = "disable"
}

// maxBodyRunes bounds how much message text reaches the cover page. A mailing
// list footer or a quoted thread can run to megabytes, and a hundred pages of
// it in front of the invoice makes the document worse, not more complete.
const maxBodyRunes = 40000

// Mail is the part of a received message this package can act on.
type Mail struct {
	From        string
	To          string
	Subject     string
	Date        time.Time
	Body        string
	Attachments []Attachment
}

// Attachment is one decoded MIME part that was not the message body.
type Attachment struct {
	Name      string
	MediaType string
	Data      []byte
}

// Parse decodes a complete RFC 5322 message.
//
// Anything that cannot be decoded is degraded rather than fatal: a mail with an
// unreadable part still becomes a document, because the operator asked for the
// mail and a partial one beats a bounce they have to go and investigate.
func Parse(r io.Reader) (*Mail, error) {
	entity, err := gomessage.Read(r)
	if err != nil && !gomessage.IsUnknownCharset(err) {
		return nil, fmt.Errorf("mailpdf: read message: %w", err)
	}

	reader := gomail.NewReader(entity)
	defer reader.Close()

	parsed := &Mail{
		From:    addressList(reader.Header, "From"),
		To:      addressList(reader.Header, "To"),
		Subject: headerText(reader.Header, "Subject"),
	}
	if date, err := reader.Header.Date(); err == nil {
		parsed.Date = date
	}

	var plain, html string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if gomessage.IsUnknownCharset(err) || gomessage.IsUnknownEncoding(err) {
				continue
			}
			return nil, fmt.Errorf("mailpdf: read part: %w", err)
		}

		switch header := part.Header.(type) {
		case *gomail.InlineHeader:
			mediaType, _, _ := header.ContentType()
			body, err := io.ReadAll(part.Body)
			if err != nil {
				continue
			}
			// The first part of each kind wins: a mail that repeats text/plain
			// in several inline parts is a thread, and the top of it is the
			// part somebody actually wrote.
			if mediaType == "text/html" {
				if html == "" {
					html = string(body)
				}
				continue
			}
			if plain == "" {
				plain = string(body)
			}
		case *gomail.AttachmentHeader:
			data, err := io.ReadAll(part.Body)
			if err != nil || len(data) == 0 {
				continue
			}
			mediaType, _, _ := header.ContentType()
			name, _ := header.Filename()
			parsed.Attachments = append(parsed.Attachments, Attachment{
				Name:      attachmentName(name, mediaType, len(parsed.Attachments)+1),
				MediaType: strings.ToLower(mediaType),
				Data:      data,
			})
		}
	}

	if plain != "" {
		parsed.Body = normalizeBody(plain)
	} else {
		parsed.Body = normalizeBody(htmlToText(html))
	}
	return parsed, nil
}

// Render lays the mail out as a single PDF.
func Render(m *Mail) ([]byte, error) {
	cover, err := coverPage(m)
	if err != nil {
		return nil, err
	}

	parts := [][]byte{cover}
	for _, attachment := range m.Attachments {
		converted, ok := attachmentPDF(attachment)
		if !ok {
			// Already reported on the cover page, which is written first and
			// makes the same judgement about the same attachment.
			continue
		}
		parts = append(parts, converted)
	}
	if len(parts) == 1 {
		return cover, nil
	}

	readers := make([]io.ReadSeeker, 0, len(parts))
	for _, part := range parts {
		readers = append(readers, bytes.NewReader(part))
	}
	var merged bytes.Buffer
	if err := api.MergeRaw(readers, &merged, false, nil); err != nil {
		return nil, fmt.Errorf("mailpdf: merge: %w", err)
	}
	return merged.Bytes(), nil
}

// Convert parses and renders in one step.
func Convert(r io.Reader) (*Mail, []byte, error) {
	parsed, err := Parse(r)
	if err != nil {
		return nil, nil, err
	}
	rendered, err := Render(parsed)
	if err != nil {
		return nil, nil, err
	}
	return parsed, rendered, nil
}

// FileName is what the document is called in the archive: the date the mail
// was sent, then its subject, so a list sorted by name reads chronologically.
func (m *Mail) FileName() string {
	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}
	subject := sanitizeFileName(m.Subject)
	if subject == "" {
		subject = "email"
	}
	// Long subjects are truncated because some filesystems cap a name at 255
	// bytes and the date prefix is the part worth keeping whole.
	if len(subject) > 120 {
		subject = strings.TrimSpace(subject[:120])
	}
	return fmt.Sprintf("%s %s.pdf", date.Format("2006-01-02"), subject)
}
