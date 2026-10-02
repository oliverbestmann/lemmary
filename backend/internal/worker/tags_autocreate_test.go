package worker

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tools/filesystem"

	"github.com/pocketbase/pocketbase/core"

	"lemmary/backend/internal/ai"
	"lemmary/backend/internal/models"
)

type tagRebuildExtractor struct {
	stubExtractor
	offered  []string
	tagsOnly bool
	sent     int
}

func (f *tagRebuildExtractor) ExtractMetadata(_ context.Context, text string, catalog ai.ExtractionCatalog) (*models.ExtractedMetadata, error) {
	f.offered = catalog.Tags
	f.tagsOnly = catalog.TagsOnly
	f.sent = len(text)
	return &models.ExtractedMetadata{
		Title:         "from the model",
		Tags:          []string{"rechnung"},
		SuggestedTags: []string{"Heizung"},
	}, nil
}

func TestRebuildDocumentTagsAddsTagsOnly(t *testing.T) {
	app := bootTagTestApp(t)
	owner := createTagUser(t, app, "owner@example.com")
	rechnung := createTag(t, app, "Rechnung", owner)
	stale := createTag(t, app, "Stale", owner)

	documents, err := app.FindCollectionByNameOrId("documents")
	if err != nil {
		t.Fatal(err)
	}
	doc := core.NewRecord(documents)
	doc.Set("user", owner)
	doc.Set("title", "hand written")
	doc.Set("ocr_text", strings.Repeat("Rechnung Heizung ", 1000))
	doc.Set("tags", []string{stale})
	file, err := filesystem.NewFileFromBytes([]byte("x"), "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	doc.Set("file", file)
	if err := app.Save(doc); err != nil {
		t.Fatalf("save document: %v", err)
	}

	extractor := &tagRebuildExtractor{}
	if err := RebuildDocumentTags(context.Background(), app, extractor, doc); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !slices.Contains(extractor.offered, "Rechnung") {
		t.Fatalf("offered tags = %v, want the existing vocabulary", extractor.offered)
	}

	if !extractor.tagsOnly || extractor.sent > rebuildTagsDocBytes {
		t.Fatalf("tagsOnly = %v, sent %d bytes; want a tags-only call on at most %d", extractor.tagsOnly, extractor.sent, rebuildTagsDocBytes)
	}

	stored, err := app.FindRecordById("documents", doc.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.GetString("title"); got != "hand written" {
		t.Fatalf("title = %q, want it untouched", got)
	}
	heizung, err := findTagByName(app, owner, "Heizung")
	if err != nil || heizung == "" {
		t.Fatalf("Heizung tag not created: %v", err)
	}
	got := stored.GetStringSlice("tags")
	if len(got) != 3 || !slices.Contains(got, stale) || !slices.Contains(got, rechnung) || !slices.Contains(got, heizung) {
		t.Fatalf("document tags = %v, want the kept %q plus Rechnung %q and Heizung %q", got, stale, rechnung, heizung)
	}
}
