package bedrock

import (
	"strings"
)

// claudeRegionPrefixes are every cross-region inference profile geo
// qualifier Bedrock has shipped for Anthropic Claude models. AWS adds
// new geos (jp., au.) without coordination with us — listing them
// explicitly here keeps the catalog deterministic and makes new geos
// a one-line addition rather than a runtime heuristic.
var claudeRegionPrefixes = []string{"us.", "eu.", "apac.", "jp.", "au.", "global."}

// claudeVersionSuffixes are the version-qualifier variants users may
// pass alongside an Anthropic Claude model ID on Bedrock.
//
//	"-v1:0" — canonical form Bedrock exposes via ListFoundationModels.
//	"-v1"   — older docs and some console UIs drop the ":0".
//	""      — short form some accounts/regions accept (and the form
//	          Anthropic's own docs list as the AWS Bedrock ID for
//	          Opus 4.7 / 4.6 / Sonnet 4.6).
var claudeVersionSuffixes = []string{"-v1:0", "-v1", ""}

// claudeAliasesFor returns every Bedrock model ID alias that resolves
// to the Anthropic Claude model identified by `family` (e.g.
// "opus-4-7", "sonnet-4-6", "haiku-4-5-20251001",
// "opus-4-20250514"). The canonical ID itself is excluded so callers
// can pass the result straight to ModelEntry.Aliases without
// post-filtering.
//
// Output covers:
//   - Cross-region inference profiles: <prefix>.anthropic.claude-<family><suffix>
//     for every prefix in claudeRegionPrefixes × every suffix in
//     claudeVersionSuffixes (-v1:0, -v1, "").
//   - Bare AWS IDs without a region prefix: anthropic.claude-<family><suffix>
//     (excluding the canonical -v1:0 form, which lives on ModelEntry.ID).
//   - Family-only short forms: claude-<family>, <family>.
//
// Total per Claude family: 7 prefixes (incl. the bare empty prefix) ×
// 3 suffixes = 21 IDs, minus 1 canonical, plus 2 family-only forms =
// 22 unique aliases. Duplicates are filtered defensively.
//
// Panics on an empty family — the catalog seed is a build-time table
// and an empty string here is always a programmer error.
func claudeAliasesFor(family string) []string {
	if family == "" {
		panic("bedrock: claudeAliasesFor called with empty family")
	}
	base := "anthropic.claude-" + family
	canonical := base + "-v1:0"

	seen := map[string]struct{}{canonical: {}}
	out := make([]string, 0, 22)
	emit := func(id string) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	for _, prefix := range append([]string{""}, claudeRegionPrefixes...) {
		for _, suffix := range claudeVersionSuffixes {
			emit(prefix + base + suffix)
		}
	}
	// Family-only short forms (per-user request: "filter it like
	// 'opus' 'sonnet'" — but version-qualified so the right cap
	// resolves; the bare "opus"/"sonnet" stem would be ambiguous).
	emit("claude-" + family)
	emit(family)

	return out
}

// openSourceRegionPrefixes are the cross-region inference profile
// geo qualifiers Bedrock ships for non-Anthropic foundation models
// (Qwen, DeepSeek, Mistral, Meta Llama). The matrix is narrower than
// Claude — no jp./au. profiles for these families today.
var openSourceRegionPrefixes = []string{"us.", "eu.", "apac.", "global."}

// openSourceAliasesFor returns alias variants for a non-Anthropic
// Bedrock model whose canonical ID is `canonical` (e.g.
// "deepseek.r1-v1:0", "meta.llama3-3-70b-instruct-v1:0",
// "qwen.qwen3-next-80b-a3b").
//
// Output covers:
//   - Cross-region inference profiles: <prefix>.<canonical>
//   - Suffix-stripped short form: <publisher>.<body>
//     (without -v1:0 / -v1)
//   - Cross-region + suffix-stripped: <prefix>.<publisher>.<body>
//
// Excludes the canonical ID itself.
func openSourceAliasesFor(canonical string) []string {
	dot := strings.Index(canonical, ".")
	if dot < 0 {
		return nil
	}
	publisher := canonical[:dot]
	body := canonical[dot+1:]
	bodyNoVer := strings.TrimSuffix(body, "-v1:0")
	bodyNoVer = strings.TrimSuffix(bodyNoVer, "-v1")
	shortBare := publisher + "." + bodyNoVer

	seen := map[string]struct{}{canonical: {}}
	out := make([]string, 0, 9)
	emit := func(id string) {
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	if shortBare != canonical {
		emit(shortBare)
	}
	for _, prefix := range openSourceRegionPrefixes {
		emit(prefix + canonical)
		if shortBare != canonical {
			emit(prefix + shortBare)
		}
	}
	return out
}
