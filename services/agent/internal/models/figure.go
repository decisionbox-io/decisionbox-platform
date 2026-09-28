package models

import gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"

// The figure types live in libs/go-common/models for the reason the quantifier ones do:
// the API decodes a stored discovery into its own mirror of Insight and cannot import this
// internal package, so a type kept here loses the audit trail to BSON before any client
// sees it. Aliased rather than re-declared so the two definitions cannot drift.
type (
	Figure           = gomodels.Figure
	FigureVerdict    = gomodels.FigureVerdict
	FigureCorrection = gomodels.FigureCorrection
	FigureTemplate   = gomodels.FigureTemplate
)

// Kinds, units, scales and verdict statuses, re-exported so callers in this service keep
// one import.
const (
	FigureCell  = gomodels.FigureCell
	FigureSum   = gomodels.FigureSum
	FigureCount = gomodels.FigureCount
	FigureRatio = gomodels.FigureRatio
	FigureDiff  = gomodels.FigureDiff

	UnitCount    = gomodels.UnitCount
	UnitCurrency = gomodels.UnitCurrency
	UnitPercent  = gomodels.UnitPercent
	UnitMultiple = gomodels.UnitMultiple
	UnitDays     = gomodels.UnitDays
	UnitPlain    = gomodels.UnitPlain

	ScaleNone      = gomodels.ScaleNone
	ScaleThousands = gomodels.ScaleThousands
	ScaleMillions  = gomodels.ScaleMillions
	ScaleBillions  = gomodels.ScaleBillions

	FigureHolds       = gomodels.FigureHolds
	FigureFails       = gomodels.FigureFails
	FigureUndecidable = gomodels.FigureUndecidable
)
