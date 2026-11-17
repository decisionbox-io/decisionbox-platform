package discovery

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The mid-run consolidation pass. It runs at the end of each analysis area,
// after the area's insights have been analysed, repaired and figure-settled,
// and BEFORE that area is validated.
//
// Why before validation rather than at Phase 9, where the duplicate check
// already lives: validateInsights charges one run-wide counter against
// MaxInsightsPerRun while Phase 4.5 runs inside the area loop, so the first
// areas spend the budget and every area after them is stamped
// skipped_budget_cap — and filterEligibleInsights then drops a capped insight
// from recommendation generation too, because skipped_budget_cap is neither
// validation_disabled nor in the eligibility set. Phase 9 detects the
// duplicates at 97% progress, by which time the budget has been spent on them
// and the recommender has already made its selection.
//
// Measured on four consecutive runs of one project: within-run restatement is
// 7% on a project with no history and 43-48% once there is any, which is a
// step rather than a ramp. The ten analysis areas are prompted independently,
// so two of them reporting the same claim is the dominant duplicate shape, and
// nothing about the prior-context budgeting touches it.

// consolidationEnabledEnv turns the mid-run pass off.
//
// It exists because this is the one change in the feature that can cost a
// finding something: a false fold used to hide a row behind an expandable
// canonical, and after this it also costs that insight its verdict and its
// place in recommendation generation. The existing similarity floor is a
// threshold, not a switch — it cannot express "do not consolidate at all",
// and the containment test does not read it — so without this an operator who
// wanted the previous behaviour back had nothing to set.
//
// Default ON: the measured case for the feature is that half of a run's
// findings restate another one, and leaving it off by default would mean the
// budget keeps being spent on them until someone opts in.
const consolidationEnabledEnv = "DISCOVERY_CONSOLIDATION_ENABLED"

// consolidationEnabled resolves that switch.
func consolidationEnabled() bool {
	return goconfig.GetEnvAsBool(consolidationEnabledEnv, true)
}

// consolidationMaxClustersEnv bounds how many clusters one area sends to the
// adjudicator, and so bounds what the pass can cost.
//
// It exists because nothing else does. Each call's prompt is bounded by the
// layer's input window, but the NUMBER of calls grows with the cluster count,
// so a pathological area — many findings, all overlapping — could spend
// several times what the analysis call that produced them did. The feature's
// own acceptance target is under 1% of a run's tokens.
//
// Clusters past the bound are not dropped: they fall through to the community
// rule, which is the outcome the product has today. The bound is deliberately
// generous — four measured runs proposed about thirty candidate pairs per RUN,
// so sixty per area is far above anything observed — and it is logged when it
// bites, because an area that reaches it is either a real finding about the
// playbook or a bug in the proposal step.
const consolidationMaxClustersEnv = "DISCOVERY_CONSOLIDATION_MAX_CLUSTERS"

const defaultConsolidationMaxClusters = 60

// consolidationMaxClusters resolves that bound. A non-positive setting means
// no bound, which is a deliberate operator choice rather than a typo to
// second-guess: the pass is already bounded per call by the input window.
func consolidationMaxClusters() int {
	return goconfig.GetEnvAsInt(consolidationMaxClustersEnv, defaultConsolidationMaxClusters)
}

// consolidateAreaInput is what consolidating one area needs.
type consolidateAreaInput struct {
	// area is the area being consolidated.
	area AnalysisArea
	// prompts is the resolved prompt set, read only for the area scope text
	// handed to the provider.
	prompts ResolvedPrompts
	// areaByID lets an earlier area's document carry its own scope text.
	areaByID map[string]AnalysisArea
	// insights is the area's own insight slice, marked IN PLACE. It aliases
	// the analysis step's Insights, so a mark written here is in the area
	// draft the next line checkpoints.
	insights []models.Insight
	// runInsights is what the areas BEFORE this one reported: candidates and
	// canonicals, never marked. See consolidationDoc.current.
	runInsights []models.Insight
}

// consolidationStats is one area's pass, for the run log. Counted by source so
// a regression in fold rate is visible without a replay — a false fold costs
// more after this change than before it (the insight also loses its verdict
// and its place in Phase 5), so the rate is the thing to watch.
type consolidationStats struct {
	Area               string
	Documents          int
	Clusters           int
	Folded             int
	ByCosine           int
	ByContainment      int
	ByBoth             int
	Decider            string
	RefusedNotMine     int
	RefusedRecheck     int
	RefusedShape       int
	RefusedUnmergeable int
	RefusedKept        int
	Kept               int
	OverBound          int
	Reverted           bool
	DurationMs         int64
}

// consolidateArea proposes the duplicate clusters among an area's fresh
// insights — against each other and against the run's earlier areas — asks the
// registered provider to adjudicate them, and stamps duplicate_of on the
// area's own insights.
//
// It marks; it never deletes. The insight is still persisted, still embedded,
// still rendered and still expandable behind its canonical, which is the
// non-destructive contract the end-of-run check already keeps.
func (o *Orchestrator) consolidateArea(ctx context.Context, in consolidateAreaInput) consolidationStats {
	started := time.Now()
	st := consolidationStats{Area: in.area.ID, Decider: "none"}
	if len(in.insights) == 0 || !consolidationEnabled() {
		return st
	}
	foldFloor := documentDedupMinScore()
	adjFloor := adjudicationMinScore()
	docs := o.consolidationDocs(ctx, in)
	st.Documents = len(docs)
	if len(docs) < 2 {
		return st
	}

	clusters := buildClusters(docs, proposePairs(docs, adjFloor))
	st.Clusters = len(clusters)

	// No early return on an empty cluster set. The community rule reads the
	// FOLD floor, and the two floors are independent: an operator who raises
	// the adjudication floor above it leaves pairs that are foldable and never
	// proposed, and returning here would let those spend the validation budget
	// the pass exists to protect. Adjudication is what is skipped when there is
	// nothing to adjudicate; the pass is not.
	var (
		verdicts   map[int]agentplugin.ConsolidationVerdict
		byProvider map[int]bool
	)
	if len(clusters) > 0 {
		// What goes to the adjudicator, as against what the community rule
		// decides. See consolidationMaxClustersEnv.
		order := make([]int, len(clusters))
		for i := range order {
			order[i] = i
		}
		// Ranked only when the cap actually bites AND a provider is there to
		// receive the slots: with none registered every cluster takes the
		// community rule whatever the order, and ranking a large area cost
		// seconds to change nothing.
		if bound := consolidationMaxClusters(); bound > 0 && len(clusters) > bound &&
			agentplugin.HasConsolidationProvider() {
			// Slots go to the clusters where a verdict changes the outcome. A
			// cluster holding a pair the community rule could fold gets folded
			// if the provider never sees it — possibly against what the
			// provider would have said — while a cluster with no such pair
			// loses only a fold that would not have happened.
			//
			// Each cluster's answer is computed ONCE. Asking it inside the
			// comparator re-walked every pair of every overlapping star on
			// each comparison: 0.12s to 4.7s on 200 findings, for the same
			// marks.
			foldable := make(map[pairKey]bool)
			writable := make([]bool, len(clusters))
			for i := range clusters {
				writable[i] = clusterCanAutoFold(docs, clusters[i], foldFloor, foldable)
			}
			sort.SliceStable(order, func(x, y int) bool {
				return writable[order[x]] && !writable[order[y]]
			})
		}
		if bound := consolidationMaxClusters(); bound > 0 && len(order) > bound {
			st.OverBound = len(order) - bound
			order = order[:bound]
			applog.WithFields(applog.Fields{
				"area": st.Area, "clusters": len(clusters), "bound": bound,
				"to_community_rule": st.OverBound, "env": consolidationMaxClustersEnv,
			}).Warn("Consolidation: this area proposed more clusters than the adjudication bound; the rest fall to the community rule")
		}
		adjudicable := make([]docCluster, len(order))
		for i, oi := range order {
			adjudicable[i] = clusters[oi]
		}

		var decider string
		verdicts, byProvider, decider = o.adjudicate(ctx, docs, adjudicable, order, adjFloor)
		st.Decider = decider
	}
	st = o.applyConsolidation(in, docs, clusters, verdicts, byProvider, foldFloor, adjFloor, st)
	// An area can write community folds with no cluster adjudicated at all,
	// once the two floors can be set apart. Decider is assigned where the
	// adjudication happens, so the summary reported "none" beside a positive
	// fold count and gave an operator no way to tell what had decided them.
	if st.Decider == "none" && st.Folded > 0 {
		st.Decider = "community"
	}
	st.DurationMs = time.Since(started).Milliseconds()

	applog.WithFields(applog.Fields{
		"area": st.Area, "documents": st.Documents, "clusters": st.Clusters,
		"folded": st.Folded, "by_cosine": st.ByCosine, "by_containment": st.ByContainment,
		"by_both": st.ByBoth, "decider": st.Decider,
		"refused_other_area": st.RefusedNotMine, "refused_recheck": st.RefusedRecheck,
		"refused_shape": st.RefusedShape, "refused_unmergeable": st.RefusedUnmergeable,
		"refused_kept": st.RefusedKept,
		"kept":         st.Kept, "over_bound": st.OverBound, "reverted": st.Reverted,
		"duration_ms": st.DurationMs,
	}).Info("Consolidation: area complete")
	return st
}

