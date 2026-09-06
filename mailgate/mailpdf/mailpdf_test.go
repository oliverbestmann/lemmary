package mailpdf

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// mimeMessage builds a multipart/mixed mail with the given body and parts.
func mimeMessage(t *testing.T, subject, body string, parts ...mimePart) string {
	t.Helper()

	const boundary = "sep"
	var out strings.Builder
	fmt.Fprintf(&out, "From: Ada Lovelace <ada@example.com>\r\n")
	fmt.Fprintf(&out, "To: in-abc@docs.example.com\r\n")
	fmt.Fprintf(&out, "Date: Sun, 06 Sep 2026 10:00:00 +0200\r\n")
	fmt.Fprintf(&out, "Subject: %s\r\n", subject)
	fmt.Fprintf(&out, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&out, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&out, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, body)
	for _, part := range parts {
		fmt.Fprintf(&out, "--%s\r\n", boundary)
		fmt.Fprintf(&out, "Content-Type: %s\r\n", part.mediaType)
		fmt.Fprintf(&out, "Content-Disposition: attachment; filename=%q\r\n", part.name)
		fmt.Fprintf(&out, "Content-Transfer-Encoding: base64\r\n\r\n")
		fmt.Fprintf(&out, "%s\r\n", chunked(base64.StdEncoding.EncodeToString(part.data)))
	}
	fmt.Fprintf(&out, "--%s--\r\n", boundary)
	return out.String()
}

type mimePart struct {
	name      string
	mediaType string
	data      []byte
}

func chunked(encoded string) string {
	var lines []string
	for len(encoded) > 76 {
		lines = append(lines, encoded[:76])
		encoded = encoded[76:]
	}
	return strings.Join(append(lines, encoded), "\r\n")
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: 40, B: 200, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return out.Bytes()
}

// testPDF builds a valid PDF with the given number of pages, so a test can
// assert on how many pages an attachment contributed. Generated rather than
// committed as a fixture, because the interesting property is the page count
// and that is easier to read here than in a binary.
func testPDF(t *testing.T, pages int) []byte {
	t.Helper()

	images := make([]io.Reader, 0, pages)
	for i := 0; i < pages; i++ {
		images = append(images, bytes.NewReader(testPNG(t)))
	}
	var out bytes.Buffer
	if err := api.ImportImages(nil, &out, images, nil, nil); err != nil {
		t.Fatalf("build test PDF: %v", err)
	}
	return out.Bytes()
}

func pageCount(t *testing.T, pdf []byte) int {
	t.Helper()
	count, err := api.PageCount(bytes.NewReader(pdf), nil)
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	return count
}

// The plain case: a mail with no attachments is still a document, and its text
// is the only thing in it.
func TestConvertBodyOnly(t *testing.T) {
	t.Parallel()

	raw := "From: ada@example.com\r\nTo: in-abc@docs.example.com\r\n" +
		"Subject: Monthly report\r\n\r\nHere is the report.\r\n"

	parsed, pdf, err := Convert(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Convert() error: %v", err)
	}
	if parsed.Subject != "Monthly report" {
		t.Errorf("Subject = %q, want %q", parsed.Subject, "Monthly report")
	}
	if !strings.Contains(parsed.Body, "Here is the report.") {
		t.Errorf("Body = %q, want it to carry the message text", parsed.Body)
	}
	if got := pageCount(t, pdf); got != 1 {
		t.Errorf("page count = %d, want 1", got)
	}
}

// The case the feature exists for: a covering note with an invoice attached
// becomes one document holding both.
func TestConvertMergesAttachmentsInOrder(t *testing.T) {
	t.Parallel()

	raw := mimeMessage(t, "Invoice 1234", "Invoice attached.",
		mimePart{name: "invoice.pdf", mediaType: "application/pdf", data: testPDF(t, 3)},
		mimePart{name: "photo.png", mediaType: "image/png", data: testPNG(t)},
	)

	parsed, pdf, err := Convert(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Convert() error: %v", err)
	}
	if len(parsed.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(parsed.Attachments))
	}
	// One cover page, three from the PDF, one for the image.
	if got := pageCount(t, pdf); got != 5 {
		t.Errorf("page count = %d, want 5 (cover + 3 + 1)", got)
	}
}

// A PDF sent as application/octet-stream is the common shape from scanners and
// phone mail clients. Refusing to render it because of the sender's header
// would defeat the whole feature.
func TestConvertTrustsTheExtensionWhenTheTypeIsGeneric(t *testing.T) {
	t.Parallel()

	raw := mimeMessage(t, "Scan", "",
		mimePart{name: "scan.pdf", mediaType: "application/octet-stream", data: testPDF(t, 2)},
	)

	_, pdf, err := Convert(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Convert() error: %v", err)
	}
	if got := pageCount(t, pdf); got != 3 {
		t.Errorf("page count = %d, want 3 (cover + 2)", got)
	}
}

