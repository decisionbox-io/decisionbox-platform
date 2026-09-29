package discovery

import (
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Which of a repaired insight's figures still stand behind a sentence a reader sees.
//
// Repair runs after the figures are rendered, and it edits text without touching any
// figure. So a figure stating 12 can pass its own check, have its sentence repaired to
// 302, and keep the value 12 -- at which point a recommendation referencing it with no
// declared value adopts 12 and puts the corrected error into a second document. That is
// what the blanket rule was for: a repaired insight lent no figure at all.
//
// The blanket rule cost a falsehood of its own, which is why this file exists. In one
// measured run, repair dropped a single indicator from an insight -- "average spend rises
// monotonically with order count", a sentence carrying no figure reference -- and the
// blanket rule withdrew all eight of that insight's figures from the recommender. All
// eight had held, and an independent oracle confirmed all eight true, including the
// 48,062 a recommendation then needed. Denied an id to name, the model borrowed a
// verified but unrelated 24,999 from another insight, which resolved and shipped as
// `holds` behind a false sentence, and emitted an empty reference for a second figure,
// shipping "{{f1}}" twice. Withholding a true number is not the safe direction when the
// alternative is the model inventing a reference.
//
// So ask the narrower question the audit trail can already answer. The template holds
// the prose as authored, references intact, and the figures hold the values that were
// rendered into it -- correction runs before rendering and repair touches neither. So
// re-rendering the template through the same renderer reproduces, exactly, the text that
// was written into each field. A field that still matches its re-render is a field repair
// did not touch, and every figure referenced only from such fields still says what the
// reader sees.
//
// Fields, not sentences. Repair's unit is the sentence, so a description losing one
// sentence withdraws every figure the description mentions, which is coarser than
// necessary. Going finer would mean locating a figure's sentence inside a paragraph and
// deciding whether the edit reached it -- text surgery, which is what this layer was
// built to get out of, and whose measured failures were all of one shape: the numeral a
// pass thinks it is looking at is not the one it is looking at. Field granularity is
// decided by string equality on text Go itself produced, and it is enough to keep the
// measured case.

// figuresBrokenByRepair names the figures whose link to the prose repair severed.
//
// A figure survives when some field that still matches its re-render references it, and no
// field that changed does. So it is withheld three ways: its field was edited, its field
// was removed, or no field ever referenced it -- the last because a number the model typed
// instead of referencing has no link for this to check, and repair may have just rewritten
// it.
//
// Empty for an insight repair never touched. Every figure for a repaired insight with no
// template: without the authored prose there is no way to ask the question, and the answer
// that withholds is the one that cannot ship a stale number.
func figuresBrokenByRepair(ins models.Insight) map[string]bool {
	if ins.Repair == nil || len(ins.Figures) == 0 {
		return nil
	}
	if ins.FigureTemplate == nil {
		broken := make(map[string]bool, len(ins.Figures))
		for _, f := range ins.Figures {
			broken[f.ID] = true
		}
		return broken
	}

	// The same renderer over the same figures as renderInsightFigures used, so a
	// difference between the two texts is a difference repair made and not a difference
	// in how they were written. usableFigures is part of that: it is the gate the render
	// went through, and re-rendering without it would resolve references the shipped
	// prose left visible.
	var tally figureRenderTally
	r := newFigureRenderer(usableFigures(ins.Figures), &tally)

	// Two sets, and a figure needs to be in the first and out of the second.
	//
	// survived holds the ids a field that still matches its re-render references: the
	// number is on the page, in that sentence, unchanged. changed holds the ids a field
	// repair touched references: that mention is gone or now says something else.
	//
	// A figure in NEITHER set is referenced from no field at all, and it is withheld. That
	// is not symmetry with the unrepaired path, where an unreferenced figure is lent
	// freely; it is the one case that path cannot have. A model may declare a figure and
	// then type its number into the sentence instead of referencing it -- the render still
	// records a template -- and repair editing "12 sub-categories" to "302" leaves that
	// figure with no placeholder to find, no changed reference, and the value 12. Requiring
	// a surviving reference is what makes the audit trail the evidence rather than the
	// absence of evidence.
	//
	// Only ids the render could actually resolve count, in either set. A field holding
	// "{{f1}}" for a figure usableFigures excluded -- no value, a duplicated id, an id the
	// grammar cannot express -- re-renders to the same placeholder the shipped text
	// carries, so the field reads as untouched and the figure as surviving. It is not: the
	// prose never showed a number for it. The prompt would then advertise an id that
	// buildFigureRefIndex separately rejects, which is the "withheld at both ends or
	// neither" rule broken from the other side, and a missing value serialises into the
	// prompt as `value: 0`. Filtering both sets is enough, because an id absent from
	// survived is withheld whether or not it is also in changed.
	usable := make(map[string]bool, len(ins.Figures))
	for _, f := range usableFigures(ins.Figures) {
		usable[f.ID] = true
	}
	survived, changed := map[string]bool{}, map[string]bool{}
	note := func(into map[string]bool, authored string) {
		for _, m := range reFigureRef.FindAllStringSubmatch(authored, -1) {
			if usable[m[1]] {
				into[m[1]] = true
			}
		}
	}
	field := func(authored, shippedText string) {
		if r.render(authored) == shippedText {
			note(survived, authored)
			return
		}
		note(changed, authored)
	}

	tpl := ins.FigureTemplate
	field(tpl.Name, ins.Name)
	field(tpl.Description, ins.Description)
	// Indicators are matched as a multiset rather than by position, because repair
	// removes one and the rest shift up. Two indicators with identical text are
	// consumed one each, so removing one of a duplicated pair is still detected.
	shipped := make(map[string]int, len(ins.Indicators))
	for _, s := range ins.Indicators {
		shipped[s]++
	}
	for _, authored := range tpl.Indicators {
		rendered := r.render(authored)
		if shipped[rendered] > 0 {
			shipped[rendered]--
			note(survived, authored)
			continue
		}
		note(changed, authored)
	}

	// Quantifier claim text is deliberately not consulted. Repair undeclares a claim
	// whose sentence it dropped, so a claim's text is gone by design even when the prose
	// it was drawn from is untouched -- and a declaration is not a sentence a reader
	// sees. Asking here would withhold a figure for the removal of a record, which is
	// precisely the over-withholding this function exists to end.
	//
	// A figure referenced from one surviving field and one changed field stays withheld.
	// The surviving field does still show its value, so lending it would not be wrong --
	// but an insight whose two mentions of one figure disagree is a repair defect, and
	// this is not the place to decide which mention a second document should inherit.
	// Keyed by the id exactly as declared, blank and malformed ones included, because both
	// callers look it up as `broken[f.ID]`. Trimming here and not there meant an id of
	// " f1 " was recorded under "f1", found under neither, and kept -- so the prompt
	// advertised a figure the renderer cannot resolve and reference resolution rejects, and
	// the recommendation shipped a visible placeholder. A blank id was skipped outright,
	// with the same result.
	broken := make(map[string]bool, len(ins.Figures))
	for _, f := range ins.Figures {
		if !survived[f.ID] || changed[f.ID] {
			broken[f.ID] = true
		}
	}
	return broken
}
