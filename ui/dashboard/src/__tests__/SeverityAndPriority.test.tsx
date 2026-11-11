/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { SeverityCell } from '@/components/scoring/SeverityCell';
import { PriorityCell } from '@/components/scoring/PriorityCell';
import { ScoringBreakdown } from '@/components/scoring/ScoringBreakdown';
import { TierTally, countTiers } from '@/components/scoring/TierTally';
import { resetScoringEnginesCache } from '@/components/scoring/useScoringEngines';
import { severityBandOrder } from '@/components/scoring/severityMeta';
import { priorityTierOrder } from '@/components/scoring/priorityMeta';
import type { Scoring, ScoringEngines } from '@/lib/api';
import { api } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: { getScoringEngines: jest.fn() },
}));

// The shipped calibration, including the empty amount bands. The empty array
// is load-bearing in these tests for the same reason it is on the wire: it is
// what explains a share-banded magnitude beside a grounded amount.
const engines: ScoringEngines = {
  current_version: 1,
  engines: [{
    version: 1,
    confidence: {
      bases: { confirmed: 0.95, supported: 0.8, partial: 0.55, rejected: 0.05, unverifiable: 0.2 },
      default: 0.25,
      cutoffs: { high: 0.75, medium: 0.4 },
      floor: 0.05,
      docks: [],
    },
    severity: {
      amount_bands: [],
      share_bands: [{ at: 20, band: 'high' }, { at: 5, band: 'medium' }],
      magnitudes: { high: 1, medium: 0.6, low: 0.25 },
      baseline: 0.6,
      cutoffs: { high: 0.66, medium: 0.33 },
      default_weight: 1,
    },
    priority: {
      tiers: [
        { tier: 'critical', weight: 3, priority: 1 },
        { tier: 'investigate', weight: 2, priority: 2 },
        { tier: 'high', weight: 1, priority: 3 },
        { tier: 'low', weight: 0, priority: 4 },
      ],
      trusted_confidence_bands: ['high', 'medium'],
      matrix: [
        { severity: 'high', confidence: 'trusted', tier: 'critical' },
        { severity: 'high', confidence: 'untrusted', tier: 'investigate' },
        { severity: 'medium', confidence: 'trusted', tier: 'high' },
        { severity: 'medium', confidence: 'untrusted', tier: 'low' },
        { severity: 'low', confidence: 'trusted', tier: 'low' },
        { severity: 'low', confidence: 'untrusted', tier: 'low' },
      ],
      severity_coeff: 0.15,
      confidence_coeff: 0.05,
    },
  }],
};

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

// shareBanded is the usual shape the engine writes with the shipped
// calibration: the magnitude came from a checked share, and a checked amount
// in the home currency is carried alongside as the money at stake.
function shareBanded(partial: Partial<Scoring> = {}): Scoring {
  return {
    confidence_band: 'high',
    confidence_base: 0.95,
    confidence_score: 0.95,
    confidence_reason: 'Warehouse rows matched every claim exactly.',
    magnitude_score: 1,
    severity_band: 'high',
    severity_score: 1,
    severity_reason: 'High — banded on 34.00%, with ₺2.40M at stake. Both checked against warehouse rows.',
    severity_driver: {
      kind: 'share',
      quantified: true,
      figure: { figure_id: 'f1', display: '34.00%', value: 34, unit: 'percent', verdict: 'holds' },
      amount: { figure_id: 'f3', display: '₺2.40M', value: 2400000, unit: 'currency', currency: '₺', verdict: 'holds' },
    },
    area_weight: 1,
    weight_source: { kind: 'default' },
    priority_tier: 'critical',
    priority_score: 3.1975,
    engine_version: 1,
    ...partial,
  };
}

function notQuantified(partial: Partial<Scoring> = {}): Scoring {
  return shareBanded({
    magnitude_score: 0.6,
    severity_band: 'medium',
    severity_score: 0.6,
    severity_reason: 'Medium — not quantified: this finding states no checked figure to measure it by, '
      + 'so the area baseline applies.',
    severity_driver: { kind: 'baseline', quantified: false },
    priority_tier: 'high',
    priority_score: 1.1375,
    ...partial,
  });
}

beforeEach(() => {
  jest.clearAllMocks();
  resetScoringEnginesCache();
  (api.getScoringEngines as jest.Mock).mockResolvedValue(engines);
});

