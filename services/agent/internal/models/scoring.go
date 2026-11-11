package models

import gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"

// The scoring types live in libs/go-common/models for the reason the figure and
// quantifier ones do: the API decodes a stored discovery into its own mirror of
// Insight and cannot import this internal package, so a type kept here loses
// the derived grade to BSON before any client sees it. Aliased rather than
// re-declared so the two definitions cannot drift -- and so that grounding the
// severity half later adds logic in one place instead of a field on four
// structs.
type (
	Scoring        = gomodels.Scoring
	ConfidenceDock = gomodels.ConfidenceDock
	SeverityDriver = gomodels.SeverityDriver
	SeverityFigure = gomodels.SeverityFigure
	WeightSource   = gomodels.WeightSource
)

// The confidence band vocabulary, re-exported so callers in this service keep
// one import.
const (
	ConfidenceBandHigh         = gomodels.ConfidenceBandHigh
	ConfidenceBandMedium       = gomodels.ConfidenceBandMedium
	ConfidenceBandLow          = gomodels.ConfidenceBandLow
	ConfidenceBandNotConfirmed = gomodels.ConfidenceBandNotConfirmed
	ConfidenceBandUnverifiable = gomodels.ConfidenceBandUnverifiable
)

// The severity band and priority tier vocabularies, re-exported for the same
// reason. The agent's own code branches on these -- the run-completed
// notification counts by tier and the top-insight briefs sort by it -- so a
// literal at those call sites is a literal that can drift from the engine.
const (
	SeverityBandHigh   = gomodels.SeverityBandHigh
	SeverityBandMedium = gomodels.SeverityBandMedium
	SeverityBandLow    = gomodels.SeverityBandLow

	SeverityDriverAmount   = gomodels.SeverityDriverAmount
	SeverityDriverShare    = gomodels.SeverityDriverShare
	SeverityDriverBaseline = gomodels.SeverityDriverBaseline

	PriorityTierCritical    = gomodels.PriorityTierCritical
	PriorityTierInvestigate = gomodels.PriorityTierInvestigate
	PriorityTierHigh        = gomodels.PriorityTierHigh
	PriorityTierLow         = gomodels.PriorityTierLow
)