// consolidationDocs projects the run's insights onto what the pass compares,
// earlier areas first and in run order, and resolves the vectors.
//
// Vectors come from the orchestrator's cache, which is filled here and read
// again by Phase 9 — so a run embeds each insight exactly once and the
// embedding spend does not change. A missing embedding provider is not an
// error: cosineFold returns no match for an empty vector, so the pass degrades
// to the containment test alone, which needs no model at all.
func (o *Orchestrator) consolidationDocs(ctx context.Context, in consolidateAreaInput) []consolidationDoc {
	docs := make([]consolidationDoc, 0, len(in.runInsights)+len(in.insights))
	add := func(ins *models.Insight, current bool, slot, runSlot int) {
		if ins.ID == "" {
			return
		}
		area := in.areaByID[ins.AnalysisArea]
		docs = append(docs, consolidationDoc{
			foldSubject: foldSubject{
				id:        ins.ID,
				docType:   "insight",
				text:      commonmodels.InsightEmbeddingText(ins.Name, ins.Description, ins.AnalysisArea, ins.Severity, ins.TargetSegment),
				magnitude: magnitudeOf("insight", ins.AffectedCount, ins.Metrics, nil, ins.Indicators),
			},
			index:       len(docs),
			current:     current,
			slot:        slot,
			runSlot:     runSlot,
			name:        ins.Name,
			prose:       insightProse(ins),
			area:        ins.AnalysisArea,
			areaScope:   areaScopeText(area, in.prompts.AnalysisAreas[ins.AnalysisArea]),
			severity:    ins.Severity,
			segment:     ins.TargetSegment,
			affected:    ins.AffectedCount,
			indicators:  ins.Indicators,
			metrics:     ins.Metrics,
			verdict:     verdictOf(ins),
			duplicateOf: ins.DuplicateOf,
			stands:      ins.StandsOnAdjudication,
		})
	}
	for i := range in.runInsights {
		add(&in.runInsights[i], false, -1, i)
	}
	for i := range in.insights {
		add(&in.insights[i], true, i, -1)
	}
	o.fillInsightVectors(ctx, docs)
	return docs
}

