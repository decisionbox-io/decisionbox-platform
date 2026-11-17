package discovery

import (
	"errors"
	"fmt"
	"sort"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// This file holds the rules that decide whether two of a run's findings are
// the same finding, and the clustering that turns those pairwise decisions
// into units a provider can adjudicate.
//
// They live here rather than inside Phase 9 because there are now two callers:
// the mid-run consolidation pass, which runs at the end of each analysis area
// BEFORE that area is validated, and Phase 9's end-of-run duplicate check.
// Two copies of "are these the same document" is how the two come to disagree
// about one — the same reason magnitudeOf is one derivation used on both sides
// of every comparison.

// foldSubject is the comparable part of one document, as the shipped pairing
// rule sees it. Both callers project onto it: Phase 9 from its dedupCandidate,
// the mid-run pass from the in-flight insight.
type foldSubject struct {
	id      string
	docType string
	vec     []float64
	// text is what the vector was built from. Two documents with the same
	// text are identical to the search by construction, so whatever the text
	// leaves out is all there is to distinguish them.
	text string
	// magnitude is the signature of what the text omits, from magnitudeOf.
	magnitude string
}

// cosineFold reports whether `later` restates `earlier` under the shipped
// rule, and at what score: same document type, cosine at or above the
// threshold, and the magnitude guard.
//
// The guard is not an optimisation. Two insights with identical prose and
// materially different numbers embed identically and score 1.0, and folding
// the later one hides 100 affected entities behind 1,000 — when a magnitude
// that has MOVED is precisely what the prompt asks the model to report.
func cosineFold(later, earlier foldSubject, threshold float64) (float64, bool) {
	if later.docType != earlier.docType {
		return 0, false
	}
	score, ok := cosineSimilarity(later.vec, earlier.vec)
	if !ok || score < threshold {
		return 0, false
	}
	if !sameClaim(later.text, later.magnitude, earlier.text, earlier.magnitude) {
		return 0, false
	}
	return score, true
}

// cosineFoldTarget is the whole pairing decision, including which document the
// mark should point at — the rule Phase 9 held inline, lifted out so the
// mid-run pass runs the same one.
//
// `canonical` maps an id to the document it has already been folded into,
// already resolved. `byID` looks up a subject by id and reports whether it is
// one of the documents in hand at all.
//
// Returns the target id, the score to record against it, and whether `later`
// folds at all. A false means "not a duplicate of anything the reader can
// see", not "try the next earlier document" — the caller's loop does that.
//
// The chain re-check is why this is not just cosineFold. Cosine is not
// transitive: three vectors at 0°, 25° and 50° score 0.906 between neighbours
// and 0.643 end to end, so matching the middle one says nothing about the
// first. Folding into it anyway would hide a document behind a row it does not
// resemble, under the neighbour's score.
func cosineFoldTarget(
	later, earlier foldSubject,
	threshold float64,
	canonical map[string]string,
	byID func(id string) (foldSubject, bool),
	logPrefix string,
) (string, float64, bool) {
	score, ok := cosineFold(later, earlier, threshold)
	if !ok {
		// Distinguish the magnitude refusal in the log, because it is the one
		// case where a 1.0 score is deliberately not a fold.
		if s, scored := cosineSimilarity(later.vec, earlier.vec); scored && s >= threshold &&
			later.docType == earlier.docType &&
			!sameClaim(later.text, later.magnitude, earlier.text, earlier.magnitude) {
			applog.WithFields(applog.Fields{"id": later.id, "earlier": earlier.id}).
				Info(logPrefix + "same wording, different magnitude; not a duplicate")
		}
		return "", 0, false
	}

	target, targetScore := earlier.id, score
	if resolved, folded := canonical[earlier.id]; folded {
		got, inHand := byID(resolved)
		if !inHand {
			// The canonical is not among the documents in hand — it belongs to
			// an earlier RUN, whose vector this pass does not have. The stored
			// pass already asked the store about this document and said no,
			// which is the only evidence available.
			return "", 0, false
		}
		s, ok := cosineFold(later, got, threshold)
		if !ok {
			// Either the re-check failed or the magnitude guard refused it
			// against the canonical. Without the second half the guard was
			// simply routed around: `later` fails against the canonical
			// directly, matches a paraphrase of it, and is folded in anyway.
			applog.WithFields(applog.Fields{"id": later.id, "canonical": resolved}).
				Info(logPrefix + "does not hold against the resolved canonical; not a duplicate")
			return "", 0, false
		}
		target, targetScore = resolved, s
	}
	if target == "" || target == later.id {
		return "", 0, false
	}
	return target, targetScore, true
}

// consolidationDoc is one of the run's findings as the consolidation pass sees
// it: the comparable part, plus what the provider is shown and what the caller
// needs in order to decide whether a mark may be written at all.
type consolidationDoc struct {
	foldSubject
	// index is position in run order across the whole run so far, so a
	// cluster's candidates can be ordered the way the shipped rule reads
	// them: earliest first.
	index int
	// current is set when this document belongs to the area being
	// consolidated. Only a current document can be marked: an earlier area's
	// finding is already validated and already checkpointed, so a mark on it
	// has nowhere durable to live and would not survive a resume.
	current bool
	// slot is the position of this document in the area's own insight slice,
	// valid only when current.
	slot int

	name string
	// prose is the finding's authored body, markdown rendition preferred.
	// What the provider is shown.
	prose string
	// body is prose plus the supporting figures, which is what the
	// containment test looks inside. Separate from prose because the
	// provider is shown the figures as their own field, and sending them
	// twice is prompt budget spent saying one thing.
	body        string
	area        string
	areaScope   string
	severity    string
	segment     string
	affected    int
	indicators  []string
	metrics     map[string]interface{}
	verdict     string
	duplicateOf string
}

// pairKey is one unordered pair of document indices.
type pairKey struct{ lo, hi int }

func newPairKey(a, b int) pairKey {
	if a > b {
		a, b = b, a
	}
	return pairKey{lo: a, hi: b}
}

// proposePairs returns the pairs worth an adjudication, keyed low-high.
//
// One test: cosine at or above `floor`, which is the ADJUDICATION floor and
// sits below the fold floor on purpose. A pair between the two is a pair the
// model is asked about and the community rule may not fold on its own.
//
// There used to be a second test here. It scanned the numbers written in one
// finding's headline and asked whether all of them appeared in the other's
// body, to catch the commonest duplicate shape — a headline that is one ROW of
// another finding's table, which the surrounding prose keeps at 0.71-0.87 and
// so below any fold floor. The scan read numerals with no idea what they
// measured, and review found six sentences where it took a quantity for a date
// or a date for a quantity; because containment asks whether every figure of
// the claim is in the body, each misread REMOVED a constraint and let two
// different measurements read as one.
//
// That judgement is the model's now. Lowering the floor is what carries those
// pairs to it: the three folds the scan was earning its place for on two field
// runs scored 0.814, 0.831 and 0.858, and a 0.80 floor reaches all three at
// 16-17 adjudications a run.
func proposePairs(docs []consolidationDoc, floor float64) map[pairKey]agentplugin.Proposal {
	rel := make(map[pairKey]agentplugin.Proposal)
	for i := range docs {
		for j := i + 1; j < len(docs); j++ {
			if _, ok := cosineFold(docs[j].foldSubject, docs[i].foldSubject, floor); ok {
				rel[newPairKey(i, j)] = agentplugin.ProposalCosine
			}
		}
	}
	return rel
}

// docCluster is one unit of adjudication: the document indices that make it
// up, in run order, and which tests proposed it.
type docCluster struct {
	members  []int
	proposal agentplugin.Proposal
}

// buildClusters turns the direct relations into stars: one cluster per seed,
// holding the seed and every document directly related to it.
//
// A star rather than a connected component, for the reason proposePairs builds
// pairs. A star rather than a bare pair, because a fold and an unfold are not
// independent edits — repairing one hand-run pass showed that unmarking a
// document so it could serve as canonical left its own twin visible, so two
// claims that had been collapsed became two visible pairs. The set has to be
// solved together, and the write guard then re-verifies each fold against
// whatever canonical came back.
//
// Clusters with no document from the area being consolidated are dropped:
// nothing in them can be marked, so adjudicating them would spend a provider
// call on an answer the caller must refuse. Clusters that are a subset of one
// already emitted are dropped too, so a pair is not adjudicated twice.
func buildClusters(docs []consolidationDoc, rel map[pairKey]agentplugin.Proposal) []docCluster {
	neighbours := make(map[int][]int, len(docs))
	for key := range rel {
		neighbours[key.lo] = append(neighbours[key.lo], key.hi)
		neighbours[key.hi] = append(neighbours[key.hi], key.lo)
	}

	// One candidate star per seed, in seed order.
	sets := make([]map[int]bool, 0, len(docs))
	ordered := make([][]int, 0, len(docs))
	for seed := range docs {
		if len(neighbours[seed]) == 0 {
			continue
		}
		members := append([]int{seed}, neighbours[seed]...)
		sort.Ints(members)
		set := make(map[int]bool, len(members))
		anyCurrent := false
		for _, m := range members {
			set[m] = true
			if docs[m].current {
				anyCurrent = true
			}
		}
		if !anyCurrent {
			continue
		}
		sets = append(sets, set)
		ordered = append(ordered, members)
	}

	// Keep only the maximal stars. A star that is contained in another is the
	// same question asked twice, and asking it twice is how two verdicts come
	// to disagree about one pair — one of which the write guard then has to
	// discard.
	out := make([]docCluster, 0, len(sets))
	for i := range sets {
		maximal := true
		for j := range sets {
			if i == j {
				continue
			}
			// On an exact tie keep the earlier seed's copy, so the result does
			// not depend on which of two identical sets is considered first.
			if len(sets[i]) == len(sets[j]) && j > i {
				continue
			}
			if isSubset(sets[i], sets[j]) {
				maximal = false
				break
			}
		}
		if !maximal {
			continue
		}
		out = append(out, docCluster{members: ordered[i], proposal: mergeProposals(rel, ordered[i])})
	}
	return out
}

// mergeProposals reports which tests fired on the relations INSIDE a cluster.
//
// Inside, and not "on any relation touching a member": a member of a
// containment cluster usually also has similarity relations to documents that
// are not in it, and reading those would report every such cluster as "both"
// — which is the one field that says whether the similarity threshold could
// have seen this duplicate at all.
func mergeProposals(rel map[pairKey]agentplugin.Proposal, members []int) agentplugin.Proposal {
	cos, con := false, false
	for i := 0; i < len(members); i++ {
		for j := i + 1; j < len(members); j++ {
			switch rel[newPairKey(members[i], members[j])] {
			case agentplugin.ProposalCosine:
				cos = true
			case agentplugin.ProposalContainment:
				con = true
			case agentplugin.ProposalBoth:
				cos, con = true, true
			}
		}
	}
	switch {
	case cos && con:
		return agentplugin.ProposalBoth
	case con:
		return agentplugin.ProposalContainment
	default:
		return agentplugin.ProposalCosine
	}
}

func isSubset(a, b map[int]bool) bool {
	if len(a) > len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// errBrokenMark reports a mark that points at a document the run does not
// hold, or a chain of marks with no end.
var errBrokenMark = errors.New("consolidation: a duplicate mark is broken")

// resolveMarkClosure resolves every mark to a FINAL canonical and reports
// which marks had to be re-pointed to get there.
//
// It takes id -> the document that id is marked a duplicate of, and returns id
// -> the final canonical: the document at the end of the chain, which is the
// only target a reader can actually be sent to.
//
// This is one rule rather than a set of pre-checks, learned over three
// attempts at it. Three hand-run passes produced three variants of the same
// bug: a fold whose canonical the same proposal also suppressed; a fold whose
// canonical a previous pass had ALREADY suppressed, which produced a mutual
// cycle so that neither document had a visible canonical; and a pre-existing
// mark pointing at a document this pass suppresses. Each time the case just
// seen was guarded, the next variant appeared. Resolving the whole set
// transitively after writing catches all three and anything else of the shape,
// and a cycle is reported rather than silently broken — a cycle means the
// decision itself was contradictory, and picking a survivor from it here would
// hide which one.
func resolveMarkClosure(marks map[string]string, exists func(id string) bool) (map[string]string, error) {
	out := make(map[string]string, len(marks))
	for id := range marks {
		seen := map[string]bool{id: true}
		target := marks[id]
		for {
			if target == "" {
				return nil, fmt.Errorf("%w: %s points at nothing", errBrokenMark, id)
			}
			if exists != nil && !exists(target) {
				// A target outside the documents in hand is not broken: a
				// cross-run canonical lives in an earlier discovery, and the
				// chain simply ends there.
				break
			}
			next, chained := marks[target]
			if !chained {
				break
			}
			if seen[target] {
				return nil, fmt.Errorf("%w: %s is in a cycle through %s", errBrokenMark, id, target)
			}
			seen[target] = true
			target = next
		}
		if target == id {
			return nil, fmt.Errorf("%w: %s resolves to itself", errBrokenMark, id)
		}
		out[id] = target
	}
	return out, nil
}
