package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/emersion/go-smtp"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// testPDF builds a small two-page PDF to attach to a test message. It has to be
// a real one: the converter merges attachments, so a placeholder would fail for
// the wrong reason.
func testPDF(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		for y := 0; y < 20; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: 40, B: 200, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	var out bytes.Buffer
	pages := []io.Reader{bytes.NewReader(encoded.Bytes()), bytes.NewReader(encoded.Bytes())}
	if err := api.ImportImages(nil, &out, pages, nil, nil); err != nil {
		t.Fatalf("build test PDF: %v", err)
	}
	return out.Bytes()
}

// base64Wrapped encodes to the line length mail transfer expects.
func base64Wrapped(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var lines []string
	for len(encoded) > 76 {
		lines = append(lines, encoded[:76])
		encoded = encoded[76:]
	}
	return strings.Join(append(lines, encoded), "\r\n")
}

func asSMTPError(err error, target **smtp.SMTPError) bool {
	return errors.As(err, target)
}