// fillInsightVectors resolves each document's vector from the run's cache,
// embedding only the texts the cache does not already hold.
func (o *Orchestrator) fillInsightVectors(ctx context.Context, docs []consolidationDoc) {
	if o.embeddingProvider == nil {
		return
	}
	missing := make([]string, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for i := range docs {
		t := docs[i].text
		if t == "" || seen[t] || o.cachedInsightVector(t) != nil {
			continue
		}
		seen[t] = true
		missing = append(missing, t)
	}
	if len(missing) > 0 {
		vecs, err := o.embeddingProvider.Embed(ctx, missing)
		switch {
		case err != nil:
			// Non-fatal, but it does cost the pass: similarity is the only
			// test now, so a finding with no vector is proposed in no pair and
			// a cold cache means no consolidation at all. Said plainly,
			// because the visible symptom is restatements reaching validation
			// and spending its budget, and an operator reading this log is the
			// only one who can connect the two.
			applog.WithError(err).WithField("texts", len(missing)).
				Warn("Consolidation: embedding failed; these findings are left out of consolidation and their restatements will reach validation")
		case len(vecs) != len(missing):
			applog.WithFields(applog.Fields{"want": len(missing), "got": len(vecs)}).
				Warn("Consolidation: embedding count mismatch; these findings are left out of consolidation and their restatements will reach validation")
		default:
			for i, t := range missing {
				o.cacheInsightVector(t, vecs[i])
			}
		}
	}
	for i := range docs {
		docs[i].vec = o.cachedInsightVector(docs[i].text)
	}
}

// insightProse is the authored body alone, markdown rendition preferred.
func insightProse(ins *models.Insight) string {
	if ins.DescriptionMd != "" {
		return ins.DescriptionMd
	}
	return ins.Description
}

// verdictOf renders the verdict a document already carries, or "".
func verdictOf(ins *models.Insight) string {
	if ins.Validation == nil {
		return ""
	}
	if ins.Validation.Combined != "" {
		return string(ins.Validation.Combined)
	}
	return ins.Validation.Status
}

// areaScopeNotThisArea is the heading a project's analysis-area prompt may use
// to state what that area does NOT own.
//
// Read as a convention rather than required: ownership predicts the right
// canonical better than document order does, and when a project has written
// its area boundaries down, handing them to the adjudicator is better than
// asking it to infer them from an area id. A project that has not written one
// simply supplies the area's description.
const areaScopeNotThisArea = "## Not This Area"

// areaScopeText renders one area's ownership for the adjudicator: its own
// description, plus the boundary block its prompt declares, if any.
func areaScopeText(area AnalysisArea, prompt string) string {
	parts := make([]string, 0, 2)
	if d := strings.TrimSpace(area.Description); d != "" {
		parts = append(parts, d)
	}
	if block := sectionAfterHeading(prompt, areaScopeNotThisArea); block != "" {
		parts = append(parts, areaScopeNotThisArea+"\n"+block)
	}
	return strings.Join(parts, "\n\n")
}

// sectionAfterHeading returns the body under a markdown heading, up to the
// next heading of the same or a higher level, or "" when the heading is
// absent.
func sectionAfterHeading(text, heading string) string {
	idx := strings.Index(text, heading)
	if idx < 0 {
		return ""
	}
	rest := text[idx+len(heading):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		return ""
	}
	lines := strings.Split(rest, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "## ") {
			break
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// adjudicate asks the registered provider to decide the clusters, and falls
// back to the community rule for every cluster the provider did not answer.
//
// The community rule is Phase 9's rule, unchanged: the earliest member is
// canonical, and a later member is folded into it only when the COSINE pair
// holds between the two. A containment-only cluster is left standing by the
// fallback, which is what keeps a deployment with no provider on exactly the
// outcome it has today — only the moment the decision is made moves.
// `order` maps each position in `clusters` back to its index in the caller's
// own slice. The cap may reorder and truncate before calling, and every map
// this returns is read by the caller's indices — keying them by position here
// attributed a verdict to the wrong cluster, which a reordered cap reproduced
// as the provider keeping a pair distinct while the community rule folded it.
func (o *Orchestrator) adjudicate(ctx context.Context, docs []consolidationDoc, clusters []docCluster, order []int, threshold float64) (map[int]agentplugin.ConsolidationVerdict, map[int]bool, string) {
	out := make(map[int]agentplugin.ConsolidationVerdict, len(clusters))
	// Which verdicts came from the PROVIDER rather than the fallback. It
	// decides whether a member the verdict left out is a judgement to honour
	// or simply the community rule declining to fold — see keepStanding.
	byProvider := make(map[int]bool, len(clusters))
	decider := "community"

	if agentplugin.HasConsolidationProvider() {
		payload := make([]agentplugin.DuplicateCluster, 0, len(clusters))
		for _, c := range clusters {
			payload = append(payload, projectCluster(docs, c))
		}
		verdicts, err := agentplugin.ResolveConsolidation(ctx, o.projectID, payload)
		switch {
		case err != nil:
			applog.WithError(err).WithField("provider", agentplugin.ConsolidationProviderName()).
				Warn("Consolidation: the provider failed; falling back to the community rule")
		case len(verdicts) > 0:
			decider = agentplugin.ConsolidationProviderName()
			// By position: the provider is handed the clusters in order and
			// answers in order. A short answer leaves the rest to the
			// fallback; a longer one is truncated, because a verdict with no
			// cluster has no candidate set to validate it against.
			//
			// An empty CanonicalID is "I did not decide this one", not a
			// malformed verdict: a provider that answers some clusters and
			// not others has to be able to say so without its gaps being
			// counted as errors. Those fall through to the community rule
			// below.
			for i := range verdicts {
				if i >= len(clusters) {
					break
				}
				if verdicts[i].CanonicalID == "" {
					continue
				}
				out[order[i]] = verdicts[i]
				byProvider[order[i]] = true
			}
		}
	}

	return out, byProvider, decider
}

// projectCluster renders one cluster for the provider.
func projectCluster(docs []consolidationDoc, c docCluster) agentplugin.DuplicateCluster {
	cands := make([]agentplugin.ConsolidationDoc, 0, len(c.members))
	var seed consolidationDoc
	if len(c.members) > 0 {
		seed = docs[c.members[0]]
	}
	for _, m := range c.members {
		d := docs[m]
		score, _ := cosineSimilarity(d.vec, seed.vec)
		cands = append(cands, agentplugin.ConsolidationDoc{
			ID:            d.id,
			Name:          d.name,
			Description:   d.prose,
			AnalysisArea:  d.area,
			Severity:      d.severity,
			TargetSegment: d.segment,
			AffectedCount: d.affected,
			Indicators:    d.indicators,
			Metrics:       anyMap(d.metrics),
			Score:         score,
			EarlierArea:   !d.current,
			Verdict:       d.verdict,
			AreaScope:     d.areaScope,
		})
	}
	return agentplugin.DuplicateCluster{Candidates: cands, Proposal: c.proposal}
}

func anyMap(in map[string]interface{}) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// communityFold is one fold the community rule would make: where the mark
// points and at what score.
type communityFold struct {
	target string
	score  float64
}

// communityFolds is the community rule, as ONE pass over the run in document
// order.
//
// Per cluster was wrong, and the clusters themselves are the reason: they are
// overlapping stars, so a per-cluster pass carries state that depends on which
// star was processed first. On two overlapping containment stars whose middle
// members are all similar to each other, the first star made one of them a
// canonical and the second then refused to fold it into the earlier member it
// actually restates — two redundant findings left standing, and the validation
// budget spent on both.
//
// The decision also has to MATCH what the end-of-run check would have made,
// because the promise this phase makes to a community deployment is that only
// the MOMENT of the decision moves. That check is one global pass, so this is
// one global pass: markDuplicates' same-run rule over the documents in hand —
// document order, cosine at the configured floor, the magnitude guard and the
// canonical-chain re-check, all of it through the one shared function.
// The chain is built only from folds this pass could actually WRITE.
//
// Simulating a fold that cannot be written is how the chain comes to describe
// a run nobody will see: with A and B kept apart by an adjudicator, recording
// B -> A anyway made a later document's chain re-check resolve against A — a
// canonical the reader is NOT being sent to — so a document that plainly
// restates the visible B was left standing and spent a validation check,
// before the end-of-run rule folded it into B regardless.
func (o *Orchestrator) communityFolds(
	docs []consolidationDoc,
	threshold float64,
	marks map[string]string,
	dependants map[string]int,
) map[string]communityFold {
	byID := make(map[string]int, len(docs))
	for i := range docs {
		byID[docs[i].id] = i
	}
	lookup := func(id string) (foldSubject, bool) {
		i, ok := byID[id]
		if !ok {
			return foldSubject{}, false
		}
		return docs[i].foldSubject, true
	}

	// Seeded with the marks that are actually standing — the ones the
	// documents arrived with AND the ones the provider verdicts just wrote —
	// so a match against a folded document resolves to the canonical a reader
	// can see.
	//
	// Reading the arrival marks alone was a real defect, not a tidiness
	// point: with the provider folding B into the later C and declining the
	// overlapping cluster, the simulation still saw B as visible and folded C
	// into it, and a later D then failed its re-check against B — while C,
	// which D plainly restates, was visible the whole time. D spent a
	// validation check and could reach Phase 5 before the end-of-run rule
	// folded it into C anyway.
	//
	// A COPY, because this is a simulation: nothing here may write a mark.
	canonical := make(map[string]string, len(docs)+len(marks))
	for id, target := range marks {
		canonical[id] = target
	}

	out := make(map[string]communityFold, len(docs))
	for i := range docs {
		// A document this pass cannot mark contributes nothing to the chain
		// either: its mark would never be written, so a later document must
		// not resolve through it. Four ways that happens — it belongs to an
		// earlier area, it already carries a mark, this pass has already sent
		// a reader to it, or an adjudicator said it must stand.
		//
		// The last one only as a SOURCE: a protected finding stays visible,
		// which makes it a perfectly good survivor for a later document to
		// fold into, and the inner loop below is free to choose it. What it
		// must not do is record a fold OF it — the writer refuses that, and
		// a chain that says otherwise then rejects the next document against
		// a canonical nobody is being sent to, which costs that document a
		// validation check and leaves the duplicate visible.
		if !docs[i].current || docs[i].stands || marks[docs[i].id] != "" || dependants[docs[i].id] > 0 {
			continue
		}
		for j := 0; j < i; j++ {
			target, score, ok := cosineFoldTarget(
				docs[i].foldSubject, docs[j].foldSubject, threshold, canonical, lookup, "Consolidation: ")
			if !ok {
				continue
			}
			if o.adjudicatedKeep(docs[i].id, target) {
				// Kept apart by an adjudicator. Not a fold, and so not a
				// link in the chain.
				continue
			}
			out[docs[i].id] = communityFold{target: target, score: score}
			canonical[docs[i].id] = target
			break
		}
	}
	return out
}

// communityFoldAt is one community fold with the document it applies to.
type communityFoldAt struct {
	at     int
	target string
	score  float64
}

// communityFoldOrder puts the community folds back into document order, which
// is the order they have to be applied in: a map's iteration is randomised,
// and applying a chain's folds out of order makes which document ends up
// carrying the mark depend on the shuffle.
func communityFoldOrder(docs []consolidationDoc, folds map[string]communityFold) []communityFoldAt {
	out := make([]communityFoldAt, 0, len(folds))
	for i := range docs {
		if f, ok := folds[docs[i].id]; ok {
			out = append(out, communityFoldAt{at: i, target: f.target, score: f.score})
		}
	}
	return out
}

// applyConsolidation writes the verdicts the guards allow, then proves the
// result: every mark in the run resolved transitively to a final canonical,
// re-verified, and the whole area's marks reverted if any of it is broken.
func (o *Orchestrator) applyConsolidation(
	in consolidateAreaInput,
	docs []consolidationDoc,
	clusters []docCluster,
	verdicts map[int]agentplugin.ConsolidationVerdict,
	byProvider map[int]bool,
	threshold float64,
	adjFloor float64,
	st consolidationStats,
) consolidationStats {
	// Pairs of donors whose folds a verdict proposed and the re-check refused.
	// Protected from each other only once every verdict has been read: a pair
	// one verdict could not fold may be one another folds outright, and that
	// decision has to win over this protection.
	var donorKeeps [][2]string
	// Pairs a rewrite verdict kept, remembered so they can be re-resolved
	// against the marks every verdict ends up writing.
	var rewritePairs [][2]string
	byID := make(map[string]int, len(docs))
	for i := range docs {
		byID[docs[i].id] = i
	}
	// Every mark the run holds, this pass's writes included, so the closure
	// check below sees the same set a reader would.
	marks := make(map[string]string, len(docs))
	for i := range docs {
		if docs[i].duplicateOf != "" {
			marks[docs[i].id] = docs[i].duplicateOf
		}
	}
	// Every pair an adjudicator decided should stand, and every finding it
	// said must stand on its own, registered BEFORE a single mark is written.
	//
	// Incrementally was wrong, and the shape is worth stating: the clusters
	// are overlapping stars processed in order, so a cluster the provider
	// DECLINED can be processed first and its community fallback folds a pair
	// that a provider verdict for a later, overlapping cluster explicitly
	// keeps. Discovered after the fact, the keep is useless — the mark is
	// already written, and the end-of-run check will not revisit a document
	// that carries one. A pair's keep is derivable from the verdict alone, so
	// it does not need the marks and can be known first.
	o.registerAdjudicatedKeeps(docs, clusters, verdicts, byProvider, byID, marks, in, &st)

	// How many of this pass's marks each document is the target of. Marking
	// a document that has any would build a chain — the closure check would
	// catch it, but refusing here keeps the pass's own output chain-free by
	// construction.
	//
	// A COUNT rather than a flag, because a cluster's folds can be given
	// back: a document that was briefly a canonical and then lost its only
	// dependant has none, and a flag left it protected for the rest of the
	// pass, refusing a later valid fold of it as though a reader were still
	// being sent there.
	dependants := make(map[string]int, len(clusters))
	// Canonicals whose description this pass has already rewritten. Clusters
	// are overlapping stars, so two verdicts can legitimately fold different
	// members into the same survivor — and each merged description REPLACES
	// the survivor's text, so applying the second would erase the detail the
	// first one preserved while both folds stand. The first rewrite wins and
	// the second is reported: it was written against text the model had seen,
	// and the text has since changed.
	written := make([]int, 0, len(docs))

	for i, c := range clusters {
		member := make(map[string]bool, len(c.members))
		for _, m := range c.members {
			member[docs[m].id] = true
		}
		v, ok := verdicts[i]
		if !ok {
			continue
		}
		final, fi, bad := verdictCanonical(v, member, byID, marks)
		if bad != "" {
			applog.WithFields(applog.Fields{"area": st.Area, "canonical": v.CanonicalID, "reason": bad}).
				Info("Consolidation: a verdict's canonical does not hold; cluster discarded")
			st.RefusedShape++
			continue
		}
		if strings.TrimSpace(v.MergedDescription) != "" {
			// A rewrite is asked for precisely when a folded member carries a
			// figure, a segment or a caveat the survivor does not — the
			// provider saying so in the only way the contract gives it.
			// Nothing applies a rewrite now, and applying the folds without
			// it suppresses exactly those findings behind text that does not
			// state them: the failure this phase exists to avoid. The folds
			// were given back for this reason before, whenever a rewrite
			// could not be applied.
			//
			// Both findings stand, and each pair is remembered as adjudicated
			// so the fallback does not fold it on document order instead.
			donors := make([]string, 0, len(v.Duplicates))
			for dupID := range v.Duplicates {
				// Only ids this cluster holds. The per-donor loop below does
				// this for a fold; a keep needs it for the same reason — a
				// verdict naming a member of some other cluster would
				// otherwise block a fold of two documents it never saw.
				if !member[dupID] || dupID == final {
					continue
				}
				if o.keepPair(dupID, final) {
					st.Kept++
				}
				// The protection on the finding itself is registered by the
				// pre-pass above, before any mark is written. Here was too
				// late: the clusters are overlapping stars read in order, so
				// an earlier verdict can fold this donor into its own
				// canonical before this one asks for a rewrite to carry the
				// donor's caveat — and the flag then arrives on a finding
				// that is already suppressed.
				donors = append(donors, dupID)
				// Remembered so the pair can be re-resolved once every
				// verdict has written its marks: this canonical may itself be
				// folded by a later verdict, and the protection has to follow
				// it. Without that, a rewrite keeping C away from B left C
				// unprotected from the A that B then folded into.
				rewritePairs = append(rewritePairs, [2]string{dupID, final})
			}
			// And from each other. The adjudicator said each of them carries
			// detail the survivor does not; folding one into another would
			// suppress that detail just as surely, and two donors of one
			// verdict are related only through a canonical none of them
			// reached.
			sort.Strings(donors)
			for x := 0; x < len(donors); x++ {
				for y := x + 1; y < len(donors); y++ {
					donorKeeps = append(donorKeeps, [2]string{donors[x], donors[y]})
				}
			}
			applog.WithFields(applog.Fields{
				"area": st.Area, "canonical": final, "donors": len(v.Duplicates),
			}).Info("Consolidation: this verdict asked for a rewrite to carry its donors' detail; nothing rewrites a survivor, so its folds were left standing")
			st.RefusedUnmergeable++
			continue
		}

		// The members this verdict named as duplicates whose fold did not get
		// written. They matter afterwards: the verdict meant all of them to
		// collapse into one survivor, so it never said anything about whether
		// they restate EACH OTHER — and with no mark on any of them the
		// community rule is free to fold one into another sideways, losing the
		// detail the verdict was collapsing. Protected from each other below.
		refusedDonors := make([]string, 0, len(v.Duplicates))

		// Sorted, so two runs over the same verdict write the same marks.
		dupIDs := make([]string, 0, len(v.Duplicates))
		for id := range v.Duplicates {
			dupIDs = append(dupIDs, id)
		}
		sort.Strings(dupIDs)

		for _, dupID := range dupIDs {
			if !member[dupID] || dupID == final {
				st.RefusedShape++
				continue
			}
			di, inHand := byID[dupID]
			if !inHand {
				st.RefusedShape++
				continue
			}
			dup := docs[di]
			if dup.duplicateOf != "" || marks[dupID] != "" {
				// Already folded, by a previous area's pass or by an earlier
				// cluster of this one. Not re-decided: that mark was made with
				// the same judgement and re-pointing it could send the reader
				// somewhere else.
				//
				// It still counts as a refused donor, because a rewrite naming
				// it credits the survivor with its detail while mergeSourceSteps
				// carries only what this verdict actually folded — so the
				// survivor would state a claim whose evidence it does not cite.
				continue
			}
			if dependants[dupID] > 0 {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID}).
					Info("Consolidation: refusing to suppress a document this pass already made canonical")
				st.RefusedShape++
				continue
			}
			if !dup.current {
				// An earlier area's finding. Its verdict is already paid for
				// and its mark has nowhere durable to live — the area's
				// checkpoint is written and its insight ids are reminted on
				// restore — so suppressing it here would not survive a resume.
				// Left standing, and remembered so the end-of-run check does
				// not fold it the other way round on this run.
				o.keepPair(dupID, final)
				st.RefusedNotMine++
				continue
			}
			// The adjudicator decided this pair stands, in some other cluster
			// of this same pass. A fallback verdict for an overlapping
			// cluster must not override it: the clusters are stars and they
			// share members, so the same pair can appear in one cluster the
			// provider answered and another it declined — and the end-of-run
			// check cannot correct it afterwards, because it skips a document
			// that already carries a mark.
			if o.adjudicatedKeep(dupID, final) {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID, "canonical": final}).
					Info("Consolidation: this pair was already adjudicated and left standing; not folding it in another cluster")
				st.RefusedKept++
				continue
			}
			// Considered and not done: gating this on the PAIR's own score
			// against the adjudication floor. With the floors at their
			// defaults the proposal floor is the lower of the two, so a pair
			// inside a star that never cleared it is refused here anyway. With
			// an operator's floors inverted — adjudication above fold — a pair
			// can enter a star through a shared neighbour at a score between
			// them, and the fold is then accepted at the fold floor: which is
			// the floor at which this product folds without a model at all, so
			// accepting it is not a weaker decision than the alternative.
			//
			// `byProvider[i] && final == v.CanonicalID`, not byProvider alone.
			// The adjudication floor is an acceptance the provider earned by
			// ruling on THIS pair, and a resolved target is not always the one
			// it named: when it folds C into B and B already points at A, the
			// chain sends C to A, which the provider may never have been shown.
			// Accepting that at the lower floor folds a pair on no verdict —
			// measured at 0.829 in review, below the floor the community rule
			// would have required. A redirected target is held to the fold
			// floor instead.
			ruledOnThisPair := byProvider[i] && final == v.CanonicalID
			score, held := reverifyFold(dup, docs[fi], threshold, adjFloor, ruledOnThisPair)
			if !held {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID, "canonical": final}).
					Info("Consolidation: the claim does not hold against the resolved canonical; not folded")
				st.RefusedRecheck++
				refusedDonors = append(refusedDonors, dupID)
				continue
			}

			if dup.stands {
				// An adjudicator already said this finding carries detail no
				// survivor of it states. Another verdict naming it, the same
				// one through a redirected canonical, or this fallback
				// choosing a different canonical does not undo that.
				//
				// And the pair is kept, which is what this refusal needs that
				// the flag alone does not give. The flag stops the protected
				// finding being folded; it says nothing about the OTHER
				// member. A second cluster naming this pair as a fold drops
				// its keep as an explicit decision, so with the fold refused
				// here and the protected finding earlier in document order,
				// the community rule was free to fold the canonical into it
				// instead — the survivor the provider chose, hidden on the
				// one direction the provider rejected.
				if o.keepPair(dupID, final) {
					st.Kept++
				}
				st.RefusedKept++
				continue
			}
			in.insights[dup.slot].DuplicateOf = final
			in.insights[dup.slot].SimilarityScore = score
			marks[dupID] = final
			written = append(written, dup.slot)
			dependants[final]++
			st.Folded++
			countFold(&st, c.proposal, 1)
			applog.WithFields(applog.Fields{
				"area": st.Area, "id": dupID, "duplicate_of": final, "score": score,
				"proposal": string(c.proposal), "decider": st.Decider, "reason": v.Reason,
			}).Info("Consolidation: folded")
		}

		// The detail a folded member added, kept on the survivor rather than
		// discarded with it — a fold decides which caveats live, so it is not
		// quality-neutral. Only onto a canonical in the area being
		// consolidated: an earlier area's text is already recorded in that
		// area's checkpoint, and rewriting it here would leave the record and
		// the result disagreeing about what the finding says.
		// A rewrite is asked for precisely when the folded members carry
		// something the survivor does not, so a rewrite that cannot be
		// applied means the folds cannot be applied either: suppressing those
		// members without it is exactly how a fold loses a caveat, which is
		// the failure this pass exists to stop making.
		//
		// Applied however many folds this cluster managed to write, including
		// none: when EVERY proposed fold is refused, the members the verdict
		// named still carry detail the survivor lacks, so they still have to
		// be protected from being folded sideways into each other by the
		// community pass — which is what happened.
		// A verdict whose folds were refused leaves its donors standing side
		// by side, unmarked, and related only through a survivor none of them
		// reached. Nothing in the verdict speaks to the pairs between them, so
		// the community rule must not read its silence as permission.
		for x := 0; x < len(refusedDonors); x++ {
			for y := x + 1; y < len(refusedDonors); y++ {
				donorKeeps = append(donorKeeps, [2]string{refusedDonors[x], refusedDonors[y]})
			}
		}

	}

	// The refused donors, now that every verdict has had its say. A pair one
	// verdict could not fold may be one another verdict folded outright, and
	// that decision wins: a protection against the fallback must not become a
	// veto over a direct decision.
	for _, pair := range donorKeeps {
		if marks[pair[0]] != "" || marks[pair[1]] != "" {
			continue
		}
		if o.keepPair(pair[0], pair[1]) {
			st.Kept++
		}
	}
	// And every pair a rewrite verdict kept, against where its canonical
	// ended up. A later verdict can fold that canonical into something else,
	// and a protection that names only the old target leaves the donor open to
	// the new one — the community rule would then hide it behind a finding
	// that states its claim no better than the first did.
	for _, pair := range rewritePairs {
		donor, canonical := pair[0], pair[1]
		if marks[donor] != "" {
			continue
		}
		final, cycle := resolveTarget(canonical, marks)
		if cycle || final == donor || final == canonical {
			continue
		}
		if o.keepPair(donor, final) {
			st.Kept++
		}
	}

	// The community rule, over everything the provider verdicts left alone.
	// One global pass in document order, so the outcome is the end-of-run
	// check's own and does not depend on which overlapping star came first.
	// See communityFolds.
	for _, fold := range communityFoldOrder(docs, o.communityFolds(docs, threshold, marks, dependants)) {
		dup := docs[fold.at]
		if !dup.current || dup.duplicateOf != "" || marks[dup.id] != "" || dependants[dup.id] > 0 {
			continue
		}
		final, cycle := resolveTarget(fold.target, marks)
		if cycle || final == dup.id {
			continue
		}
		fi, inHand := byID[final]
		if !inHand {
			continue
		}
		// The adjudicator's decisions outrank this rule in both directions:
		// a pair it kept is not folded, and the re-check is cosine-only
		// because containment is a proposal it rules on.
		if o.adjudicatedKeep(dup.id, final) {
			st.RefusedKept++
			continue
		}
		score, held := reverifyFold(dup, docs[fi], threshold, adjFloor, false)
		if !held {
			st.RefusedRecheck++
			continue
		}
		if dup.stands {
			// An adjudicator already said this finding carries detail no
			// survivor of it states. Another verdict naming it, the same
			// one through a redirected canonical, or this fallback
			// choosing a different canonical does not undo that. The
			// simulation refuses it too, so this guard should never be the
			// one that fires — it stays because the fold the simulation
			// chose is re-resolved here against the marks.
			st.RefusedKept++
			continue
		}
		in.insights[dup.slot].DuplicateOf = final
		in.insights[dup.slot].SimilarityScore = score
		marks[dup.id] = final
		written = append(written, dup.slot)
		dependants[final]++
		st.Folded++
		countFold(&st, agentplugin.ProposalCosine, 1)
		applog.WithFields(applog.Fields{
			"area": st.Area, "id": dup.id, "duplicate_of": final, "score": score,
			"decider": "community",
		}).Info("Consolidation: folded")
	}

	if st.Folded == 0 {
		return st
	}

	// The proof. Per-case pre-checks are not sufficient — three hand-run
	// passes produced three variants of one bug, each guarded in turn and each
	// followed by the next variant. So after writing, every mark in the run is
	// resolved transitively and re-verified, and a pass that cannot be proved
	// correct gives back its own marks rather than leaving a reader in front
	// of a row that is itself hidden.
	resolved, err := resolveMarkClosure(marks, func(id string) bool { _, ok := byID[id]; return ok })
	if err == nil {
		for _, slot := range written {
			id := in.insights[slot].ID
			if final := resolved[id]; final != "" && final != in.insights[slot].DuplicateOf {
				err = errBrokenMark
				break
			}
		}
	}
	if err != nil {
		applog.WithError(err).WithFields(applog.Fields{"area": st.Area, "folded": st.Folded}).
			Error("Consolidation: the marks this pass wrote do not resolve cleanly; reverting them")
		for _, slot := range written {
			in.insights[slot].DuplicateOf = ""
			in.insights[slot].SimilarityScore = 0
		}
		st.Reverted = true
		st.Folded, st.ByCosine, st.ByContainment, st.ByBoth = 0, 0, 0, 0
	}
	return st
}

