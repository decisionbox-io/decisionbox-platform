/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { ConfidenceCell } from '@/components/scoring/ConfidenceCell';
import { ScoringBreakdown } from '@/components/scoring/ScoringBreakdown';
import { resetScoringEnginesCache } from '@/components/scoring/useScoringEngines';
import type { Scoring, ScoringEngines } from '@/lib/api';
import { api } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: { getScoringEngines: jest.fn() },
}));

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
    // Carried so the fixture is a whole ScoringEngine rather than the
    // confidence half of one. Nothing in this file reads it; a fixture that
    // does not match its own type is a trap for whoever adds the next case.
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
      matrix: [],
      severity_coeff: 0.15,
      confidence_coeff: 0.05,
    },
  }],
};

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

// scored builds a sub-document the way the engine writes one: the amounts
// always sum to exactly the drop the score took.
function scored(partial: Partial<Scoring>): Scoring {
  return {
    confidence_band: 'medium',
    confidence_base: 0.8,
    confidence_score: 0.65,
    confidence_reason: 'Warehouse rows are consistent with the claims that were checked.',
    confidence_docks: [
      { code: 'refuted_arithmetic', label: "A number's own arithmetic does not reproduce it", amount: 0.1 },
      { code: 'unresolved_figure', label: 'The text still carries a reference to a number that was never supplied', amount: 0.05 },
    ],
    engine_version: 1,
    ...partial,
  };
}

beforeEach(() => {
  jest.clearAllMocks();
  resetScoringEnginesCache();
  (api.getScoringEngines as jest.Mock).mockResolvedValue(engines);
});

// The calibration arrives asynchronously and only the cutoff line needs it, so
// a breakdown rendered directly asserts on the document's own numbers before
// the fetch lands. Flushing at the end of such a test keeps that state update
// inside the test rather than after it.
async function flushCalibration() {
  await waitFor(() => {
    expect(api.getScoringEngines).toHaveBeenCalled();
  });
}

describe('the confidence band', () => {
  it.each([
    ['high', 'High confidence'],
    ['medium', 'Medium confidence'],
    ['low', 'Low confidence'],
    ['not_confirmed', 'Not confirmed'],
    ['unverifiable', 'Unverifiable'],
  ])('renders %s as %s with its reason', (band, label) => {
    wrap(
      <ConfidenceCell
        scoring={scored({ confidence_band: band as Scoring['confidence_band'], confidence_reason: 'REASON-FROM-THE-ENGINE' })}
        confidence={0.65}
        showReason
      />,
    );
    expect(screen.getByTestId('confidence-band')).toHaveTextContent(label);
    // The reason is rendered verbatim off the document — the dashboard holds
    // no second wording for it.
    expect(screen.getByTestId('confidence-reason')).toHaveTextContent('REASON-FROM-THE-ENGINE');
  });

  it('renders unverifiable as its own state rather than as low confidence', () => {
    wrap(<ConfidenceCell scoring={scored({ confidence_band: 'unverifiable', confidence_score: 0.2 })} confidence={0.2} />);
    const badge = screen.getByTestId('confidence-band');
    expect(badge).toHaveTextContent('Unverifiable');
    expect(badge).not.toHaveTextContent(/Low/);
    expect(badge).toHaveAttribute('data-band', 'unverifiable');
  });

  it('renders a band this build does not know without inventing a label for it', () => {
    wrap(<ConfidenceCell scoring={scored({ confidence_band: 'calibrated_by_vibes' as Scoring['confidence_band'] })} confidence={0.5} />);
    expect(screen.getByTestId('confidence-band')).toHaveTextContent('Unknown');
  });
});

describe('a legacy document', () => {
  it('shows its stored number marked legacy, not a band', () => {
    wrap(<ConfidenceCell scoring={undefined} confidence={0.93} />);
    expect(screen.getByTestId('confidence-legacy')).toHaveTextContent('legacy');
    expect(screen.queryByTestId('confidence-band')).not.toBeInTheDocument();
    // And the stored number itself, unchanged — no document is rewritten.
    expect(screen.getByText('93%')).toBeInTheDocument();
  });
});

