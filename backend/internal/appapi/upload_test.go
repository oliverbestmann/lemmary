package appapi

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/apitoken"
)

// minimalPDF is just enough for Go's content sniffer (and so the documents
// collection's FileField mimetype check) to recognize application/pdf; it is
// not a valid rendered document, which the pipeline never runs during this
// test.
const minimalPDF = "%PDF-1.4\n1 0 obj<<>>endobj\n%%EOF"

func newUploadRequest(t *testing.T, fieldName, fileName, body string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestPostUploadCreatesAPendingDocumentOwnedByTheCaller(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "scripted@example.test")

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = user
	e.Request = newUploadRequest(t, "file", "invoice.pdf", minimalPDF)
	e.Response = httptest.NewRecorder()

	if err := handlePostUpload(app)(e); err != nil {
		t.Fatalf("handlePostUpload() error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	var resp uploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Fatal("response carried no document id")
	}
	if resp.ProcessingStatus != "pending" {
		t.Fatalf("processing_status = %q, want pending", resp.ProcessingStatus)
	}

	record, err := app.FindRecordById("documents", resp.ID)
	if err != nil {
		t.Fatalf("find created document: %v", err)
	}
	if got := record.GetString("user"); got != user.Id {
		t.Fatalf("document user = %q, want %q", got, user.Id)
	}
	if got := record.GetString("file"); got == "" {
		t.Fatal("document has no stored file")
	}
}

// bindAuth is what protects this endpoint in the real route table, so an API
// token has to be able to drive it end to end -- that's the whole point of
// giving scripts a token instead of a browser session.
func TestPostUploadWorksThroughBindAuthWithAnAPIToken(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "scripter@example.test")
	_, raw, err := apitoken.Create(app, user.Id, "upload script")
	if err != nil {
		t.Fatalf("apitoken.Create() error: %v", err)
	}

	e := &core.RequestEvent{}
	e.App = app
	e.Request = newUploadRequest(t, "file", "statement.pdf", minimalPDF)
	e.Request.Header.Set("Authorization", "Bearer "+raw)
	e.Response = httptest.NewRecorder()

	handler := bindAuth(handlePostUpload(app))
	if err := handler(e); err != nil {
		t.Fatalf("bindAuth(handlePostUpload)() error: %v", err)
	}

	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
}

func TestPostUploadRejectsNonPDFFilenames(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "wrongtype@example.test")

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = user
	e.Request = newUploadRequest(t, "file", "notes.txt", "just some text")
	e.Response = httptest.NewRecorder()

	if err := handlePostUpload(app)(e); err != nil {
		t.Fatalf("handlePostUpload() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

func TestPostUploadRejectsMissingFile(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)
	user := makeAPITokenTestUser(t, app, "empty@example.test")

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	e := &core.RequestEvent{}
	e.App = app
	e.Auth = user
	e.Request = req
	e.Response = httptest.NewRecorder()

	if err := handlePostUpload(app)(e); err != nil {
		t.Fatalf("handlePostUpload() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

func TestPostUploadRejectsUnauthenticatedRequests(t *testing.T) {
	t.Parallel()
	app := bootAPITokenTestApp(t)

	e := &core.RequestEvent{}
	e.App = app
	e.Request = newUploadRequest(t, "file", "invoice.pdf", minimalPDF)
	e.Response = httptest.NewRecorder()

	handler := bindAuth(handlePostUpload(app))
	if err := handler(e); err != nil {
		t.Fatalf("bindAuth(handlePostUpload)() error: %v", err)
	}
	rec := e.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
