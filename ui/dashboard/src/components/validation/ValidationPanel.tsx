'use client';

// Single entry point for rendering an insight or recommendation's
// validation block. Dispatches to the new (plan-v5) panel when the
// payload carries the new shape, otherwise falls back to the legacy
// card. Pages should only import this component.
//
// When legacy support is removed:
//   1. Delete LegacyValidationCard.tsx and validationShape.ts.
//   2. Replace the body of this component with
//        <NewValidationPanel validation={validation} title={title} />.
// No call site changes.

import { NewValidationPanel } from './NewValidationPanel';
import { LegacyValidationCard } from './LegacyValidationCard';
import { isNewValidation } from './validationShape';
import type { InsightValidation } from '@/lib/api';

export function ValidationPanel({
  validation,
  title,
}: {
  validation: InsightValidation;
  title?: string;
}) {
  if (isNewValidation(validation)) {
    return <NewValidationPanel validation={validation} title={title} />;
  }
  return <LegacyValidationCard validation={validation} />;
}
