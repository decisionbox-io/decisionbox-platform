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
