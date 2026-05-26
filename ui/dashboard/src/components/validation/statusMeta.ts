// Decision-maker copy for the seven new validation statuses (plus the
// two legacy statuses we still need to render when looking at old
// discoveries). Centralised so a copy change in one place propagates
// everywhere a verdict is shown.
//
// `label` is the short pill text (~12 chars max).
// `tagline` is the one-line decision-friendly explanation rendered
//   next to the badge — phrased in plain English, not jargon.
// `tone` maps to the design-token semantic colour pair (bg + text)
//   defined in styles/tokens.css. Never inline hex codes here.

import type { ValidationStatus } from '@/lib/api';

export type StatusTone = 'green' | 'amber' | 'red' | 'grey' | 'blue';

export interface StatusMeta {
  label: string;
  tagline: string;
  tone: StatusTone;
}

const META: Record<string, StatusMeta> = {
  // --- New shape ---
  confirmed: {
    label: 'Confirmed',
    tagline: 'Every claim matched the evidence exactly.',
    tone: 'green',
  },
  supported: {
    label: 'Supported',
    tagline: 'Claims are consistent with the evidence.',
    tone: 'green',
  },
  partial: {
    label: 'Partial',
    tagline: 'Some claims hold up, others are mixed.',
    tone: 'amber',
  },
  rejected: {
    label: 'Rejected',
    tagline: 'Evidence disagrees with at least one headline claim.',
    tone: 'red',
  },
  unverifiable: {
    label: 'Unverifiable',
    tagline: 'Cited evidence was not available to check.',
    tone: 'amber',
  },
  validation_disabled: {
    label: 'Disabled',
    tagline: 'Validation was turned off for this run.',
    tone: 'grey',
  },
  skipped_budget_cap: {
    label: 'Skipped',
    tagline: 'Validation budget was reached before this item.',
    tone: 'grey',
  },
  // --- Legacy (kept until legacy docs are migrated) ---
  adjusted: {
    label: 'Adjusted',
    tagline: 'Counts were revised after re-running the query.',
    tone: 'amber',
  },
  unverified: {
    label: 'Unverified',
    tagline: 'No validation run for this insight.',
    tone: 'grey',
  },
  error: {
    label: 'Error',
    tagline: 'Validation could not complete due to an error.',
    tone: 'red',
  },
};

const FALLBACK: StatusMeta = {
  label: 'Unknown',
  tagline: 'Verdict not recognised — see raw status below.',
  tone: 'grey',
};

export function statusMeta(status: ValidationStatus | string | undefined): StatusMeta {
  if (!status) return FALLBACK;
  return META[status] ?? FALLBACK;
}

// Mantine Badge takes a named colour; map our semantic tones to its
// palette. (Mantine's "gray" matches our `grey` token bg.)
export function toneToMantineColor(tone: StatusTone): string {
  switch (tone) {
    case 'green': return 'green';
    case 'amber': return 'yellow';
    case 'red': return 'red';
    case 'blue': return 'blue';
    case 'grey':
    default: return 'gray';
  }
}
