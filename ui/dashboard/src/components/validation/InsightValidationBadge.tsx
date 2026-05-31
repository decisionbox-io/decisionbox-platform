'use client';

import { VerdictBadge } from './VerdictBadge';
import type { InsightValidation, ValidationStatus } from '@/lib/api';

// Inline validation badge for list surfaces (Insights tab, discovery
// results, search results). It reuses VerdictBadge so colours and
// labels stay consistent with the insight/recommendation detail views.
//
// resolveValidationStatus is the single place that decides which status
// a list row shows: the new-shape `combined` verdict is canonical;
// legacy docs fall back to their single `status`; and a doc that was
// never validated (or a payload that omitted validation) renders the
// neutral "Unverified" state so the column reads consistently across
// every row.

export function resolveValidationStatus(
  validation: InsightValidation | undefined,
): ValidationStatus {
  if (validation?.combined) return validation.combined;
  if (validation?.status) return validation.status as ValidationStatus;
  return 'unverified';
}

// Sort/group rank for the Insights-tab "Validation" sort and filter.
// Trustworthy positive verdicts first, then mixed, then the negative
// verdict, then the "agent never ran" states and the neutral fallback
// last. Lower sorts earlier.
const VALIDATION_RANK: Record<string, number> = {
  confirmed: 0,
  supported: 1,
  partial: 2,
  unverifiable: 3,
  adjusted: 4,
  rejected: 5,
  error: 6,
  validation_disabled: 7,
  skipped_budget_cap: 8,
  unverified: 9,
};

export function validationSortRank(
  validation: InsightValidation | undefined,
): number {
  return VALIDATION_RANK[resolveValidationStatus(validation)] ?? 99;
}

export function InsightValidationBadge({
  validation,
  size = 'xs',
}: {
  validation?: InsightValidation;
  size?: 'xs' | 'sm' | 'md';
}) {
  return <VerdictBadge status={resolveValidationStatus(validation)} size={size} />;
}
