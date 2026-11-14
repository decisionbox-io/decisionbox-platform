package discovery

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

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

// figureToken matches a run of digits with the separators a figure may carry.
// Deliberately permissive about which separator means what: a thousands
// separator is a locale setting and the two conventions are each other's
// decimal mark, so the token is compared as written rather than parsed.
var figureToken = regexp.MustCompile(`\d[\d.,]*`)

// claimFigures returns the distinctive numeric tokens of a text: those whose
// digits number three or more.
//
// Three is the floor that makes the containment test mean something. "5%",
// "3x" and "12" appear in almost every finding a project produces, so a test
// built on them matches everything; a figure of three significant digits is
// specific enough that two findings carrying the same one are usually carrying
// the same measurement.
//
// Tokens are returned as written, not normalised to a number. A figure is
// being looked for inside another finding's prose, and that prose contains it
// in whatever form the model wrote it — so the comparison that matters is the
// textual one, and parsing would introduce a locale question (is 12.345 twelve
// thousand or twelve point three) that the comparison does not need to answer.
func claimFigures(s string) []string {
	if s == "" {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, m := range figureToken.FindAllString(s, -1) {
		t := strings.Trim(m, ".,")
		digits := 0
		for _, r := range t {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 3 {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// minContainmentFigures is how many distinctive figures a headline must carry,
// and have found in the other document's body, for containment to be claimed.
//
// Two rather than one. A single shared three-digit number between two findings
// about the same warehouse is coincidence often enough to matter — a row count,
// a year, a store code — and the cost of a wrong fold here is a finding that
// loses its verdict, not just a row that renders collapsed.
const minContainmentFigures = 2

// containedIn reports whether `claim`'s distinctive figures all appear in
// `body`: the test for "this finding's entire claim is already stated inside
// that one".
//
// It exists because similarity is the wrong test for the commonest duplicate
// shape these findings take. A finding whose headline is one ROW of another's
// markdown table is strictly redundant, and because the surrounding prose
// differs it scores 0.71–0.87 — never folded by a 0.88 rule. Of the folds a
// human reviewer applied by hand on one run, four of seven sat below 0.88 and
// two below 0.80. This scan found all of them, and it is a string match: no
// model, no embedding, no measurable cost.
func containedIn(claim, body string) bool {
	figs := claimFigures(claim)
	if len(figs) < minContainmentFigures || body == "" {
		return false
	}
	for _, f := range figs {
		if !strings.Contains(body, f) {
			return false
		}
	}
	return true
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

	name        string
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

// proposePairs runs both cheap tests over every pair of documents and returns
// the direct relations, each tagged with which test fired.
//
// DIRECT relations, never a transitive closure. Containment is not transitive
// either: on real run output, clustering by connected components over the
// containment relation — A contains B, B contains C, therefore one cluster —
// proposed suppressing 23 of 55 insights, where the same data as direct pairs
// yields 9.
func proposePairs(docs []consolidationDoc, threshold float64) map[pairKey]agentplugin.Proposal {
	rel := make(map[pairKey]agentplugin.Proposal)
	for i := range docs {
		for j := i + 1; j < len(docs); j++ {
			var cos, con bool
			if _, ok := cosineFold(docs[j].foldSubject, docs[i].foldSubject, threshold); ok {
				cos = true
			}
			// Both directions: containment is asymmetric, and which member is
			// contained in which is the provider's to weigh, not this scan's.
			if containedIn(docs[j].name, docs[i].body) || containedIn(docs[i].name, docs[j].body) {
				con = true
			}
			switch {
			case cos && con:
				rel[newPairKey(i, j)] = agentplugin.ProposalBoth
			case cos:
				rel[newPairKey(i, j)] = agentplugin.ProposalCosine
			case con:
				rel[newPairKey(i, j)] = agentplugin.ProposalContainment
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
