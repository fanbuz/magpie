package usage

import (
	"regexp"
	"strings"
)

// Which model answered: a vendor may serve a request with another model
// than the one asked for — a cheaper one when it's busy — and its reply
// says so in its model field. Most echo the name asked for, or its dated
// or pinned version (gpt-5 as gpt-5-2025-08-07, claude-sonnet-4-5 as
// claude-sonnet-4-5-20250929, gemini-2.5-pro as models/gemini-2.5-pro-001),
// which is the same model.

// versionTail is what a vendor puts after a model's name for the version
// it answered with: a date, a build number, Bedrock's v1:0, latest,
// preview. Not v3 alone: deepseek-v3 is another model than deepseek-v2.
var versionTail = regexp.MustCompile(`(?:[-_@:](?:\d{4}-\d{2}-\d{2}|\d{2}-\d{2}|\d{6,8}|\d{3,4}|v\d+:\d+|latest|preview|exp))+$`)

// vendorDot is Bedrock's region and maker before a model's name
// (us.anthropic.claude-…).
var vendorDot = regexp.MustCompile(`^(?:[a-z]{2,4}\.)?(?:anthropic|amazon|meta|mistral|cohere|ai21|deepseek|qwen|openai|google|moonshotai|minimax|zai)\.`)

// contextTail is what Claude Code writes after a model's name for the size of
// its context: claude-opus-5[1m].
var contextTail = regexp.MustCompile(`\[[^\]]*\]$`)

// bareModel is a model's name without its maker or path, its version, the
// size of its context or its case.
func bareModel(m string) string {
	m = contextTail.ReplaceAllString(strings.ToLower(strings.TrimSpace(m)), "")
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	m = vendorDot.ReplaceAllString(m, "")
	if loc := versionTail.FindStringIndex(m); loc != nil && loc[0] > 0 {
		m = m[:loc[0]]
	}
	return m
}

// Swapped reports whether served is another model than sent: not the same
// name, however dated, pinned or prefixed.
func Swapped(sent, served string) bool {
	a, b := bareModel(sent), bareModel(served)
	return a != "" && b != "" && a != b
}
