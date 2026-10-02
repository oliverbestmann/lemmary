package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/list"

	"lemmary/backend/internal/ai"
	"lemmary/backend/internal/strutil"
)

// The same cut appapi's tag assignment makes: deciding a tag does not need the
// whole text.
const rebuildTagsDocBytes = 4000

func createMissingTags(state *StepState, dropped, suggested []string) error {
	_, missing, err := addMatchedTags(state.App, state.Document, append(append([]string{}, dropped...), suggested...))
	if err != nil {
		return err
	}
	names := pendingTagSuggestions(missing)
	if len(names) == 0 {
		return nil
	}
	ids := state.Document.GetStringSlice("tags")
	for _, name := range names {
		id, _, err := EnsureTag(state.App, state.Document.GetString("user"), name)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	state.Document.Set("tags", list.ToUniqueStringSlice(ids))
	return nil
}

// RebuildDocumentTags adds a fresh extraction's tags to the document and writes
// nothing else of that answer.
func RebuildDocumentTags(ctx context.Context, app core.App, extractor ai.Extractor, document *core.Record) error {
	ocrText := strings.TrimSpace(document.GetString("ocr_text"))
	if ocrText == "" {
		return fmt.Errorf("document has no text")
	}
	catalog, err := loadExtractionCatalog(app, strings.TrimSpace(document.GetString("user")), app.Logger())
	if err != nil {
		return err
	}
	catalog.SuggestNewTags = true
	catalog.TagsOnly = true

	metadata, err := extractor.ExtractMetadata(ctx, strutil.Truncate(ocrText, rebuildTagsDocBytes), catalog)
	if err != nil {
		return fmt.Errorf("ai extraction: %w", err)
	}

	if err := createMissingTags(&StepState{App: app, Document: document}, metadata.Tags, metadata.SuggestedTags); err != nil {
		return err
	}
	document.IgnoreUnchangedFields(true)
	return app.Save(document)
}