// --- The band and its driver -----------------------------------------------

describe('SeverityCell', () => {
  it('renders the band with the number beside it', () => {
    wrap(<SeverityCell scoring={shareBanded()} severity="high" />);

    expect(screen.getByTestId('severity-band')).toHaveTextContent('High severity');
    expect(screen.getByTestId('severity-band')).toHaveAttribute('data-band', 'high');
    // The AMOUNT is what a reader is shown, not the share that banded it: the
    // money at stake is the number they care about, and the breakdown is
    // where the distinction lives.
    expect(screen.getByTestId('severity-driver')).toHaveTextContent('₺2.40M');
  });

  it('marks a missing magnitude as not quantified rather than as a low band', () => {
    wrap(<SeverityCell scoring={notQuantified()} severity="medium" />);

    // The band is NOT low. "We could not measure this" is not evidence that
    // the thing is small, and banding it low would hide exactly the finding
    // whose numbers a model failed to declare.
    expect(screen.getByTestId('severity-band')).toHaveTextContent('Medium severity');
    expect(screen.getByTestId('severity-driver')).toHaveTextContent('not quantified');
  });

  it('renders a document written before the derivation as a legacy word', () => {
    // No severity_band — an insight from before severity was derived. Its
    // `critical` is the analysis model's own assessment, and nothing
    // recalculates it.
    wrap(<SeverityCell scoring={undefined} severity="critical" />);

    expect(screen.getByTestId('severity-legacy')).toHaveTextContent('legacy');
    expect(screen.queryByTestId('severity-band')).not.toBeInTheDocument();
    expect(screen.getByText('critical')).toBeInTheDocument();
  });

  it('renders a confidence-only sub-document as legacy too', () => {
    // The trap the SeverityCell branch exists for: a document an earlier
    // engine version scored on the confidence axis alone HAS a scoring block
    // and no severity half. Branching on "is there a scoring block" would
    // label its model-written word as derived.
    const confidenceOnly: Scoring = {
      confidence_band: 'high',
      confidence_score: 0.95,
      engine_version: 1,
    };
    wrap(<SeverityCell scoring={confidenceOnly} severity="critical" />);

    expect(screen.getByTestId('severity-legacy')).toBeInTheDocument();
    expect(screen.queryByTestId('severity-band')).not.toBeInTheDocument();
  });
});

// --- The tier --------------------------------------------------------------

describe('PriorityCell', () => {
  it('renders the tier by what a reader should do about it', () => {
    wrap(<PriorityCell scoring={shareBanded()} />);
    const badge = screen.getByTestId('priority-tier');
    expect(badge).toHaveTextContent('Act now');
    expect(badge).toHaveAttribute('data-tier', 'critical');
  });

  it('renders Investigate for a severe finding nothing could check', () => {
    wrap(<PriorityCell scoring={shareBanded({
      confidence_band: 'unverifiable',
      priority_tier: 'investigate',
    })} />);
    expect(screen.getByTestId('priority-tier')).toHaveTextContent('Investigate');
  });

  it('renders nothing but a dash for a document with no tier', () => {
    // Priority is a rollup of two bands, so a document missing the severity
    // half has no cell to be in. A tier here — "low", say — would claim it
    // had been triaged and found unimportant.
    wrap(<PriorityCell scoring={{ confidence_band: 'high', engine_version: 1 }} />);
    expect(screen.getByTestId('priority-ungraded')).toHaveTextContent('—');
    expect(screen.queryByTestId('priority-tier')).not.toBeInTheDocument();
  });
});

// --- The breakdown ---------------------------------------------------------

