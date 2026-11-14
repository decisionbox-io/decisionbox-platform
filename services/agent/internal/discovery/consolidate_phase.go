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
	threshold := documentDedupMinScore()
	docs := o.consolidationDocs(ctx, in)
	st.Documents = len(docs)
	if len(docs) < 2 {
		return st
	}

	clusters := buildClusters(docs, proposePairs(docs, threshold))
	st.Clusters = len(clusters)
	if len(clusters) == 0 {
		st.DurationMs = time.Since(started).Milliseconds()
		return st
	}

	// What goes to the adjudicator, as against what the community rule
	// decides. See consolidationMaxClustersEnv.
	adjudicable := clusters
	if bound := consolidationMaxClusters(); bound > 0 && len(clusters) > bound {
		adjudicable = clusters[:bound]
		st.OverBound = len(clusters) - bound
		applog.WithFields(applog.Fields{
			"area": st.Area, "clusters": len(clusters), "bound": bound,
			"to_community_rule": st.OverBound, "env": consolidationMaxClustersEnv,
		}).Warn("Consolidation: this area proposed more clusters than the adjudication bound; the rest fall to the community rule")
	}

	verdicts, byProvider, decider := o.adjudicate(ctx, docs, adjudicable, threshold)
	st.Decider = decider
	st = o.applyConsolidation(in, docs, clusters, verdicts, byProvider, threshold, st)
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
	add := func(ins *models.Insight, current bool, slot int) {
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
			name:        ins.Name,
			prose:       insightProse(ins),
			body:        insightBody(ins),
			area:        ins.AnalysisArea,
			areaScope:   areaScopeText(area, in.prompts.AnalysisAreas[ins.AnalysisArea]),
			severity:    ins.Severity,
			segment:     ins.TargetSegment,
			affected:    ins.AffectedCount,
			indicators:  ins.Indicators,
			metrics:     ins.Metrics,
			verdict:     verdictOf(ins),
			duplicateOf: ins.DuplicateOf,
		})
	}
	for i := range in.runInsights {
		add(&in.runInsights[i], false, -1)
	}
	for i := range in.insights {
		add(&in.insights[i], true, i)
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
			// Non-fatal, and deliberately not a reason to skip the pass: the
			// containment test needs no vectors, and it is the test that finds
			// the duplicate shape similarity cannot see.
			applog.WithError(err).WithField("texts", len(missing)).
				Warn("Consolidation: embedding failed; falling back to the containment test alone")
		case len(vecs) != len(missing):
			applog.WithFields(applog.Fields{"want": len(missing), "got": len(vecs)}).
				Warn("Consolidation: embedding count mismatch; falling back to the containment test alone")
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

// insightBody is what the containment test looks inside: the finding's prose
// as it was authored, plus the figures it lists.
//
// DescriptionMd in preference to Description, because the markdown rendition
// is where the table lives — and a table row is the commonest thing a second
// finding restates as its own headline. The indicators are appended because a
// model asked to list its supporting figures puts some of them there rather
// than in the prose.
func insightBody(ins *models.Insight) string {
	return insightBodyOf(ins.Description, ins.DescriptionMd, ins.Indicators)
}

// insightProse is the authored body alone, markdown rendition preferred.
func insightProse(ins *models.Insight) string {
	if ins.DescriptionMd != "" {
		return ins.DescriptionMd
	}
	return ins.Description
}

// insightBodyOf is insightBody over loose fields, so a description rewritten
// mid-pass can be projected again through the same derivation. Two
// constructions of "what the containment test looks inside" is how the
// projection and the re-check come to disagree about one document.
func insightBodyOf(description, descriptionMd string, indicators []string) string {
	var b strings.Builder
	if descriptionMd != "" {
		b.WriteString(descriptionMd)
	} else {
		b.WriteString(description)
	}
	for _, ind := range indicators {
		b.WriteString("\n")
		b.WriteString(ind)
	}
	return b.String()
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
func (o *Orchestrator) adjudicate(ctx context.Context, docs []consolidationDoc, clusters []docCluster, threshold float64) (map[int]agentplugin.ConsolidationVerdict, map[int]bool, string) {
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
				out[i] = verdicts[i]
				byProvider[i] = true
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
func (o *Orchestrator) communityFolds(docs []consolidationDoc, threshold float64) map[string]communityFold {
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

	// Seeded with the marks the documents already carry, so a match against a
	// document a previous area's pass folded resolves to the canonical a
	// reader can actually see.
	canonical := make(map[string]string, len(docs))
	for i := range docs {
		if docs[i].duplicateOf != "" {
			canonical[docs[i].id] = docs[i].duplicateOf
		}
	}

	out := make(map[string]communityFold, len(docs))
	for i := range docs {
		// A document this pass cannot mark — one from an earlier area, or
		// one already folded — contributes nothing to the chain either: its
		// mark would never be written, so a later document must not resolve
		// through it.
		if docs[i].duplicateOf != "" || !docs[i].current {
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
	st consolidationStats,
) consolidationStats {
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
	// Every pair an adjudicator decided should stand, registered BEFORE a
	// single mark is written.
	//
	// Incrementally was wrong, and the shape is worth stating: the clusters
	// are overlapping stars processed in order, so a cluster the provider
	// DECLINED can be processed first and its community fallback folds a pair
	// that a provider verdict for a later, overlapping cluster explicitly
	// keeps. Discovered after the fact, the keep is useless — the mark is
	// already written, and the end-of-run check will not revisit a document
	// that carries one. A pair's keep is derivable from the verdict alone, so
	// it does not need the marks and can be known first.
	o.registerAdjudicatedKeeps(docs, clusters, verdicts, byProvider, marks, &st)

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
	mergedCanonicals := make(map[string]bool, len(clusters))
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
		if !member[v.CanonicalID] {
			applog.WithFields(applog.Fields{"area": st.Area, "canonical": v.CanonicalID}).
				Info("Consolidation: a verdict named a canonical outside its cluster; discarded")
			st.RefusedShape++
			continue
		}
		// Resolve the canonical through the marks already standing. A
		// canonical that is itself suppressed — by this proposal or by a
		// previous pass — is a row the reader cannot be sent to, and pointing
		// at it is how a mutual cycle gets built.
		final, cycle := resolveTarget(v.CanonicalID, marks)
		if cycle {
			st.RefusedShape++
			continue
		}
		if hasKey(v.Duplicates, final) {
			applog.WithFields(applog.Fields{"area": st.Area, "canonical": final}).
				Info("Consolidation: the resolved canonical is suppressed by the same verdict; cluster discarded")
			st.RefusedShape++
			continue
		}
		fi, inHand := byID[final]
		if !inHand {
			st.RefusedShape++
			continue
		}

		// This cluster's own folds. Gating the merge on the run-wide counter
		// would apply a merged description to a canonical nothing was folded
		// into, as soon as any earlier cluster had folded something.
		clusterFolds := 0
		clusterWritten := make([]int, 0, len(c.members))
		// Whether any member this verdict named as a duplicate was NOT
		// folded. It matters only when a rewrite is also asked for: the
		// rewrite may incorporate detail from a donor that was refused, and
		// applying it would then credit the survivor with evidence it does
		// not cite. See the merge switch below.
		clusterRefusedDonor := false

		// Sorted, so two runs over the same verdict write the same marks.
		dupIDs := make([]string, 0, len(v.Duplicates))
		for id := range v.Duplicates {
			dupIDs = append(dupIDs, id)
		}
		sort.Strings(dupIDs)

		for _, dupID := range dupIDs {
			if !member[dupID] || dupID == final {
				st.RefusedShape++
				clusterRefusedDonor = true
				continue
			}
			di, inHand := byID[dupID]
			if !inHand {
				st.RefusedShape++
				clusterRefusedDonor = true
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
				clusterRefusedDonor = true
				continue
			}
			if dependants[dupID] > 0 {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID}).
					Info("Consolidation: refusing to suppress a document this pass already made canonical")
				st.RefusedShape++
				clusterRefusedDonor = true
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
				clusterRefusedDonor = true
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
				clusterRefusedDonor = true
				continue
			}
			score, held := reverifyFold(dup, docs[fi], threshold, byProvider[i])
			if !held {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID, "canonical": final}).
					Info("Consolidation: the claim does not hold against the resolved canonical; not folded")
				st.RefusedRecheck++
				clusterRefusedDonor = true
				continue
			}

			in.insights[dup.slot].DuplicateOf = final
			in.insights[dup.slot].SimilarityScore = score
			marks[dupID] = final
			written = append(written, dup.slot)
			clusterWritten = append(clusterWritten, dup.slot)
			dependants[final]++
			clusterFolds++
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
		// the failure this pass exists to stop making. Two reasons it cannot
		// be applied, and they get the same answer.
		// Not gated on clusterFolds: when EVERY proposed fold is refused there
		// is nothing to revert, and the members the verdict named still carry
		// detail the survivor lacks — so they still have to be protected from
		// being folded sideways into each other by the community pass, which
		// is what happened. The protection is the point; the revert is only
		// the part that has something to undo.
		if merged := strings.TrimSpace(v.MergedDescription); merged != "" {
			switch {
			case !docs[fi].current, mergedCanonicals[final], clusterRefusedDonor,
				len(in.insights[docs[fi].slot].Figures) > 0:
				// Either the survivor belongs to an area the run has already
				// finished — its text is recorded in that area's checkpoint,
				// and rewriting it here would leave the record and the result
				// disagreeing — or this pass has already rewritten it for an
				// overlapping cluster, so this rewrite was written against
				// text that has since changed and applying it would erase
				// what the first one preserved.
				reason := "the survivor's area has already recorded its text"
				switch {
				case !docs[fi].current:
					// keeps the reason above
				case clusterRefusedDonor:
					// The rewrite may incorporate detail from a member this
					// pass refused to fold — an earlier area's finding, say,
					// or one the re-check dropped. Applying it credits the
					// survivor with a claim whose evidence it does not cite,
					// and the provenance union below cannot carry what was
					// never folded. The whole cluster goes back rather than
					// half of it.
					reason = "a member this verdict folds was refused, so its rewrite cannot be trusted"
				case len(in.insights[docs[fi].slot].Figures) > 0:
					// The survivor declared evidence figures, and those were
					// RENDERED INTO this prose: the figure template holds the
					// text as authored with the references intact, and a
					// recommendation may restate a figure by naming its id.
					// Free-text replacement severs that link — every field
					// stops matching its re-render, so a repaired insight's
					// figures are all withheld from the recommender, and on an
					// unrepaired one a recommendation could restate a number
					// the survivor's prose no longer shows. The model is not
					// shown the references (it is shown the rendered text), so
					// it cannot preserve them, and re-deriving them from free
					// text is the numeral surgery the figure layer exists to
					// avoid.
					reason = "the survivor's prose carries rendered evidence figures"
				default:
					reason = "this pass has already rewritten the survivor for an overlapping cluster"
				}
				// Every member this verdict named as a duplicate, written or
				// not, protected against every other member of the cluster.
				// Named-but-refused is the case that has nothing to revert and
				// needed protecting just as much.
				for _, dupID := range dupIDs {
					for _, m := range c.members {
						if docs[m].id == dupID {
							continue
						}
						o.keepPair(dupID, docs[m].id)
						st.Kept++
					}
				}
				for _, slot := range clusterWritten {
					// Registered at the refusal and not in the pre-pass: the
					// pre-pass reads the verdicts, which FOLD these pairs, and
					// only the writer knows it could not apply the fold.
					//
					// The keeps for this member are registered above, with
					// every other named duplicate's.
					// Out of `marks` as well as off the insight. Left in, a
					// later cluster resolving its canonical through the chain
					// map would resolve through a mark that no longer exists
					// and point a real fold at the wrong document — and
					// keepStanding would read this pair as folded and record
					// no keep for it, so the end-of-run check would fold it
					// the way this branch just refused.
					delete(marks, in.insights[slot].ID)
					dependants[final]--
					in.insights[slot].DuplicateOf = ""
					in.insights[slot].SimilarityScore = 0
					st.Folded--
					clusterFolds--
					// And the bucket the fold was counted in, or Folded would
					// read 0 beside a non-zero by_cosine and the one number
					// this log line exists to make legible — the fold rate per
					// test — would not add up.
					countFold(&st, c.proposal, -1)
				}
				// `written` is the pass-wide list the closure check reads, and
				// this cluster's slots are its last entries. `clusterWritten`
				// is not reset: nothing after this switch reads it, and the
				// next cluster builds its own.
				written = written[:len(written)-len(clusterWritten)]
				st.RefusedUnmergeable++
				applog.WithFields(applog.Fields{"area": st.Area, "canonical": final, "reason": reason}).
					Info("Consolidation: the survivor cannot be rewritten; leaving the findings standing")
			case clusterFolds == 0:
				// A rewrite with nothing folded into the survivor. There is
				// nothing to carry across and nothing to protect — the
				// pre-pass has already registered this verdict's pairs as
				// standing — so an unnecessary rewrite would lose detail
				// rather than add it.
				applog.WithFields(applog.Fields{"area": st.Area, "canonical": final}).
					Info("Consolidation: a rewrite was offered with nothing folded into the survivor; left alone")
			default:
				plain, md := splitMarkdownDescription(merged)
				in.insights[docs[fi].slot].Description = plain
				in.insights[docs[fi].slot].DescriptionMd = md
				// The provenance of what the rewrite now states, carried with
				// it. A folded member's figure is supported by the steps IT
				// cited, and the verifier builds its evidence bundle from the
				// survivor's SourceSteps — so a merged claim whose step the
				// survivor does not cite would be checked against evidence
				// that does not contain it, and attachSourceQuality would
				// miss the caveat that step carries.
				in.insights[docs[fi].slot].SourceSteps = mergeSourceSteps(
					in.insights[docs[fi].slot].SourceSteps, in.insights, clusterWritten)
				// And in the projection, so a later cluster folding into the
				// same survivor runs its containment re-check against what
				// the survivor now says rather than against the text it said
				// before this rewrite. The VECTOR is necessarily stale — the
				// merged text has not been embedded — so a cosine re-check
				// still reads the pre-merge document, which is the
				// conservative direction: it can refuse a fold that would now
				// hold, never accept one that would not.
				docs[fi].prose = insightProse(&in.insights[docs[fi].slot])
				docs[fi].body = insightBodyOf(plain, md, docs[fi].indicators)
				mergedCanonicals[final] = true
				applog.WithFields(applog.Fields{"area": st.Area, "canonical": final}).
					Info("Consolidation: merged the folded members' detail onto the canonical")
			}
		}
	}

	// The community rule, over everything the provider verdicts left alone.
	// One global pass in document order, so the outcome is the end-of-run
	// check's own and does not depend on which overlapping star came first.
	// See communityFolds.
	for _, fold := range communityFoldOrder(docs, o.communityFolds(docs, threshold)) {
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
		score, held := reverifyFold(dup, docs[fi], threshold, false)
		if !held {
			st.RefusedRecheck++
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

// mergeSourceSteps returns the survivor's cited steps unioned with those of
// the members folded into it, deduplicated and ordered.
//
// Only called when a rewrite was applied: a fold with no rewrite leaves the
// survivor's text alone, so it states nothing it did not already cite.
// Ordered and deduplicated because the list is rendered into prompts and
// persisted, and a set whose order changes between two runs reads as a changed
// claim.
func mergeSourceSteps(canonical []int, insights []models.Insight, folded []int) []int {
	seen := make(map[int]struct{}, len(canonical)+len(folded))
	out := make([]int, 0, len(canonical)+len(folded))
	add := func(steps []int) {
		for _, step := range steps {
			if _, dup := seen[step]; dup {
				continue
			}
			seen[step] = struct{}{}
			out = append(out, step)
		}
	}
	add(canonical)
	for _, slot := range folded {
		add(insights[slot].SourceSteps)
	}
	sort.Ints(out)
	return out
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

// registerAdjudicatedKeeps records every pair an adjudicator decided should
// stand, before a single mark is written.
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
	marks map[string]string,
	st *consolidationStats,
) {
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
			explicit[keptPairKey(v.CanonicalID, dupID)] = struct{}{}
			explicit[keptPairKey(canonical, dupID)] = struct{}{}
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
				o.keepPair(a, b)
				st.Kept++
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
func reverifyFold(dup, canonical consolidationDoc, threshold float64, adjudicated bool) (float64, bool) {
	score, _ := cosineSimilarity(dup.vec, canonical.vec)
	if _, ok := cosineFold(dup.foldSubject, canonical.foldSubject, threshold); ok {
		return score, true
	}
	if adjudicated && containedIn(dup.name, canonical.body) {
		return score, true
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
func (o *Orchestrator) keepPair(a, b string) {
	if a == "" || b == "" {
		return
	}
	if o.consolidationKept == nil {
		o.consolidationKept = make(map[string]struct{})
	}
	o.consolidationKept[keptPairKey(a, b)] = struct{}{}
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
