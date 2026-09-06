package mailpdf

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
	"github.com/go-pdf/fpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"golang.org/x/text/encoding/charmap"
)

// attachmentKind is how one attachment can contribute pages.
type attachmentKind int

const (
	// kindUnsupported is anything this package cannot lay out: a .docx, a
	// spreadsheet, a calendar invite. The bytes are not lost silently -- the
	// cover page names the attachment and says it was not rendered.
	kindUnsupported attachmentKind = iota
	kindPDF
	kindImage
)

// imageMediaTypes are the raster formats pdfcpu can wrap in a page.
var imageMediaTypes = map[string]bool{
	"image/jpeg": true,
	"image/jpg":  true,
	"image/png":  true,
	"image/gif":  true,
	"image/tiff": true,
	"image/webp": true,
}

var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".tif": true, ".tiff": true, ".webp": true,
}

// classify decides what an attachment is.
//
// The media type is preferred but not trusted alone: scanners and phone mail
// clients routinely send a perfectly good PDF as application/octet-stream, and
// refusing to render it because of the sender's header would defeat the whole
// feature. The extension is the fallback, never the override.
func classify(a Attachment) attachmentKind {
	switch {
	case a.MediaType == "application/pdf", a.MediaType == "application/x-pdf":
		return kindPDF
	case imageMediaTypes[a.MediaType]:
		return kindImage
	}
	if a.MediaType != "" && a.MediaType != "application/octet-stream" &&
		a.MediaType != "application/binary" {
		return kindUnsupported
	}
	switch ext := strings.ToLower(filepath.Ext(a.Name)); {
	case ext == ".pdf":
		return kindPDF
	case imageExtensions[ext]:
		return kindImage
	}
	return kindUnsupported
}

// coverPage renders the envelope, the message text and the attachment list.
func coverPage(m *Mail) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(18, 18, 18)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetTitle(tr(m.Subject), true)
	pdf.AddPage()

	pdf.SetFont("Helvetica", "B", 15)
	subject := m.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	pdf.MultiCell(0, 7, tr(subject), "", "L", false)
	pdf.Ln(3)

	date := ""
	if !m.Date.IsZero() {
		date = m.Date.Format(time.RFC1123Z)
	}
	for _, field := range [][2]string{{"From", m.From}, {"To", m.To}, {"Date", date}} {
		if field[1] == "" {
			continue
		}
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(20, 5, field[0], "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr(field[1]), "", "L", false)
	}

	if len(m.Attachments) > 0 {
		pdf.Ln(2)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.CellFormat(20, 5, "Files", "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		for index, attachment := range m.Attachments {
			if index > 0 {
				pdf.SetX(pdf.GetX() + 20)
			}
			note := ""
			if classify(attachment) == kindUnsupported {
				note = " - not rendered, see the original mail"
			}
			line := fmt.Sprintf("%s (%s%s)", attachment.Name, humanSize(len(attachment.Data)), note)
			pdf.MultiCell(0, 5, tr(line), "", "L", false)
		}
	}

	pdf.Ln(4)
	pdf.SetDrawColor(180, 180, 180)
	left, _, right, _ := pdf.GetMargins()
	width, _ := pdf.GetPageSize()
	pdf.Line(left, pdf.GetY(), width-right, pdf.GetY())
	pdf.Ln(5)

	if body := strings.TrimSpace(m.Body); body != "" {
		pdf.SetFont("Courier", "", 9)
		pdf.MultiCell(0, 4.2, tr(body), "", "L", false)
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("mailpdf: render cover page: %w", err)
	}
	return out.Bytes(), nil
}

// attachmentPDF converts one attachment to PDF bytes. The second result is
// false for an attachment this package does not render, which the cover page
// has already reported.
func attachmentPDF(a Attachment) ([]byte, bool) {
	switch classify(a) {
	case kindPDF:
		return a.Data, true
	case kindImage:
		var out bytes.Buffer
		if err := api.ImportImages(nil, &out, []io.Reader{bytes.NewReader(a.Data)}, nil, nil); err != nil {
			// A picture pdfcpu cannot place is treated like any other file it
			// cannot lay out rather than failing the whole mail: the mail is
			// still worth keeping, and the cover page names the attachment.
			return nil, false
		}
		return out.Bytes(), true
	default:
		return nil, false
	}
}