describe('ScoringBreakdown, severity axis', () => {
  it('shows magnitude, the weight and what set it, the final and the cutoff', async () => {
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="severity" />);

    expect(screen.getByTestId('severity-magnitude')).toHaveTextContent('1.00');
    expect(screen.getByTestId('severity-weight')).toBeInTheDocument();
    expect(screen.getByText(/× 1 weight \(default\)/)).toBeInTheDocument();
    expect(screen.getByTestId('severity-final')).toHaveTextContent('1.00');

    // The cutoff comes from the engine the document says scored it.
    await waitFor(() => {
      expect(screen.getByTestId('severity-cutoff')).toHaveTextContent('at or above 66%');
    });
  });

  it('shows the checked figure behind the number, with its verdict', () => {
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="severity" />);

    // The figure that banded it, and the amount, as two separate lines —
    // because with amount bands unconfigured they are two different figures,
    // and conflating them would claim the money set the band.
    expect(screen.getByTestId('severity-banded-by')).toHaveTextContent('34.00% (holds)');
    expect(screen.getByTestId('severity-amount')).toHaveTextContent('₺2.40M (holds)');
  });

  it('explains why a grounded amount did not set the band', async () => {
    // The case a reader would otherwise read as a bug: a finding with a real
    // ₺2.4M banded off a share. An absolute money cutoff is a property of a
    // business, so the engine ships none, and the breakdown has to say so.
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="severity" />);

    await waitFor(() => {
      expect(screen.getByTestId('severity-no-amount-bands'))
        .toHaveTextContent(/no amount thresholds are configured/i);
    });
  });

  it('names the area baseline when nothing could be measured', () => {
    wrap(<ScoringBreakdown scoring={notQuantified()} axis="severity" />);

    expect(screen.getByText(/area baseline — not quantified/i)).toBeInTheDocument();
    expect(screen.queryByTestId('severity-banded-by')).not.toBeInTheDocument();
  });

  it('omits the cutoff rather than faking it for an unknown engine version', async () => {
    // A document scored by a newer agent than this API. Everything a reader
    // needs is on the document; only the cutoff line is not, so that line
    // goes and nothing else does.
    wrap(<ScoringBreakdown scoring={shareBanded({ engine_version: 99 })} axis="severity" />);

    await waitFor(() => expect(api.getScoringEngines).toHaveBeenCalled());
    expect(screen.queryByTestId('severity-cutoff')).not.toBeInTheDocument();
    expect(screen.getByTestId('severity-final')).toHaveTextContent('1.00');
  });

  it('survives a calibration it cannot fetch', async () => {
    (api.getScoringEngines as jest.Mock).mockRejectedValue(new Error('offline'));
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="severity" />);

    await waitFor(() => expect(api.getScoringEngines).toHaveBeenCalled());
    expect(screen.getByTestId('severity-magnitude')).toHaveTextContent('1.00');
    expect(screen.queryByTestId('severity-cutoff')).not.toBeInTheDocument();
  });

  it('shows the weight source when a category set it', () => {
    wrap(<ScoringBreakdown
      scoring={shareBanded({ area_weight: 2, weight_source: { kind: 'category', category: 'compliance' } })}
      axis="severity"
    />);
    expect(screen.getByText(/× 2 weight \(compliance\)/)).toBeInTheDocument();
  });
});

describe('ScoringBreakdown, priority axis', () => {
  it('shows the matrix cell and the two bands that selected it', () => {
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="priority" />);

    expect(screen.getByTestId('priority-label')).toHaveTextContent('Act now');
    expect(screen.getByTestId('priority-cell'))
      .toHaveTextContent('high severity × evidence that held up');
  });

  it('names the untrusted column for a finding nothing could check', () => {
    wrap(<ScoringBreakdown
      scoring={shareBanded({ confidence_band: 'unverifiable', priority_tier: 'investigate' })}
      axis="priority"
    />);
    expect(screen.getByTestId('priority-cell'))
      .toHaveTextContent(/never checked|did not hold up/);
  });

  it('renders all three axes on a detail page', () => {
    wrap(<ScoringBreakdown scoring={shareBanded()} axis="all" />);

    expect(screen.getByTestId('priority-label')).toBeInTheDocument();
    expect(screen.getByTestId('severity-final')).toBeInTheDocument();
    expect(screen.getByTestId('scoring-final')).toBeInTheDocument();
    expect(screen.getByTestId('scoring-engine')).toHaveTextContent('v1');
  });

  it('skips the severity and priority sections on a confidence-only document', () => {
    wrap(<ScoringBreakdown
      scoring={{ confidence_band: 'high', confidence_base: 0.95, confidence_score: 0.95, engine_version: 1 }}
      axis="all"
    />);

    expect(screen.getByTestId('scoring-final')).toBeInTheDocument();
    expect(screen.queryByTestId('severity-final')).not.toBeInTheDocument();
    expect(screen.queryByTestId('priority-label')).not.toBeInTheDocument();
  });
});

// --- The hover, which is how a list row reaches a breakdown ----------------

