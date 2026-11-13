/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { RiskFlagChips } from '@/components/scoring/RiskFlagChips';
import { AreaRiskConfig } from '@/components/scoring/AreaRiskConfig';
import type { AnalysisAreaConfig, RiskCategory, Scoring } from '@/lib/api';

// The two surfaces the business-risk configuration is read and written
// through: the flags a reader sees on a finding, and the per-area editor on
// the playbook page.

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

const scored = (flags?: string[]): Scoring => ({
  confidence_band: 'high',
  severity_band: 'high',
  risk_flags: flags,
  engine_version: 1,
});

// --- The flags -------------------------------------------------------------

describe('RiskFlagChips', () => {
  // An unconfigured deployment has no taxonomy, so every document carries no
  // flags. An empty chip row under a heading would read as "no business
  // risk", which is a claim; rendering nothing is the absence of one.
  it.each([
    ['no flags field', scored()],
    ['no scoring block at all', undefined],
    ['an empty list', scored([])],
  ])('renders nothing for %s', (_name, scoring) => {
    wrap(<RiskFlagChips scoring={scoring} />);
    // The group itself, not the container: MantineProvider puts its style
    // tags in there, so an emptiness assertion on the container would be
    // about Mantine rather than about this component.
    expect(screen.queryByTestId('risk-flags')).not.toBeInTheDocument();
  });

  // Multi-tag, in the order the document carries: the order is the area's own
  // declaration order, which the engine keeps stable across re-scores.
  it('renders one chip per flag, in document order', () => {
    wrap(<RiskFlagChips scoring={scored(['revenue_leak', 'compliance'])} />);
    const chips = screen.getByTestId('risk-flags');
    expect(chips.textContent).toBe('Revenue leakCompliance');
  });

  it('humanises an id without inventing words', () => {
    wrap(<RiskFlagChips scoring={scored(['data_quality', 'player-churn', 'compliance'])} />);
    expect(screen.getByTestId('risk-flag-data_quality')).toHaveTextContent('Data quality');
    expect(screen.getByTestId('risk-flag-player-churn')).toHaveTextContent('Player churn');
    expect(screen.getByTestId('risk-flag-compliance')).toHaveTextContent('Compliance');
  });
});

// --- The editor ------------------------------------------------------------

const taxonomy: RiskCategory[] = [
  { id: 'revenue_leak', label: 'Revenue leakage', weight: 2.5 },
  { id: 'customer_churn', label: 'Customer churn', weight: 2 },
];

const area = (over: Partial<AnalysisAreaConfig> = {}): AnalysisAreaConfig => ({
  name: 'Conversion',
  description: 'The funnel',
  keywords: ['cart'],
  prompt: 'Analyse {{QUERY_RESULTS}}',
  is_base: true,
  is_custom: false,
  priority: 1,
  enabled: true,
  ...over,
});

