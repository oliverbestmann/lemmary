package ai

const tagLanguagePrompt = `

All tags must be given in german.`

// title is asked for only because the shared reply parser rejects an answer
// without one; the caller of a tags-only extraction ignores it.
func buildTagsOnlyPrompt(catalog ExtractionCatalog) string {
	prompt := `You assign tags to a document from its OCR text.
Return ONLY valid JSON with these fields:
- title (string, always exactly "-")
- tags (array of strings, chosen only from the existing tags list below)
- suggested_tags (array of strings): up to 3 short new tag names that fit this document and are NOT in the existing tags list; return an empty array when the existing tags already cover the document`
	prompt += formatAllowedTagsPrompt(catalog.Tags)
	prompt += tagLanguagePrompt
	prompt += `

Do not include markdown or explanation.`
	return prompt
}
