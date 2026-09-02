package warehouse

import (
	"context"
	"fmt"
)

// SourceShape describes how a source organises what can be queried. It is not
// cosmetic bookkeeping: it decides what a correct query even looks like, and
// downstream consumers (prompt construction, pack generation, exploration)
// branch on it rather than guessing from the provider name.
type SourceShape string

const (
	// ShapeEntities is the table/object model: named collections of rows with
	// columns, selected from and joined. Every SQL warehouse is this shape, as
	// is an object-based API such as a CRM.
	ShapeEntities SourceShape = "entities"

	// ShapeCube is the metric/dimension model: there are no tables and no rows
	// to select from. A query is a choice of metrics, dimensions, a date range
	// and optional filters, and the source computes the result. A web-analytics
	// property is this shape.
	//
	// Modelling a cube as pseudo-tables would lie about capability — the agent
	// would generate joins and filters the source cannot honour, and the
	// failure would surface as a confusing query-time error rather than an
	// honest "this source can't do that".
	ShapeCube SourceShape = "cube"
)

// NativeQuery is a query expressed in a source's own language.
//
// SQL sources carry the statement in Text and leave Payload nil. A source
// whose query is a structured request rather than a string — a report
// specification, for instance — carries that request in Payload and may leave
// Text empty, or set it to a human-readable rendering for logs and prompts.
//
// The two fields exist because the repair loop, the debug log and the stored
// query history all want something printable, while the executing provider
// wants the real payload. Collapsing them to a string would force structured
// sources to serialise and re-parse their own requests.
type NativeQuery struct {
	// Text is the query as text: the SQL statement, or a readable rendering of
	// a structured payload. Never empty for SQL sources.
	Text string

	// Payload is the provider-native structured request, or nil when Text is
	// the whole query. A provider that sets this is responsible for type-
	// asserting it back in RunQuery.
	Payload any
}

// SQLQuery builds a NativeQuery for a plain SQL statement.
func SQLQuery(sql string) NativeQuery { return NativeQuery{Text: sql} }

// IsStructured reports whether the query carries a payload the executing
// provider must interpret, rather than text it can run directly.
func (q NativeQuery) IsStructured() bool { return q.Payload != nil }

// String renders the query for logs, prompts and stored history. It never
// returns the payload, so a provider that wants its structured request to be
// legible must render it into Text.
func (q NativeQuery) String() string { return q.Text }

// QueryRunner is the narrow seam the query-execution loop actually depends on:
// generate a query in the source's language, run it, and on failure ask for a
// repair prompt in that same language. Everything else the SQL Provider
// interface offers — dataset listing, schema introspection, identifier
// quoting — is irrelevant to executing a query and is deliberately absent.
//
// SQL providers do not implement this directly; AsQueryRunner adapts them, so
// the SQL path is unchanged. A non-SQL adapter implements QueryRunner itself
// and needs none of the table-shaped surface.
type QueryRunner interface {
	// RunQuery executes a query in the source's native language.
	RunQuery(ctx context.Context, q NativeQuery) (*QueryResult, error)

	// QueryLanguage names the language queries are written in — "SQL" and its
	// dialects, or a source's own request format. Used to tell the model what
	// it is writing.
	QueryLanguage() string

	// QueryFixPrompt returns the repair-prompt template for this language, or
	// "" when the source offers none. This is the generalisation of
	// Provider.SQLFixPrompt; the same placeholder contract applies.
	QueryFixPrompt() string
}

// AsQueryRunner returns p as a QueryRunner. A provider that implements the
// interface itself is returned unchanged; any other provider is wrapped in an
// adapter that maps RunQuery onto Query, QueryLanguage onto SQLDialect, and
// QueryFixPrompt onto SQLFixPrompt.
//
// The adapter is what keeps this extraction behaviour-preserving: every
// existing SQL provider — including the ones registered from the enterprise
// plugin repo, which this package cannot see — keeps working with no change
// and no compile break.
func AsQueryRunner(p Provider) QueryRunner {
	if r, ok := p.(QueryRunner); ok {
		return r
	}
	return sqlRunner{p: p}
}

// sqlRunner adapts a SQL Provider to the QueryRunner seam.
type sqlRunner struct{ p Provider }

// RunQuery forwards the query text to Provider.Query with nil params, exactly
// as the executor did before the seam existed.
//
// A structured payload is refused rather than ignored. Running Text alone
// would execute something other than what the caller asked for — Text is only
// a readable rendering when a payload is present, and may be empty — and a SQL
// provider cannot interpret the payload. Dropping it would produce a
// well-formed result for the wrong question, which is the failure mode this
// seam exists to prevent; a caller that reaches here with a payload has paired
// a non-SQL query shape with a SQL provider.
func (r sqlRunner) RunQuery(ctx context.Context, q NativeQuery) (*QueryResult, error) {
	if q.IsStructured() {
		return nil, fmt.Errorf("warehouse: %T cannot execute a structured query payload (%T); "+
			"a structured query needs a provider that implements QueryRunner", r.p, q.Payload)
	}
	return r.p.Query(ctx, q.Text, nil)
}

func (r sqlRunner) QueryLanguage() string  { return r.p.SQLDialect() }
func (r sqlRunner) QueryFixPrompt() string { return r.p.SQLFixPrompt() }

