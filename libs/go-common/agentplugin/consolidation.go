package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// Proposal names which of the two cheap tests put a cluster in front of the
// provider. Both run and their results are unioned, because measured on real
// run output they find largely DIFFERENT duplicates: on one run the cosine rule
// proposed 18 pairs and the containment scan 16, and only 3 were the same pair.
// Neither test is a superset of the other, so the provider is told which one
// fired — a containment-only cluster is one the similarity threshold cannot see
// at all, and that is a different kind of evidence from a 0.93 cosine.
type Proposal string

const (
	// ProposalCosine — the shipped similarity rule proposed this cluster:
	// cosine over the embedded text at or above the configured floor.
	ProposalCosine Proposal = "cosine"
	// ProposalContainment — every distinctive figure in one member's
	// headline appears in another's body. The commonest duplicate shape on
	// findings that carry tables: the headline IS one row of the other's
	// table, which scores 0.71–0.87 and is never folded by similarity.
	ProposalContainment Proposal = "containment"
	// ProposalBoth — both tests fired on the same cluster.
	ProposalBoth Proposal = "both"
)

// ConsolidationDoc is one candidate finding, as the adjudicating provider sees
// it. A projection of the in-flight insight: enough to judge whether two
// findings state the same thing and which phrasing a reader should be sent to,
// and nothing else — the prompt carries the clustered documents, never the run.
type ConsolidationDoc struct {
	ID            string
	Name          string
	Description   string
	AnalysisArea  string
	Severity      string
	TargetSegment string
	AffectedCount int
	Indicators    []string
	Metrics       map[string]any

	// Score is the cosine similarity against the cluster's seed, or 0 when
	// this member was proposed by containment alone and no vector was
	// available. Never a reason on its own: it is what the threshold already
	// knew.
	Score float64

	// EarlierArea is set when this document comes from an area the run has
	// already finished, rather than the one being consolidated.
	//
	// It is the one piece of structural information a provider cannot infer
	// and must not ignore: only the area being consolidated can be marked.
	// An earlier area's finding is a legitimate CANONICAL — usually the right
	// one — but choosing it as the member to suppress is a verdict the caller
	// has to refuse, because that document's verdict is already paid for and
	// its mark has nowhere durable to live.
	EarlierArea bool

	// Verdict is the validation verdict this document already carries, or ""
	// when it has none yet. Empty for every document in the area being
	// consolidated, by construction — that is the whole point of running
	// before validation.
	Verdict string

	// AreaScope is what this document's analysis area is for, and where
	// available what it explicitly does NOT own, taken from the project's own
	// prompt for that area.
	//
	// Supplied because ownership predicts the right canonical better than
	// document order does, and document order is all the shipped rule has:
	// scored by hand over four runs, the member that should survive is the
	// one whose area OWNS the subject far more often than it is the one that
	// happens to sit earliest in the result.
	AreaScope string
}

// DuplicateCluster is one set of candidates to adjudicate together.
//
// Candidates are in run order, so Candidates[0] is the earliest — which is the
// member the shipped rule would keep, and therefore the fallback answer.
//
// A cluster is a STAR, not a connected component: every member has a direct
// relation to the seed. Neither cosine nor containment is transitive, and
// clustering by transitive closure over the containment relation — A contains
// B, B contains C, therefore one cluster — proposed suppressing 23 of 55
// insights on a real run where the direct relations yield 9.
type DuplicateCluster struct {
	Candidates []ConsolidationDoc
	Proposal   Proposal
}

// ConsolidationVerdict is one cluster's adjudication.
type ConsolidationVerdict struct {
	// CanonicalID is the member a reader should be sent to. Must be one of
	// the cluster's candidates.
	CanonicalID string
	// Duplicates maps each member folded into the canonical to the score it
	// is folded at. A member of the cluster left out of this map is left
	// standing, which is a legitimate answer: two findings can be proposed
	// as a pair and turn out to be two findings.
	Duplicates map[string]float64
	// MergedDescription replaces the canonical's description, so a figure or
	// a caveat that only a folded member carried survives the fold. Empty
	// leaves the canonical alone.
	//
	// Applied only when the canonical is in the area being consolidated: an
	// earlier area's text is already recorded in that area's checkpoint, and
	// rewriting it here would leave the record and the result disagreeing.
	MergedDescription string
	// Reason is one line for the run log, so a fold can be read back without
	// re-deriving it.
	Reason string
}