// countFold moves the per-test fold counter the proposal belongs to. One
// function for both directions, so a fold and a fold given back cannot be
// counted against different buckets.
func countFold(st *consolidationStats, p agentplugin.Proposal, delta int) {
	switch p {
	case agentplugin.ProposalCosine:
		st.ByCosine += delta
	case agentplugin.ProposalContainment:
		st.ByContainment += delta
	default:
		st.ByBoth += delta
	}
}

// verdictCanonical resolves the survivor a verdict names, and gives in one
// place every reason a verdict's SHAPE makes it unusable: a canonical outside
// the cluster it was asked about, one that resolves through a cycle of marks,
// one the same verdict suppresses, and one that is not a document of this pass
// at all.
//
// Shared with the pre-pass, and that is the point of extracting it. The
// pre-pass runs before any mark is written, so a verdict the writer discards
// wholesale must not leave a keep or a protection behind: a verdict naming A
// as its canonical and then listing A among its own duplicates was registering
// a keep for the A-B pair the writer then refused, and that keep went on to
// block the community rule from folding B into A at a score clearing the fold
// floor — a duplicate left visible, spending a validation check.
//
// The index returned is the resolved canonical's position in docs, valid only
// when the reason is empty.
func verdictCanonical(
	v agentplugin.ConsolidationVerdict,
	member map[string]bool,
	byID map[string]int,
	marks map[string]string,
) (string, int, string) {
	if !member[v.CanonicalID] {
		return "", -1, "named a canonical outside its cluster"
	}
	// Resolved through the marks already standing. A canonical that is itself
	// suppressed — by this proposal or by a previous pass — is a row the
	// reader cannot be sent to, and pointing at it is how a mutual cycle gets
	// built.
	final, cycle := resolveTarget(v.CanonicalID, marks)
	if cycle {
		return "", -1, "its canonical resolves through a cycle of marks"
	}
	if hasKey(v.Duplicates, final) {
		return "", -1, "the resolved canonical is suppressed by the same verdict"
	}
	fi, inHand := byID[final]
	if !inHand {
		return "", -1, "the resolved canonical is not a document of this pass"
	}
	return final, fi, ""
}

