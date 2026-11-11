// Decision-maker copy for the three severity bands.
//
// Labels and tone only, for the reason confidenceMeta carries only those: the
// REASON a document is in its band is stored on the document itself and is
// rendered verbatim. The engine computed it, so the engine gets to phrase it;
// re-wording it here would be a second answer to "why is this medium",
// drifting from the first the moment either changed.
//
// There are three bands and no `critical`. That word was the top severity
// until severity stopped being the analysis model's own; it is the top
// PRIORITY tier now, which is what the matrix calls it. A document written
// before the change carries `critical` in its own `severity` field and is
// rendered as a legacy value, never re-read as a tier.

import type { StatusTone } from '@/components/validation/statusMeta';
import type { SeverityBand, SeverityDriver, SeverityFigure } from '@/lib/api';

export interface SeverityMeta {
  label: string;
  // A short gloss on what the BAND means, independent of the document. The
  // document's own reason says why this document is in it.
  gloss: string;
  tone: StatusTone;
}

const META: Record<string, SeverityMeta> = {
  high: {
    label: 'High severity',
    gloss: 'The checked figures behind this put it near the top of what a run finds.',
    tone: 'red',
  },
  medium: {
    label: 'Medium severity',
    gloss: 'Material, on the figures the platform re-checked.',
    tone: 'amber',
  },
  low: {
    label: 'Low severity',
    gloss: 'Small, on the figures the platform re-checked.',
    tone: 'green',
  },
};

const FALLBACK: SeverityMeta = {
  label: 'Unknown severity',
  gloss: 'Band not recognised — this document was scored by a newer version.',
  tone: 'grey',
};

export function severityMeta(band: string | undefined): SeverityMeta {
  if (!band) return FALLBACK;
  return META[band] ?? FALLBACK;
}

// driverSummary is the one-line driver a list row shows beside the band: the
// number, and nothing else.
//
// The amount comes first when there is one, because it is the figure a reader
// cares about — and with amount bands unconfigured it is usually NOT the
// figure that banded the magnitude, which is what the breakdown explains. A
// row has no space for that distinction; the hover does.
export function driverSummary(driver: SeverityDriver | undefined): string {
  if (!driver) return '';
  if (driver.amount?.display) return driver.amount.display;
  if (driver.figure?.display) return driver.figure.display;
  if (driver.kind === 'baseline') return 'not quantified';
  return '';
}

// notQuantified says whether the band came from the area baseline rather than
// from a figure.
//
// Rendered as a marker, never as a low band: "we could not measure this" is
// not evidence that the thing is small, and a reader shown a Medium label with
// no number beside it is entitled to know which of the two it is.
export function notQuantified(driver: SeverityDriver | undefined): boolean {
  return driver?.quantified === false;
}

// figureWords renders a driver figure with its verdict, for the breakdown.
// The verdict comes off the document rather than being asserted here.
export function figureWords(figure: SeverityFigure | undefined): string {
  if (!figure) return '';
  const shown = figure.display ?? (figure.figure_id ? `figure ${figure.figure_id}` : '');
  if (!shown) return '';
  return figure.verdict ? `${shown} (${figure.verdict})` : shown;
}

// severityBandOrder ranks the bands for a client-side sort, highest first.
// `critical` is included for legacy documents, tied with `high` — the same tie
// the ledger's own rank makes, and for the same reason: a new finding must not
// sort below an old one that used the older word for the same thing.
const BAND_ORDER: Record<string, number> = {
  critical: 0,
  high: 0,
  medium: 1,
  low: 2,
  info: 3,
};

export function severityBandOrder(band: string | undefined): number {
  if (!band) return 9;
  return BAND_ORDER[band] ?? 9;
}

// isLegacySeverityWord says whether a severity string is one this build no
// longer writes. Only `critical` qualifies: it is the single word the
// vocabulary change moved.
export function isLegacySeverityWord(severity: string | undefined): boolean {
  return severity === 'critical';
}

export type { SeverityBand };
