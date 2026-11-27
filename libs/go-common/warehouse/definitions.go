package warehouse

import "context"

// Kinds of BusinessDefinition. Distinct from the catalog's ItemKind values:
// an indexed definition sits beside a source's tables, and search must be
// able to tell it apart from a cube's metric or dimension.
const (
	DefinitionMeasure   = "defined measure"
	DefinitionDimension = "defined dimension"
)

// IsDefinitionKind reports whether kind is a BusinessDefinition kind.
func IsDefinitionKind(kind string) bool {
	return kind == DefinitionMeasure || kind == DefinitionDimension
}

// BusinessDefinition is a named measure or dimension that the SOURCE defines,
// with the source's own formula for it — the customer's definition of
// "revenue" or "margin", rather than one the model has to guess.
type BusinessDefinition struct {
	// Name is what a query references, verbatim.
	Name string
	// Kind is DefinitionMeasure or DefinitionDimension.
	Kind string
	// Expression is the source's own formula, in the source's query language.
	Expression string
	// Description is the source's own description, or "".
	Description string
}

// DefinitionSource is an optional provider interface for a source that carries
// the customer's own named measures and dimensions: a BI tool's master items,
// a semantic layer's metrics. A provider that does not implement it is
// unaffected.
//
// It is additive to the provider's tables, unlike CatalogSource, which
// describes a source that HAS no tables and is indexed instead of them. Schema
// indexing reads these alongside the tables so search surfaces them with their
// kind, and the model can reference a definition by name instead of
// re-deriving its formula.
type DefinitionSource interface {
	BusinessDefinitions(ctx context.Context) ([]BusinessDefinition, error)
}

// AsDefinitionSource reports whether p carries business definitions, looking
// through middleware (FindCapability): reading definitions runs no query a
// middleware could govern — it lists named formulas the source already exposes
// to anyone who can read it.
func AsDefinitionSource(p Provider) (DefinitionSource, bool) {
	return FindCapability[DefinitionSource](p)
}