// registerAdjudicatedKeeps records every pair an adjudicator decided should
// stand, before a single mark is written — and, for a verdict that asked for a
// rewrite, the stronger refusal: the donor finding itself must stand, whatever
// else this pass or the end-of-run check later proposes for it.
//
// Read from the VERDICTS, not from the marks, which is what lets it run first.
// It takes two passes over them, and both are load-bearing.
//
// FIRST every pair any verdict explicitly folds, resolving each canonical the
// way the writer will: a verdict naming a canonical that is already suppressed
// is folding into the end of that chain, so the pair it folds is (resolved
// canonical, duplicate) and not only the pair it named.
//
// THEN the keeps, skipping any pair in that set. Two overlapping clusters can
// disagree about one pair — the same member folded in one and simply unnamed
// in the other — and an explicit fold beats an omission: the fold is a
// positive judgement, while a member a verdict did not mention may just be a
// member it did not mention. Without that precedence the second cluster's
// silence vetoed the first cluster's decision, which is the opposite of
// honouring the adjudicator.
//
// A pair whose fold this pass goes on to REFUSE is registered at the refusal
// instead: that one cannot be known from the verdicts, because it depends on
// what the writer could apply.
func (o *Orchestrator) registerAdjudicatedKeeps(
	docs []consolidationDoc,
	clusters []docCluster,
	verdicts map[int]agentplugin.ConsolidationVerdict,
	byProvider map[int]bool,
	byID map[string]int,
	marks map[string]string,
	in consolidateAreaInput,
	st *consolidationStats,
) {
	// A verdict asking for a rewrite says its fold would lose something, so
	// the pair it names must be kept whichever cluster is read first. Settled
	// here, ahead of the explicit folds, because two overlapping clusters can
	// propose the same fold with only one of them asking for the rewrite — and
	// then the outcome depended on processing order: the rewrite verdict first
	// blocked the fold, the rewrite verdict second left the mark standing.
	//
	// This is the one case where a keep beats an explicit fold. An omission is
	// ambiguous, which is why a fold beats it below; a rewrite request is a
	// positive statement that something would be lost.
	rewriteKept := make(map[string]struct{})
	for i := range clusters {
		v, ok := verdicts[i]
		if !ok || !byProvider[i] || strings.TrimSpace(v.MergedDescription) == "" {
			continue
		}
		// Only ids this cluster actually holds. A verdict that names a
		// stranger was decided over a set that is not this one, and the
		// writer's loop refuses it on the same test — registering a keep for
		// a pair that is not in the cluster would protect two documents no
		// adjudicator looked at together.
		member := make(map[string]bool, len(clusters[i].members))
		for _, m := range clusters[i].members {
			member[docs[m].id] = true
		}
		// The whole shape, through the same function the writer uses. A
		// verdict the writer discards wholesale must leave nothing behind
		// here: one naming A as its canonical and then listing A among its
		// own duplicates used to register a keep for the A-B pair, and that
		// keep went on to block the community rule from folding B into A at a
		// score that clears the fold floor.
		canonical, _, bad := verdictCanonical(v, member, byID, marks)
		if bad != "" {
			continue
		}
		for dupID := range v.Duplicates {
			if !member[dupID] || dupID == v.CanonicalID {
				continue
			}
			rewriteKept[keptPairKey(v.CanonicalID, dupID)] = struct{}{}
			rewriteKept[keptPairKey(canonical, dupID)] = struct{}{}
			// The decision that has to outlive this pass, recorded on the
			// finding before a single mark is written. The pair keeps above
			// protect this donor from THIS canonical; the flag protects it
			// from every other fold of this pass, from the community
			// fallback, and — once it is checkpointed — from the end-of-run
			// check after a resume.
			//
			// Before, and not when the writer reaches this verdict: the
			// clusters are overlapping stars read in order, so an earlier
			// verdict can fold this donor into its own canonical first, and
			// a flag set afterwards lands on a finding that is already
			// suppressed and dropped from the recommendations.
			if di, ok := byID[dupID]; ok {
				docs[di].stands = true
				switch {
				case docs[di].current:
					in.insights[docs[di].slot].StandsOnAdjudication = true
				case docs[di].runSlot >= 0:
					// An earlier area's finding, and this is the one thing
					// this pass may write on one. A mark may not: it
					// suppresses a row whose checkpoint is already on disk
					// and whose id is reminted on restore, so it would not
					// survive. A protection suppresses nothing — losing it on
					// a resume leaves the end-of-run check doing what it did
					// before this phase existed — and NOT writing it costs
					// the donor its protection in this very run: the
					// end-of-run check would fold it into a previous
					// discovery's copy of the survivor, which states the
					// claim no better than the survivor did.
					in.runInsights[docs[di].runSlot].StandsOnAdjudication = true
				}
			}
			// Both the named pair and the resolved one, and counted once:
			// the two are the same pair whenever the canonical is not itself
			// folded, which is the ordinary case.
			named := o.keepPair(v.CanonicalID, dupID)
			if o.keepPair(canonical, dupID) || named {
				st.Kept++
			}
		}
	}

	explicit := make(map[string]struct{})
	for i := range clusters {
		v, ok := verdicts[i]
		if !ok || !byProvider[i] {
			continue
		}
		canonical, cycle := resolveTarget(v.CanonicalID, marks)
		if cycle {
			canonical = v.CanonicalID
		}
		for dupID := range v.Duplicates {
			for _, key := range []string{keptPairKey(v.CanonicalID, dupID), keptPairKey(canonical, dupID)} {
				if _, kept := rewriteKept[key]; kept {
					continue
				}
				explicit[key] = struct{}{}
			}
		}
	}

	for i, c := range clusters {
		v, ok := verdicts[i]
		if !ok || !byProvider[i] {
			continue
		}
		for x := 0; x < len(c.members); x++ {
			for y := x + 1; y < len(c.members); y++ {
				a, b := docs[c.members[x]].id, docs[c.members[y]].id
				if _, folded := explicit[keptPairKey(a, b)]; folded {
					continue
				}
				// Both folded into the same canonical by this verdict: they
				// are related through it, and neither can be folded into the
				// other afterwards because both carry a mark.
				_, aDup := v.Duplicates[a]
				_, bDup := v.Duplicates[b]
				if aDup && bDup {
					continue
				}
				// Everything else in the cluster is kept. The provider
				// contract is explicit that a member left out of the
				// Duplicates map is left standing — "two findings can be
				// proposed as a pair and turn out to be two findings" — so
				// its omission is an answer, not a gap, and the community
				// rule must not read past it.
				//
				// Review pushed the other way here, on a real limit: a
				// verdict names ONE canonical, so a cluster holding two
				// independent strong pairs has a pair the verdict cannot
				// describe, and keeping it blocks a fold that should happen.
				// That is a contract problem rather than a caller problem —
				// either the verdict needs to express more than one canonical
				// or such a star should not be built — and guessing past the
				// contract here trades a missed fold for a hidden finding,
				// which is the expensive direction.
				if o.keepPair(a, b) {
					st.Kept++
				}
			}
		}
	}
}

