package warehouse

import "fmt"

// QualityKind names a way in which a source can return a result that is not a
// faithful answer to the query that produced it.
//
// Every kind here describes a *silent* degradation: the source computed
// something, returned well-formed rows, and reported the shortfall only in
// metadata alongside them. Nothing about the rows themselves says they are
// incomplete, so a consumer that reads only the rows cannot tell a sound
// result from an unsound one.
type QualityKind string

const (
	// QualityWithheld means the source omitted rows it holds but declined to
	// return — a small-cohort privacy threshold being the usual reason. The
	// rows present are real; the ones missing are not zero, they are unknown,
	// so any total, share or ranking computed over the result understates by
	// an unknown amount.
	QualityWithheld QualityKind = "withheld"

	// QualitySampled means the values were computed from a subset of the
	// underlying events and extrapolated. Aggregates are estimates with
	// sampling error, and small differences between them may not be real.
	QualitySampled QualityKind = "sampled"

	// QualityTruncated means the source collapsed the tail of a
	// high-cardinality breakdown into a single catch-all bucket. The named
	// rows are accurate, but they are not the whole population and "the top N"
	// may not be the true top N.
	QualityTruncated QualityKind = "truncated"

	// QualityRestricted means the source withheld fields the query asked for,
	// because the credential in use is not permitted to read them. Unlike an
	// authorization error, this arrives as a successful response with those
	// fields quietly absent.
	QualityRestricted QualityKind = "restricted"
)

// QualityCaveat is a source-reported statement that a result is degraded, and
// how.
//
// It exists because the alternative is worse than an error. A query that fails
// is visible; a query that succeeds against withheld or sampled data produces a
// confident, well-formed, wrong answer — and downstream that answer becomes an
// insight nobody has reason to doubt. Carrying the caveat with the result is
// what lets a consumer label it, discount it, or refuse it.
//
// Caveats are advisory metadata, not errors. A provider attaches them to a
// result it is returning; it does not use them to report a failure.
type QualityCaveat struct {
	// Kind is the machine-readable degradation class. Consumers branch on it.
	Kind QualityKind `json:"kind"`

	// Detail is a short human-readable explanation naming what was degraded
	// and, where the source says so, by how much. Surfaced in prompts and
	// stored alongside results, so it should read as a sentence fragment a
	// reader can act on — "37 of 412 rows withheld", not "flag set".
	Detail string `json:"detail,omitempty"`
}

// String renders the caveat for logs and prompts.
func (c QualityCaveat) String() string {
	if c.Detail == "" {
		return string(c.Kind)
	}
	return fmt.Sprintf("%s: %s", c.Kind, c.Detail)
}

// Degraded reports whether the result carries any quality caveat. A nil
// receiver is not degraded, so the check is safe on a result that was never
// produced.
func (r *QueryResult) Degraded() bool {
	return r != nil && len(r.Quality) > 0
}