// An attachment this package cannot lay out must not lose the mail. The
// document is still created, and the cover page names what was left out.
func TestConvertKeepsGoingPastAnUnsupportedAttachment(t *testing.T) {
	t.Parallel()

	raw := mimeMessage(t, "Contract", "See attached.",
		mimePart{
			name:      "contract.docx",
			mediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			data:      []byte("PK\x03\x04 not really a docx"),
		},
	)

	parsed, pdf, err := Convert(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Convert() error: %v", err)
	}
	if got := pageCount(t, pdf); got != 1 {
		t.Errorf("page count = %d, want 1 (cover only)", got)
	}
	if len(parsed.Attachments) != 1 || parsed.Attachments[0].Name != "contract.docx" {
		t.Errorf("attachments = %+v, want the docx recorded", parsed.Attachments)
	}
	if classify(parsed.Attachments[0]) != kindUnsupported {
		t.Error("a .docx must classify as unsupported")
	}
}

// A mail whose only body is HTML still needs readable text on the cover page.
func TestParseFallsBackToTheHTMLPart(t *testing.T) {
	t.Parallel()

	raw := "From: ada@example.com\r\nSubject: Notice\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>p{color:red}</style></head><body>" +
		"<p>Rechnung &amp; Beleg</p><p>Zweite Zeile</p></body></html>\r\n"

	parsed, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if !strings.Contains(parsed.Body, "Rechnung & Beleg") {
		t.Errorf("Body = %q, want the entity decoded", parsed.Body)
	}
	if !strings.Contains(parsed.Body, "Zweite Zeile") {
		t.Errorf("Body = %q, want the second block on its own line", parsed.Body)
	}
	if strings.Contains(parsed.Body, "color:red") {
		t.Errorf("Body = %q, want the stylesheet dropped", parsed.Body)
	}
}

// RFC 2047 is how any non-ASCII subject arrives, and the subject becomes the
// document's name.
func TestParseDecodesAnEncodedSubject(t *testing.T) {
	t.Parallel()

	raw := "From: ada@example.com\r\nDate: Sun, 06 Sep 2026 10:00:00 +0200\r\n" +
		"Subject: =?utf-8?q?Rechnung_f=C3=BCr_M=C3=A4rz?=\r\n\r\nbody\r\n"

	parsed, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if parsed.Subject != "Rechnung für März" {
		t.Errorf("Subject = %q, want the decoded text", parsed.Subject)
	}
	if got, want := parsed.FileName(), "2026-09-06 Rechnung für März.pdf"; got != want {
		t.Errorf("FileName() = %q, want %q", got, want)
	}
}

// The name goes into a directory and into a Content-Disposition header, so a
// subject may not carry a separator or a leading dot out of the mail.
func TestFileNameIsSafe(t *testing.T) {
	t.Parallel()

	m := &Mail{Subject: "../../etc/passwd: \"urgent\""}
	name := m.FileName()
	if strings.ContainsAny(name, `/\:"`) {
		t.Errorf("FileName() = %q, want no path or quoting characters", name)
	}
	if strings.HasPrefix(name, ".") {
		t.Errorf("FileName() = %q, want no leading dot", name)
	}

	empty := (&Mail{}).FileName()
	if !strings.HasSuffix(empty, "email.pdf") {
		t.Errorf("FileName() with no subject = %q, want a generated name", empty)
	}
}

// The core PDF fonts have no Unicode tables, so text is transcoded. Umlauts and
// typographic punctuation must survive that; anything outside the charset must
// degrade to a placeholder rather than a broken glyph.
func TestTrKeepsLatinTextAndReplacesTheRest(t *testing.T) {
	t.Parallel()

	if got, want := tr("Grüße – „Beleg“"), "Gr\xfc\xdfe \x96 \x84Beleg\x93"; got != want {
		t.Errorf("tr() = %q, want %q", got, want)
	}
	if got := tr("hello 😀"); got != "hello ?" {
		t.Errorf("tr() = %q, want the emoji replaced", got)
	}
	if got := tr("a\r\nb"); got != "a\nb" {
		t.Errorf("tr() = %q, want the carriage return dropped", got)
	}
}

// A quoted thread or a mailing list footer can run to megabytes, and a hundred
// pages of it in front of the invoice makes the document worse.
func TestNormalizeBodyTruncates(t *testing.T) {
	t.Parallel()

	body := normalizeBody(strings.Repeat("x", maxBodyRunes+500))
	if !strings.HasSuffix(body, "message truncated") {
		t.Error("an oversized body must be truncated and say so")
	}
	if len([]rune(body)) > maxBodyRunes+64 {
		t.Errorf("truncated body is %d runes, want about %d", len([]rune(body)), maxBodyRunes)
	}
}

// An attachment with no filename is common from phone clients, and the cover
// page still has to name it.
func TestAttachmentNameFallsBackToTheMediaType(t *testing.T) {
	t.Parallel()

	if got := attachmentName("", "image/png", 2); got != "attachment-2.png" {
		t.Errorf("attachmentName() = %q, want attachment-2.png", got)
	}
	if got := attachmentName("../evil.pdf", "application/pdf", 1); got != "evil.pdf" {
		t.Errorf("attachmentName() = %q, want the path stripped", got)
	}
}