// reverifyFold re-checks a fold against the canonical it will actually point
// at and returns the score to record.
//
// `adjudicated` says whether a provider decided this cluster, and it gates the
// containment half. Containment is a PROPOSAL the adjudicator rules on, never
// a community rule — so letting the community fallback satisfy its re-check by
// containment folds pairs the community rule would never have folded. The
// shape that exposed it: overlapping clusters where B is already folded into
// A, a fallback verdict then proposes C into B, and the re-check against the
// resolved canonical A passes on shared figures at a cosine of 0.643 against a
// 0.88 floor. That is a finding losing its verdict and its place in Phase 5 on
// a rule the deployment never opted into.
//
// The score recorded is the measured cosine whatever it is, including when it
// is below the threshold: a containment fold is a real fold at a real
// similarity, and storing the threshold instead would make the stored score a
// restatement of the rule rather than a measurement of the pair.
func reverifyFold(dup, canonical consolidationDoc, threshold, adjFloor float64, adjudicated bool) (float64, bool) {
	// The magnitude guard gates BOTH paths, and that it did not was a hole
	// this feature opened rather than inherited. sameClaim refuses a fold
	// where the two texts are IDENTICAL and the figures differ — 100 affected
	// entities hidden behind 1,000 — and containment reached its acceptance
	// without passing it, so two otherwise identical findings were folded
	// whenever the headline's figures appeared in the other's body. The
	// quantitatively distinct one then lost its verdict and its place in
	// Phase 5 on the guard's own case.
	if !sameClaim(dup.text, dup.magnitude, canonical.text, canonical.magnitude) {
		return 0, false
	}
	score, _ := cosineSimilarity(dup.vec, canonical.vec)
	if _, ok := cosineFold(dup.foldSubject, canonical.foldSubject, threshold); ok {
		return score, true
	}
	// An adjudicated pair is re-checked against the floor it was PROPOSED at,
	// not the fold floor: a pair between the two is exactly what the model was
	// asked about, and refusing it here would discard every verdict it gave on
	// one. The magnitude guard above still applies, so a pair whose text is
	// identical and whose numbers are not cannot come through this door.
	if adjudicated {
		if _, ok := cosineFold(dup.foldSubject, canonical.foldSubject, adjFloor); ok {
			return score, true
		}
	}
	return 0, false
}