// Unwrap exposes the adapted Provider so callers that still need the
// table-shaped surface (schema discovery, identifier quoting) can reach it
// without keeping a second reference alongside the runner.
func (r sqlRunner) Unwrap() Provider { return r.p }

// NonSQLLanguage names the language p's queries are written in, but only when
// that is not SQL. A warehouse returns "".
//
// It answers "are this source's queries the SQL the Provider interface
// assumes?" without opening a connection or pattern-matching a dialect label,
// and it answers it from a declaration rather than an inference: a provider
// implements QueryRunner precisely because its queries are not SQL, since a
// SQL provider gains nothing by implementing a seam the adapter already maps
// onto its SQL surface.
//
// The empty return is what lets a caller pass this straight through to a
// prompt or an instruction that must stay unchanged for every warehouse.
func NonSQLLanguage(p Provider) string {
	r, ok := p.(QueryRunner)
	if !ok {
		return ""
	}
	return r.QueryLanguage()
}

// Anchoring returns a pointer to v, for declaring ProviderMeta.CanAnchor.
//
// Only a provider that cannot anchor needs to say so:
//
//	CanAnchor: warehouse.Anchoring(false)
func Anchoring(v bool) *bool { return &v }

// Capability is the source-capability descriptor: what language a source's
// queries are written in, how it is shaped, and whether it can carry a project
// by itself. It is declared once at provider registration and travels from
// there to everything that has to reason about a source without opening a
// connection to it — prompt construction, routing, pack generation.
//
// Every field is optional and defaults to what was true before the descriptor
// existed, so a provider registered before this type — including the
// enterprise-only drivers this repo cannot see — stays correct unedited.
type Capability struct {
	// QueryLanguage names the language queries against this source are written
	// in. Empty resolves via Language().
	QueryLanguage string `json:"query_language,omitempty"`

	// Shape is how the source organises what can be queried. Empty resolves to
	// ShapeEntities via EffectiveShape().
	Shape SourceShape `json:"shape,omitempty"`

	// CanAnchor declares whether a source of this type can carry a project by
	// itself. Nil resolves to true via Anchors(); declare it only to say false,
	// using Anchoring(false).
	CanAnchor *bool `json:"can_anchor,omitempty"`
}

// Language returns the declared query language, defaulting to "SQL".
func (c Capability) Language() string {
	if c.QueryLanguage != "" {
		return c.QueryLanguage
	}
	return "SQL"
}

// EffectiveShape returns the declared source shape, defaulting to
// ShapeEntities. The default is safe for an undeclared source because every
// source that existed before shape did is table-shaped; a cube source cannot
// be introduced without declaring itself one, since nothing else about it
// would work.
func (c Capability) EffectiveShape() SourceShape {
	if c.Shape != "" {
		return c.Shape
	}
	return ShapeEntities
}

// Anchors reports whether a source of this type can carry a project by itself
// — a system of record rather than a system of observation.
//
// Undeclared means true. That direction is deliberate: a warehouse or CRM is
// the system of record by construction, so the sources that existed before
// this flag are all anchoring, and a source that forgets to declare keeps
// working. The failure mode of the opposite default would be silent — an
// existing datasource quietly becoming ineligible to be a project's only
// source, refused with no error anyone would connect to a missing field.
//
// Only a source whose value is purely correlative declares CanAnchor false.
func (c Capability) Anchors() bool {
	return c.CanAnchor == nil || *c.CanAnchor
}

// Language returns the query language for this provider: the declared
// QueryLanguage, falling back to the display Dialect, and finally to "SQL".
//
// The Dialect fallback is what lets every already-registered SQL provider
// carry a correct language without being edited.
func (m ProviderMeta) Language() string {
	if m.QueryLanguage != "" {
		return m.QueryLanguage
	}
	if m.Dialect != "" {
		return m.Dialect
	}
	return "SQL"
}

// EffectiveAnchoring resolves whether one connected datasource may carry a
// project by itself, from its provider's capability and the per-datasource
// override.
//
// The two inputs answer different questions and compose one way only.
// `CanAnchor` on the provider is what a source of this TYPE is capable of, and
// it is a ceiling: a provider that cannot anchor can never be promoted, because
// promotion would be a claim about the data that isn't true. The override is
// what THIS customer's datasource is being used as, and it may only demote —
// saying "our warehouse already holds this, treat it as enrichment" is a
// legitimate statement about where the records actually live.
//
// override nil means "not set", which resolves to the provider's capability.
//
// An unregistered provider resolves to anchoring. That is the safe reading
// rather than the permissive one: a binary that has not linked a provider
// cannot construct it either, so such a datasource cannot be connected at all —
// the project is broken in an obvious way long before anchoring is consulted.
// The alternative default would make anchoring depend on which providers a
// binary happens to blank-import, which is invisible at the call site.
func EffectiveAnchoring(provider string, override *bool) bool {
	if meta, ok := GetProviderMeta(provider); ok && !meta.Anchors() {
		return false
	}
	if override != nil {
		return *override
	}
	return true
}

// AnchoringOverrideAllowed reports whether `want` is a legal override for a
// datasource of this provider type. Only demotion is legal; promoting a
// provider that declares CanAnchor false is refused rather than ignored, so the
// caller can tell the user why instead of silently storing a value that does
// nothing.
func AnchoringOverrideAllowed(provider string, want bool) bool {
	if !want {
		return true // demotion is always legal
	}
	meta, ok := GetProviderMeta(provider)
	return !ok || meta.Anchors()
}