describe('the "How this was scored" breakdown', () => {
  it('shows base, every dock with its amount, the final, the cutoff crossed and the engine version', async () => {
    wrap(<ScoringBreakdown scoring={scored({})} />);

    expect(screen.getByTestId('scoring-base')).toHaveTextContent('80%');
    expect(screen.getByTestId('scoring-dock-refuted_arithmetic')).toHaveTextContent('−10');
    expect(screen.getByTestId('scoring-dock-unresolved_figure')).toHaveTextContent('−5');
    expect(screen.getByTestId('scoring-final')).toHaveTextContent('65%');
    expect(screen.getByTestId('scoring-engine')).toHaveTextContent('v1');

    // Each dock's own label, rendered verbatim rather than re-worded here.
    expect(screen.getByText("A number's own arithmetic does not reproduce it")).toBeInTheDocument();

    // The cutoff line needs the engine, so it arrives after the fetch.
    await waitFor(() => {
      expect(screen.getByTestId('scoring-cutoff')).toHaveTextContent('40%');
    });
    expect(screen.getByTestId('scoring-cutoff')).toHaveTextContent(/Medium/);
  });

  it('adds up: base minus every displayed dock equals the displayed final', async () => {
    const scoring = scored({});
    wrap(<ScoringBreakdown scoring={scoring} />);

    const pct = (id: string) => Number(screen.getByTestId(id).textContent!.replace('%', ''));
    const dockPoints = (scoring.confidence_docks ?? []).reduce((sum, d) => {
      const shown = screen.getByTestId(`scoring-dock-${d.code}`).textContent!;
      return sum + Number(shown.replace('−', ''));
    }, 0);

    expect(pct('scoring-base') - dockPoints).toBe(pct('scoring-final'));
    await flushCalibration();
  });

  it.each(['not_confirmed', 'unverifiable'])(
    'says a %s band came from the verdict rather than leaving the cutoff line blank',
    async (band) => {
      wrap(<ScoringBreakdown scoring={scored({
        confidence_band: band as Scoring['confidence_band'],
        confidence_base: 0.25, confidence_score: 0.25, confidence_docks: undefined,
      })} />);
      // Present without waiting for the calibration: a terminal band's line
      // needs no cutoff from the API.
      expect(screen.getByTestId('scoring-cutoff')).toHaveTextContent(/validation verdict/);
      expect(screen.getByTestId('scoring-cutoff')).not.toHaveTextContent('%');
      await flushCalibration();
    },
  );

  it('omits only the cutoff line when the engine that scored the document is unknown to this API', async () => {
    // A document scored by a newer agent than the API serving the page.
    wrap(<ScoringBreakdown scoring={scored({ engine_version: 7 })} />);

    await waitFor(() => {
      expect(api.getScoringEngines).toHaveBeenCalled();
    });
    expect(screen.getByTestId('scoring-base')).toHaveTextContent('80%');
    expect(screen.getByTestId('scoring-final')).toHaveTextContent('65%');
    expect(screen.getByTestId('scoring-dock-refuted_arithmetic')).toBeInTheDocument();
    expect(screen.getByTestId('scoring-engine')).toHaveTextContent('v7');
    expect(screen.queryByTestId('scoring-cutoff')).not.toBeInTheDocument();
  });

  it('omits only the cutoff line when the calibration cannot be fetched at all', async () => {
    (api.getScoringEngines as jest.Mock).mockRejectedValue(new Error('older API'));
    wrap(<ScoringBreakdown scoring={scored({})} />);

    await waitFor(() => {
      expect(api.getScoringEngines).toHaveBeenCalled();
    });
    expect(screen.getByTestId('scoring-final')).toHaveTextContent('65%');
    expect(screen.queryByTestId('scoring-cutoff')).not.toBeInTheDocument();
  });

  it('shows no deductions for a document that took none', async () => {
    wrap(<ScoringBreakdown scoring={scored({
      confidence_band: 'high', confidence_base: 0.95, confidence_score: 0.95, confidence_docks: undefined,
    })} />);
    expect(screen.getByTestId('scoring-base')).toHaveTextContent('95%');
    expect(screen.getByTestId('scoring-final')).toHaveTextContent('95%');
    expect(screen.queryByTestId(/^scoring-dock-/)).not.toBeInTheDocument();
    await flushCalibration();
  });

  it('fetches the calibration once however many badges are on the page', async () => {
    wrap(
      <>
        <ScoringBreakdown scoring={scored({})} />
        <ScoringBreakdown scoring={scored({})} />
        <ScoringBreakdown scoring={scored({})} />
      </>,
    );
    await waitFor(() => {
      expect(screen.getAllByTestId('scoring-cutoff').length).toBe(3);
    });
    expect(api.getScoringEngines).toHaveBeenCalledTimes(1);
  });

  it('opens from the band itself', async () => {
    wrap(<ConfidenceCell scoring={scored({})} confidence={0.65} />);
    await userEvent.hover(screen.getByTestId('confidence-band'));
    await waitFor(() => {
      expect(screen.getByTestId('scoring-final')).toHaveTextContent('65%');
    });
  });
});
