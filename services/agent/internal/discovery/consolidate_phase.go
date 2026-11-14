package discovery

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
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
	Area           string
	Documents      int
	Clusters       int
	Folded         int
	ByCosine       int
	ByContainment  int
	ByBoth         int
	Decider        string
	RefusedNotMine int
	RefusedRecheck int
	RefusedShape   int
	Reverted       bool
	DurationMs     int64
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
	if len(in.insights) == 0 {
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

	verdicts, decider := o.adjudicate(ctx, docs, clusters, threshold)
	st.Decider = decider
	st = o.applyConsolidation(in, docs, clusters, verdicts, threshold, st)
	st.DurationMs = time.Since(started).Milliseconds()

	applog.WithFields(applog.Fields{
		"area": st.Area, "documents": st.Documents, "clusters": st.Clusters,
		"folded": st.Folded, "by_cosine": st.ByCosine, "by_containment": st.ByContainment,
		"by_both": st.ByBoth, "decider": st.Decider,
		"refused_other_area": st.RefusedNotMine, "refused_recheck": st.RefusedRecheck,
		"refused_shape": st.RefusedShape, "reverted": st.Reverted,
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
	var b strings.Builder
	if ins.DescriptionMd != "" {
		b.WriteString(ins.DescriptionMd)
	} else {
		b.WriteString(ins.Description)
	}
	for _, ind := range ins.Indicators {
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
func (o *Orchestrator) adjudicate(ctx context.Context, docs []consolidationDoc, clusters []docCluster, threshold float64) (map[int]agentplugin.ConsolidationVerdict, string) {
	out := make(map[int]agentplugin.ConsolidationVerdict, len(clusters))
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
			}
		}
	}

	for i, c := range clusters {
		if _, decided := out[i]; decided {
			continue
		}
		out[i] = communityVerdict(docs, c, threshold)
	}
	return out, decider
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
			Description:   d.body,
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

// communityVerdict is the fallback: earliest member canonical, cosine pairs
// folded into it, everything else left standing.
func communityVerdict(docs []consolidationDoc, c docCluster, threshold float64) agentplugin.ConsolidationVerdict {
	if len(c.members) == 0 {
		return agentplugin.ConsolidationVerdict{}
	}
	canonical := docs[c.members[0]]
	v := agentplugin.ConsolidationVerdict{
		CanonicalID: canonical.id,
		Reason:      "community rule: earliest member, similarity pair",
	}
	for _, m := range c.members[1:] {
		if score, ok := cosineFold(docs[m].foldSubject, canonical.foldSubject, threshold); ok {
			if v.Duplicates == nil {
				v.Duplicates = make(map[string]float64, len(c.members))
			}
			v.Duplicates[docs[m].id] = score
		}
	}
	return v
}

// applyConsolidation writes the verdicts the guards allow, then proves the
// result: every mark in the run resolved transitively to a final canonical,
// re-verified, and the whole area's marks reverted if any of it is broken.
func (o *Orchestrator) applyConsolidation(
	in consolidateAreaInput,
	docs []consolidationDoc,
	clusters []docCluster,
	verdicts map[int]agentplugin.ConsolidationVerdict,
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
	// Documents this pass has already sent a reader to. Marking one of them
	// later would build a chain — the closure check would catch it, but
	// refusing here keeps the pass's own output chain-free by construction.
	usedAsCanonical := make(map[string]bool, len(clusters))
	written := make([]int, 0, len(docs))

	for i, c := range clusters {
		v, ok := verdicts[i]
		if !ok {
			continue
		}
		member := make(map[string]bool, len(c.members))
		for _, m := range c.members {
			member[docs[m].id] = true
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
				continue
			}
			if usedAsCanonical[dupID] {
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
			score, held := reverifyFold(dup, docs[fi], threshold)
			if !held {
				applog.WithFields(applog.Fields{"area": st.Area, "id": dupID, "canonical": final}).
					Info("Consolidation: the claim does not hold against the resolved canonical; not folded")
				st.RefusedRecheck++
				continue
			}

			in.insights[dup.slot].DuplicateOf = final
			in.insights[dup.slot].SimilarityScore = score
			marks[dupID] = final
			written = append(written, dup.slot)
			usedAsCanonical[final] = true
			clusterFolds++
			st.Folded++
			switch c.proposal {
			case agentplugin.ProposalCosine:
				st.ByCosine++
			case agentplugin.ProposalContainment:
				st.ByContainment++
			default:
				st.ByBoth++
			}
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
		if merged := strings.TrimSpace(v.MergedDescription); merged != "" && docs[fi].current && clusterFolds > 0 {
			plain, md := splitMarkdownDescription(merged)
			in.insights[docs[fi].slot].Description = plain
			in.insights[docs[fi].slot].DescriptionMd = md
			applog.WithFields(applog.Fields{"area": st.Area, "canonical": final}).
				Info("Consolidation: merged the folded members' detail onto the canonical")
		}
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

// reverifyFold re-checks a fold against the canonical it will actually point
// at, by either test, and returns the score to record.
//
// The score recorded is the measured cosine whatever it is, including when it
// is below the threshold: a containment fold is a real fold at a real
// similarity, and storing the threshold instead would make the stored score a
// restatement of the rule rather than a measurement of the pair.
func reverifyFold(dup, canonical consolidationDoc, threshold float64) (float64, bool) {
	score, _ := cosineSimilarity(dup.vec, canonical.vec)
	if _, ok := cosineFold(dup.foldSubject, canonical.foldSubject, threshold); ok {
		return score, true
	}
	if containedIn(dup.name, canonical.body) {
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
	if st.Clusters == 0 && st.Folded == 0 {
		return nil
	}
	return &models.ConsolidationSummary{
		Documents:        st.Documents,
		Clusters:         st.Clusters,
		Folded:           st.Folded,
		ByCosine:         st.ByCosine,
		ByContainment:    st.ByContainment,
		ByBoth:           st.ByBoth,
		Decider:          st.Decider,
		RefusedOtherArea: st.RefusedNotMine,
		RefusedRecheck:   st.RefusedRecheck,
		RefusedShape:     st.RefusedShape,
		Reverted:         st.Reverted,
		DurationMs:       st.DurationMs,
	}
}
