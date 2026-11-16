package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RiskConfigHash fingerprints a pack's RISK CONFIGURATION — the taxonomy it
// declares, and each analysis area's risk categories and weight override.
// Nothing else.
//
// It exists so that a change to those fields is detectable without every
// writer of a pack agreeing to announce it. A counter has to be incremented by
// whoever writes, which means a writer that does not know about the counter
// slips past anything built on it; a fingerprint of the content is true of the
// document however it got that way. It is also NARROWER than a modification
// time: rewriting an area's prompt leaves this value alone, because a prompt
// cannot make a risk mapping invalid.
//
// Two properties the callers depend on:
//
//   - equal hash ⇒ equal risk configuration, for the purpose of deciding
//     whether an edit was built on what is stored now;
//   - stable across a BSON round trip, so a value computed from a document
//     read back out of MongoDB matches the one computed before it was written.
//     That is why weights are formatted with the shortest representation that
//     round-trips rather than printed with %v, and why an absent override is
//     spelled rather than left to collide with a zero one.
//
// Area ORDER is deliberately not part of it. An area's position is not
// something anyone chose, and the areas array is rewritten wholesale by a
// section regeneration — a reordering that changes no risk setting should not
// read as a change. Taxonomy order IS part of it: declaration order is the
// order an insight's risk flags come out in.
func RiskConfigHash(pack *DomainPack) string {
	if pack == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("taxonomy\n")
	for _, cat := range pack.RiskTaxonomy {
		// Label and description are in: an edit to either is a change a
		// concurrent writer should be told about, even though neither affects
		// scoring.
		fmt.Fprintf(&b, "%s\x1f%s\x1f%s\x1f%s\n",
			cat.ID, cat.Label, cat.Description, formatWeight(&cat.Weight))
	}

	// Keyed by where the area lives as well as by its id, because an id is not
	// unique across a pack: the same area id can appear under two categories,
	// and those are two different areas with two different configurations.
	lines := make([]string, 0, len(pack.AnalysisAreas.Base))
	appendArea := func(where string, area PackAnalysisArea) {
		lines = append(lines, fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s",
			where, area.ID, strings.Join(area.RiskCategories, ","), formatWeight(area.SeverityWeight)))
	}
	for _, area := range pack.AnalysisAreas.Base {
		appendArea("base", area)
	}
	for cat, areas := range pack.AnalysisAreas.Categories {
		for _, area := range areas {
			appendArea("category:"+cat, area)
		}
	}
	sort.Strings(lines)
	b.WriteString("areas\n")
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// formatWeight renders a weight for the fingerprint.
//
// 'f' with -1 precision is the shortest decimal that parses back to the same
// float64, so 2.5 is "2.5" and not "2.500000" — and a value that survived a
// BSON round trip formats identically to the one that went in. An absent
// override is "none" rather than "" or "0": absent and zero are different
// instructions everywhere else in this configuration, and a fingerprint that
// conflated them would call two different documents the same.
func formatWeight(w *float64) string {
	if w == nil {
		return "none"
	}
	return strconv.FormatFloat(*w, 'f', -1, 64)
}