// ConsolidationProvider adjudicates proposed duplicate clusters.
//
// It answers the three questions a similarity threshold cannot: which member
// is canonical (the shipped rule can only answer "whichever sits earliest"),
// what detail the folded members add that should survive the fold, and whether
// a near-threshold pair that is the same subject with a different metric is one
// finding or two.
type ConsolidationProvider interface {
	// Consolidate returns one verdict per cluster it decided. Clusters it
	// declines to decide may be omitted; the caller falls back to the
	// community rule for those. Must be safe for concurrent use and honor
	// ctx cancellation.
	Consolidate(ctx context.Context, projectID string, clusters []DuplicateCluster) ([]ConsolidationVerdict, error)
	// Name is a stable identifier for telemetry.
	Name() string
}

var (
	consolidationMu       sync.RWMutex
	consolidationProvider ConsolidationProvider
)

// RegisterConsolidationProvider installs p as the consolidation provider.
// There is a single provider, as with DiscoveryPolicyProvider; the last
// registrant wins. Registering nil, or a provider with an empty Name(), panics
// — those are programmer errors surfaced at init().
func RegisterConsolidationProvider(p ConsolidationProvider) {
	if p == nil {
		panic("agentplugin: RegisterConsolidationProvider called with nil provider")
	}
	if p.Name() == "" {
		panic("agentplugin: ConsolidationProvider.Name() returned empty string")
	}
	consolidationMu.Lock()
	consolidationProvider = p
	consolidationMu.Unlock()
}

// HasConsolidationProvider reports whether one is registered. The caller uses
// it to skip building the projection at all on a deployment with no provider,
// where the community fallback needs nothing but the clusters it already has.
func HasConsolidationProvider() bool {
	consolidationMu.RLock()
	defer consolidationMu.RUnlock()
	return consolidationProvider != nil
}

// ConsolidationProviderName returns the registered provider's name, or "" when
// none is registered. For the run log, so a run records which decider it used.
func ConsolidationProviderName() string {
	consolidationMu.RLock()
	p := consolidationProvider
	consolidationMu.RUnlock()
	if p == nil {
		return ""
	}
	return safeConsolidationName(p)
}

// ResolveConsolidation asks the registered provider to adjudicate clusters.
//
// Best-effort and it never fails discovery. With no provider registered it
// returns (nil, nil) and the caller applies the community rule — earliest
// member wins, which is what Phase 9 does today. A provider that errors or
// panics returns (nil, err): the caller logs it and applies the same rule, so
// the worst case for a deployment with a broken provider is today's outcome,
// decided a little earlier in the run.
//
// Returning nil rather than a partial result on error is deliberate. A
// half-applied adjudication is the one state the write guard cannot reason
// about: a canonical chosen in one cluster and suppressed in another is how a
// fold chain gets built, and a fold chain is a document hidden behind a row
// that is itself hidden.
func ResolveConsolidation(ctx context.Context, projectID string, clusters []DuplicateCluster) (verdicts []ConsolidationVerdict, err error) {
	consolidationMu.RLock()
	p := consolidationProvider
	consolidationMu.RUnlock()
	if p == nil || len(clusters) == 0 {
		return nil, nil
	}
	defer func() {
		if r := recover(); r != nil {
			verdicts = nil
			err = fmt.Errorf("agentplugin: consolidation provider %q panicked: %v", safeConsolidationName(p), r)
		}
	}()
	out, perr := p.Consolidate(ctx, projectID, clusters)
	if perr != nil {
		return nil, perr
	}
	return out, nil
}

func safeConsolidationName(p ConsolidationProvider) (name string) {
	defer func() {
		if recover() != nil {
			name = "<unknown:Name() panicked>"
		}
	}()
	return p.Name()
}

// resetConsolidationForTest clears the registered provider. Test-only.
func resetConsolidationForTest() {
	consolidationMu.Lock()
	consolidationProvider = nil
	consolidationMu.Unlock()
}