describe('the badges open their own axis', () => {
  it('opens the severity derivation from the severity badge', async () => {
    const user = userEvent.setup();
    wrap(<SeverityCell scoring={shareBanded()} severity="high" />);

    await user.hover(screen.getByTestId('severity-band'));
    await waitFor(() => {
      expect(screen.getByText('How this was scored')).toBeInTheDocument();
    });
    expect(screen.getByTestId('severity-magnitude')).toBeInTheDocument();
  });

  it('opens the matrix cell from the priority badge', async () => {
    const user = userEvent.setup();
    wrap(<PriorityCell scoring={shareBanded()} />);

    await user.hover(screen.getByTestId('priority-tier'));
    await waitFor(() => {
      expect(screen.getByTestId('priority-cell')).toBeInTheDocument();
    });
  });
});

// --- The per-run tally -----------------------------------------------------

describe('TierTally', () => {
  const insight = (tier?: string, confidence?: string) => ({
    evidence_scoring: tier || confidence
      ? ({
        priority_tier: tier as never,
        confidence_band: confidence as never,
        engine_version: 1,
      } as Scoring)
      : undefined,
  });

  it('counts the four tiers plus the unverifiable band', () => {
    const counts = countTiers([
      insight('critical', 'high'),
      insight('critical', 'medium'),
      insight('investigate', 'unverifiable'),
      insight('high', 'high'),
      insight('low', 'low'),
    ]);

    expect(counts.tiers).toEqual([
      { tier: 'critical', count: 2 },
      { tier: 'investigate', count: 1 },
      { tier: 'high', count: 1 },
      { tier: 'low', count: 1 },
    ]);
    // Beside the tiers rather than inside them: a tier says what to do, this
    // says how much of the run nothing could check.
    expect(counts.unverifiable).toBe(1);
    expect(counts.ungraded).toBe(0);
  });

  it('reports legacy insights as ungraded rather than folding them into a tier', () => {
    // The run header is a statement about this run. Mapping an old severity
    // word onto a tier the document was never triaged by would overstate what
    // is known about it.
    const counts = countTiers([insight('critical', 'high'), insight(), insight()]);
    expect(counts.ungraded).toBe(2);
    expect(counts.tiers.find(t => t.tier === 'critical')?.count).toBe(1);
  });

  it('renders the tally with a zero column kept rather than dropped', () => {
    wrap(<TierTally insights={[insight('critical', 'high')]} />);

    expect(screen.getByTestId('tier-tally')).toBeInTheDocument();
    expect(screen.getByTestId('tier-tally-critical')).toHaveTextContent('1 Act now');
    // Zero is information, and a tally whose columns come and go cannot be
    // compared between two runs at a glance.
    expect(screen.getByTestId('tier-tally-low')).toHaveTextContent('0 Low');
  });

  it('renders nothing when no insight is graded', () => {
    // A run from before the scorer, or one that produced no insights. A row of
    // zeroes would say less than nothing.
    wrap(<TierTally insights={[insight(), insight()]} />);
    expect(screen.queryByTestId('tier-tally')).not.toBeInTheDocument();
  });
});

// --- The orderings the lists sort by ---------------------------------------

describe('client-side ordering', () => {
  it('ranks the tiers critical > investigate > high > low, and an ungraded document last', () => {
    const order = ['critical', 'investigate', 'high', 'low'].map(priorityTierOrder);
    expect(order).toEqual([...order].sort((a, b) => a - b));
    expect(priorityTierOrder(undefined)).toBeGreaterThan(priorityTierOrder('low'));
    // A tier from a newer engine sorts with the ungraded rather than at the
    // top: this build cannot know where it belongs.
    expect(priorityTierOrder('something_newer')).toBeGreaterThan(priorityTierOrder('low'));
  });

  it('ties a legacy critical severity with a derived high', () => {
    // The same tie the ledger's own rank makes, and for the same reason: a new
    // finding must not sort below an old one that used the older word for the
    // same thing.
    expect(severityBandOrder('critical')).toBe(severityBandOrder('high'));
    expect(severityBandOrder('high')).toBeLessThan(severityBandOrder('medium'));
    expect(severityBandOrder('medium')).toBeLessThan(severityBandOrder('low'));
    expect(severityBandOrder(undefined)).toBeGreaterThan(severityBandOrder('low'));
  });
});
