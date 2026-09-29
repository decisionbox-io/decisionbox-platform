package discovery

import (
	"strings"

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
// Empty for an insight repair never touched, and for one where every edited field
// referenced no figure. Every figure for a repaired insight with no template: without the
// authored prose there is no way to ask the question, and the answer that withholds is the
// one that cannot ship a stale number.
func figuresBrokenByRepair(ins models.Insight) map[string]bool {
	if ins.Repair == nil || len(ins.Figures) == 0 {
		return nil
	}
	if ins.FigureTemplate == nil {
		broken := make(map[string]bool, len(ins.Figures))
		for _, f := range ins.Figures {
			if id := strings.TrimSpace(f.ID); id != "" {
				broken[id] = true
			}
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

	broken := make(map[string]bool)
	mark := func(authored string) {
		for _, m := range reFigureRef.FindAllStringSubmatch(authored, -1) {
			broken[m[1]] = true
		}
	}

	tpl := ins.FigureTemplate
	if r.render(tpl.Name) != ins.Name {
		mark(tpl.Name)
	}
	if r.render(tpl.Description) != ins.Description {
		mark(tpl.Description)
	}
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
			continue
		}
		mark(authored)
	}

	// Quantifier claim text is deliberately not consulted. Repair undeclares a claim
	// whose sentence it dropped, so a claim's text is gone by design even when the prose
	// it was drawn from is untouched -- and a declaration is not a sentence a reader
	// sees. Asking here would withhold a figure for the removal of a record, which is
	// precisely the over-withholding this function exists to end.
	return broken
}