// resolveTarget walks a mark chain to its end. The bool reports a cycle.
func resolveTarget(id string, marks map[string]string) (string, bool) {
	seen := map[string]bool{id: true}
	for {
		next, ok := marks[id]
		if !ok || next == "" {
			return id, false
		}
		if seen[next] {
			return id, true
		}
		seen[next] = true
		id = next
	}
}

func hasKey(m map[string]float64, k string) bool {
	_, ok := m[k]
	return ok
}

// These keeps live only in memory, for the length of the process. The decision
// that MUST survive a resume — an adjudicator saying a finding carries detail
// no survivor states — is recorded on the finding itself instead, as
// StandsOnAdjudication, which travels in the area draft and is read again by
// the end-of-run check. What is left here is the weaker kind: a pair an
// adjudicator saw and left standing, whose loss on a resume costs a fold the
// end-of-run check may then make on document order. That is the outcome the
// product had before this phase existed.
//
// keepPair records that the consolidation pass adjudicated these two documents
// and decided to leave both standing, so the end-of-run check does not fold
// them the other way round.
//
// It matters for exactly one shape: a cosine pair at or above the threshold
// where the adjudicator chose the LATER document as the one a reader should be
// seen to, which this pass cannot write because the earlier one belongs to an
// area already finished. Left to itself, Phase 9 would mark the later one as a
// duplicate of the earlier — the direction the adjudicator rejected.
//
// In memory for the attempt that made the decision, which is the attempt whose
// Phase 9 reads it. An area restored from a completed checkpoint does not
// re-run its pass, so a resumed run can lose a refusal and get Phase 9's own
// rule for that pair instead: today's behaviour, not a new failure.
//
// Two shapes of that are worth naming, because review found both and the
// answer is the same. A refused rewrite protects its donor on the finding, so
// the flag survives a resume — but the flag stops the donor being folded, and
// Phase 9 reads it on the document it is about to fold, not on the target. If
// the donor is EARLIER in document order, a resumed Phase 9 folds the
// canonical into it. And a donor belonging to an area the run had already
// finished cannot carry the flag at all, for the same reason it cannot carry a
// mark: its checkpoint is written and its ids are reminted on restore.
//
// In both cases what the resumed run does is Phase 9's document-order rule —
// the outcome with no consolidation in the product at all — so the feature is
// not making a finding less visible than it was, it is only failing to improve
// it on a resumed attempt. Fixing it properly means persisting the keeps in
// the deciding area's checkpoint under ids that survive a restore, which is a
// checkpoint-schema decision and not part of moving the containment judgement
// to the model.
// The bool reports whether this pair was not already kept, so a caller can
// count PAIRS rather than calls: one pair can be recorded by the rewrite
// pre-pass, by the cluster keep pass and at a refusal, and `kept` read 3 for
// one pair.
func (o *Orchestrator) keepPair(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if o.consolidationKept == nil {
		o.consolidationKept = make(map[string]struct{})
	}
	key := keptPairKey(a, b)
	if _, already := o.consolidationKept[key]; already {
		return false
	}
	o.consolidationKept[key] = struct{}{}
	return true
}

