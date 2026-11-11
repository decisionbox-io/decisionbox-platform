// Decision-maker copy for the five confidence bands.
//
// Labels and tone only. The REASON a document is in its band, and the words
// for each deduction, are stored on the document itself and rendered verbatim
// — the engine computed them, so it is the engine that gets to phrase them.
// Re-wording either here would be a second answer to "why is this medium",
// drifting from the first the moment one of them changed.
//
// `tone` maps to the design-token semantic colour pairs in styles/tokens.css,
// through the same StatusTone vocabulary the verdict badges use. Never inline
// a hex code here.

import type { StatusTone } from '@/components/validation/statusMeta';

export type ConfidenceBand =
  | 'high'
  | 'medium'
  | 'low'
  | 'not_confirmed'
  | 'unverifiable';

export interface ConfidenceMeta {
  label: string;
  // A short gloss on what the BAND means, independent of the document. The
  // document's own reason says why this document is in it.
  gloss: string;
  tone: StatusTone;
}

const META: Record<string, ConfidenceMeta> = {
  high: {
    label: 'High confidence',
    gloss: 'Checked against warehouse rows and held up.',
    tone: 'green',
  },
  medium: {
    label: 'Medium confidence',
    gloss: 'Checked, with something against it.',
    tone: 'amber',
  },
  low: {
    label: 'Low confidence',
    gloss: 'Checked, and the evidence is weak or contradicts it.',
    tone: 'red',
  },
  // Terminal, and deliberately not "low": nothing was checked, which is a
  // different statement from something having been checked and found weak.
  not_confirmed: {
    label: 'Not confirmed',
    gloss: 'Not checked against warehouse rows.',
    tone: 'grey',
  },
  unverifiable: {
    label: 'Unverifiable',
    gloss: 'Nothing in the cited evidence could check this.',
    tone: 'grey',
  },
};

const FALLBACK: ConfidenceMeta = {
  label: 'Unknown',
  gloss: 'Band not recognised — this document was scored by a newer version.',
  tone: 'grey',
};

export function confidenceMeta(band: string | undefined): ConfidenceMeta {
  if (!band) return FALLBACK;
  return META[band] ?? FALLBACK;
}

// formatScore renders a score as a whole percentage, which is how every other
// confidence surface in the dashboard has always written it.
export function formatScore(score: number): string {
  return `${Math.round(score * 100)}%`;
}

// formatDock renders a deduction. The stored amount is the positive magnitude
// charged — the breakdown's arithmetic is base minus the sum of them — so the
// minus sign belongs to the rendering rather than to the data.
export function formatDock(amount: number): string {
  return `−${Math.round(amount * 100)}`;
}