// tr converts to the Windows-1252 the fpdf core fonts encode.
//
// The core fonts carry no Unicode tables, and shipping a TrueType font to embed
// would put a megabyte in the binary to typeset a covering note. Windows-1252
// covers the Latin scripts, the quotes and the dashes real mail is written in;
// anything outside it becomes '?' rather than a broken glyph.
func tr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\r':
			continue
		case '\n', '\t':
			b.WriteRune(r)
			continue
		}
		if encoded, ok := charmap.Windows1252.EncodeRune(r); ok {
			b.WriteByte(encoded)
			continue
		}
		b.WriteByte('?')
	}
	return b.String()
}

func humanSize(bytes int) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// normalizeBody trims the message text to something a fixed-width page can hold.
func normalizeBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	runes := []rune(body)
	if len(runes) > maxBodyRunes {
		body = string(runes[:maxBodyRunes]) + "\n\n[...] message truncated"
	}
	return body
}

var (
	htmlBlockTags = regexp.MustCompile(`(?is)</(p|div|tr|li|h[1-6])>|<br\s*/?>`)
	htmlDropped   = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	htmlTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRun      = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is the fallback for a mail with no text/plain part.
//
// It is a stripper, not a renderer: tables and layout are lost. That is
// accepted because the HTML alternative of a mail whose plain part is missing
// is nearly always a short note, and because the attachments -- the part
// somebody actually wants archived -- are unaffected either way.
func htmlToText(html string) string {
	if html == "" {
		return ""
	}
	text := htmlDropped.ReplaceAllString(html, "")
	text = htmlBlockTags.ReplaceAllString(text, "\n")
	text = htmlTag.ReplaceAllString(text, "")
	text = unescapeEntities(text)
	text = blankRun.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

var entities = strings.NewReplacer(
	"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
	"&quot;", `"`, "&#39;", "'", "&apos;", "'",
)

func unescapeEntities(s string) string { return entities.Replace(s) }

// unsafeFileName is everything a document name must not carry into a filesystem
// or a Content-Disposition header.
var unsafeFileName = regexp.MustCompile(`[^\p{L}\p{N} ._+-]+`)

func sanitizeFileName(name string) string {
	cleaned := unsafeFileName.ReplaceAllString(name, " ")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.Trim(cleaned, " .")
}

// addressList renders one address header for the cover page.
func addressList(header gomail.Header, key string) string {
	addresses, err := header.AddressList(key)
	if err != nil || len(addresses) == 0 {
		// Not every sender writes a parseable header, and the raw value is
		// still the most useful thing to print.
		raw, _ := header.Text(key)
		return strings.TrimSpace(raw)
	}
	rendered := make([]string, 0, len(addresses))
	for _, address := range addresses {
		rendered = append(rendered, formatAddress(address))
	}
	return strings.Join(rendered, ", ")
}

func formatAddress(address *mail.Address) string {
	if address.Name == "" {
		return address.Address
	}
	return fmt.Sprintf("%s <%s>", address.Name, address.Address)
}

func headerText(header gomail.Header, key string) string {
	value, err := header.Text(key)
	if err != nil {
		value = header.Get(key)
	}
	return strings.TrimSpace(value)
}

// attachmentName falls back to a generated name for a part that carries none,
// which is common for inline images and for anything a phone client sends.
func attachmentName(name, mediaType string, index int) string {
	if decoded := sanitizeFileName(path.Base(strings.TrimSpace(name))); decoded != "" {
		return decoded
	}
	extension := ""
	if extensions, err := mime.ExtensionsByType(mediaType); err == nil && len(extensions) > 0 {
		extension = extensions[0]
	}
	return fmt.Sprintf("attachment-%d%s", index, extension)
}