// adjudicatedKeep reports whether the consolidation pass decided to leave this
// pair standing.
func (o *Orchestrator) adjudicatedKeep(a, b string) bool {
	if len(o.consolidationKept) == 0 {
		return false
	}
	_, ok := o.consolidationKept[keptPairKey(a, b)]
	return ok
}

func keptPairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// cachedInsightVector returns the vector embedded for this text during the
// run, or nil.
func (o *Orchestrator) cachedInsightVector(text string) []float64 {
	if len(o.insightVectors) == 0 {
		return nil
	}
	return o.insightVectors[text]
}

// cacheInsightVector records a vector so Phase 9 does not pay to embed the
// same text again.
//
// Keyed on the embedding TEXT rather than the insight id, which is what makes
// it correct when a description is merged: the merged text is not in the cache,
// so it is embedded, while every untouched insight is a hit. Keyed on the id it
// would have served a stale vector for the one document whose meaning changed.
//
// No lock: the area loop is sequential and Phase 9 runs after it. The
// concurrency inside an area is the validation workers, which never touch this.
func (o *Orchestrator) cacheInsightVector(text string, vec []float64) {
	if text == "" || len(vec) == 0 {
		return
	}
	if o.insightVectors == nil {
		o.insightVectors = make(map[string][]float64)
	}
	o.insightVectors[text] = vec
}

// persisted renders the pass for the run log, or nil when there was nothing to
// record. Nil for an area that proposed no clusters at all, which keeps the
// field absent on the common case rather than storing a row of zeros per area.
func (st consolidationStats) persisted() *models.ConsolidationSummary {
	if st.Clusters == 0 && st.Folded == 0 && st.Kept == 0 {
		return nil
	}
	return &models.ConsolidationSummary{
		Documents:          st.Documents,
		Clusters:           st.Clusters,
		Folded:             st.Folded,
		ByCosine:           st.ByCosine,
		ByContainment:      st.ByContainment,
		ByBoth:             st.ByBoth,
		Decider:            st.Decider,
		RefusedOtherArea:   st.RefusedNotMine,
		RefusedRecheck:     st.RefusedRecheck,
		RefusedShape:       st.RefusedShape,
		RefusedUnmergeable: st.RefusedUnmergeable,
		RefusedKept:        st.RefusedKept,
		Kept:               st.Kept,
		OverBound:          st.OverBound,
		Reverted:           st.Reverted,
		DurationMs:         st.DurationMs,
	}
}
