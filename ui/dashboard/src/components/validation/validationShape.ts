// Shape-detector for the two validation payload shapes the dashboard
// currently has to render: the plan-v5 LLM-native shape (verifier +
// refuter + combined + claim_verdicts) and the legacy single-status shape
// from the pre-plan validators.
//
// A document carries the *new* shape when any of the new fields is
// present. Documents persisted under the new pipeline always set
// `combined` (it's the canonical status); some older transitional docs
// may carry both shapes (the backend backfills legacy `status` from
// `combined` for the old UI). We dispatch on the most reliable signal:
// presence of `verifier` or `refuter` or `combined`.
//
// The parameter is typed structurally so both `InsightValidation` (the
// doc-attached summary) and `ValidationLogEntry` (the per-area log row)
// can be inspected without an adapter. Both wire types carry the same
// optional new-shape fields.
//
// When legacy support is removed, this whole module + the legacy branch
// in ValidationPanel.tsx and the LegacyValidationCard.tsx file can be
// deleted in one commit. No other call site touches the shape.

import type { StructuredVerdict, ValidationStatus } from '@/lib/api';

type ValidationLike = {
  verifier?: StructuredVerdict;
  refuter?: StructuredVerdict;
  combined?: ValidationStatus;
};

export function isNewValidation(v: ValidationLike): boolean {
  return v.verifier != null || v.refuter != null || v.combined != null;
}

export function isLegacyValidation(v: ValidationLike): boolean {
  return !isNewValidation(v);
}
