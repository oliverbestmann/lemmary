package ai

import (
	"strings"
	"testing"
)

func TestExtractionPromptAsksForGermanTags(t *testing.T) {
	prompt := buildExtractionSystemPrompt("", "", ExtractionCatalog{Tags: []string{"Rechnung"}, SuggestNewTags: true})
	for _, want := range []string{"suggested_tags", "All tags must be given in german."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}

func TestTagsOnlyPromptAsksForNothingElse(t *testing.T) {
	prompt := buildExtractionSystemPrompt("English", "always set a summary", ExtractionCatalog{
		Tags:           []string{"Rechnung"},
		Correspondents: []string{"Acme"},
		TagsOnly:       true,
	})
	for _, want := range []string{`"Rechnung"`, "suggested_tags", "All tags must be given in german."} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
	for _, unwanted := range []string{"summary", "correspondent", "Acme", "document_date"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt mentions %q", unwanted)
		}
	}
}