describe('AreaRiskConfig', () => {
  const globals = ['compliance', 'security', 'data_quality'];

  it('offers the pack’s categories and the engine’s cross-cutting ids', async () => {
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={taxonomy}
        globalCategories={globals}
        maxWeight={3}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByTestId('area-risk-categories'));

    // The pack's own, with the weight each contributes, so a reader choosing
    // one can see what it is worth.
    expect(screen.getByText('Revenue leakage (×2.5)')).toBeInTheDocument();
    expect(screen.getByText('Customer churn (×2)')).toBeInTheDocument();
    // And the global set, which is available to every pack.
    for (const id of globals) {
      expect(screen.getByText(id.replace(/_/g, ' '))).toBeInTheDocument();
    }
  });

  // A pack that declares no taxonomy is the backward-compatible case, and the
  // global ids are what make the feature reachable for it at all.
  it('still offers the global ids for a pack with no taxonomy', async () => {
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={undefined}
        globalCategories={globals}
        maxWeight={3}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByTestId('area-risk-categories'));
    expect(screen.getByText('compliance')).toBeInTheDocument();
  });

  // A pack that declares `compliance` itself must not be offered the id twice
  // — once with its own weight and once as the bare global.
  it('does not offer a global id the pack declares itself', async () => {
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={[{ id: 'compliance', label: 'Regulatory compliance', weight: 3 }]}
        globalCategories={globals}
        maxWeight={3}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByTestId('area-risk-categories'));
    expect(screen.getByText('Regulatory compliance (×3)')).toBeInTheDocument();
    expect(screen.queryByText('compliance')).not.toBeInTheDocument();
  });

  it('raises a selected category to the parent', async () => {
    const onChange = jest.fn();
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={taxonomy}
        globalCategories={globals}
        maxWeight={3}
        onChange={onChange}
      />,
    );
    await userEvent.click(screen.getByTestId('area-risk-categories'));
    await userEvent.click(screen.getByText('Revenue leakage (×2.5)'));

    expect(onChange).toHaveBeenCalledWith({ risk_categories: ['revenue_leak'] });
  });

  it('shows the override the area already carries', () => {
    wrap(
      <AreaRiskConfig
        area={area({ severity_weight: 1.5 })}
        taxonomy={taxonomy}
        globalCategories={globals}
        maxWeight={3}
        onChange={() => {}}
      />,
    );
    expect(screen.getByTestId('area-severity-weight')).toHaveValue('1.5');
  });

  it('writes an override and states the range it must fall in', async () => {
    const onChange = jest.fn();
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={taxonomy}
        globalCategories={globals}
        maxWeight={3}
        onChange={onChange}
      />,
    );
    // The ceiling is the engine's, served with the calibration rather than
    // restated in the dashboard — so the field has to show the number it was
    // given.
    expect(screen.getByText(/0\.01–3/)).toBeInTheDocument();

    await userEvent.type(screen.getByTestId('area-severity-weight'), '2');
    expect(onChange).toHaveBeenLastCalledWith({ severity_weight: 2 });
  });

  // Clearing the field must produce "no override", not a weight of zero: zero
  // is not a legal weight, and it is the one value that would remove every
  // finding in the area from the ranking.
  it('clears the override to undefined rather than to zero', async () => {
    const onChange = jest.fn();
    wrap(
      <AreaRiskConfig
        area={area({ severity_weight: 1.5 })}
        taxonomy={taxonomy}
        globalCategories={globals}
        maxWeight={3}
        onChange={onChange}
      />,
    );
    await userEvent.clear(screen.getByTestId('area-severity-weight'));
    expect(onChange).toHaveBeenLastCalledWith({ severity_weight: undefined });
  });

  // An API older than this build serves a severity calibration without the
  // global ids or the ceiling. That costs those two things and leaves the rest
  // of the editor working, which is the same contract the breakdown's cutoff
  // line already honours.
  it('degrades without the served contract instead of breaking', async () => {
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={taxonomy}
        globalCategories={undefined}
        maxWeight={undefined}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getByTestId('area-risk-categories'));
    expect(screen.getByText('Revenue leakage (×2.5)')).toBeInTheDocument();
    expect(screen.queryByText('compliance')).not.toBeInTheDocument();
    expect(screen.queryByText(/0\.01–/)).not.toBeInTheDocument();
  });

  // A project whose pack declares nothing AND an API that serves no global
  // ids leaves nothing to select. Disabled with a reason beats an empty
  // dropdown that looks broken.
  it('disables the selector when there is nothing to select', () => {
    wrap(
      <AreaRiskConfig
        area={area()}
        taxonomy={[]}
        globalCategories={[]}
        maxWeight={3}
        onChange={() => {}}
      />,
    );
    const input = screen.getByTestId('area-risk-categories');
    expect(input).toBeDisabled();
    expect(input).toHaveAttribute('placeholder', 'No risk categories available');
  });
});
