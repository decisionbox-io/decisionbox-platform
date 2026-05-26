/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { AgentRunStats } from '@/components/validation/AgentRunStats';
import type { StructuredVerdict } from '@/lib/api';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

function makeVerdict(overrides: Partial<StructuredVerdict>): StructuredVerdict {
  return {
    doc_id: 'i1',
    doc_kind: 'insight',
    mode: 'verifier',
    claims_considered: [],
    claim_verdicts: [],
    overall: 'supported',
    overall_reason: '',
    lookups_used: 0,
    queries_issued: 0,
    step_reads_used: 0,
    llm_tokens_in: 0,
    llm_tokens_out: 0,
    duration_millis: 0,
    ...overrides,
  };
}

describe('AgentRunStats', () => {
  it('formats sub-second durations in ms', () => {
    wrap(<AgentRunStats verdict={makeVerdict({ duration_millis: 250 })} />);
    expect(screen.getByText('250ms')).toBeInTheDocument();
  });

  it('formats sub-minute durations in seconds', () => {
    wrap(<AgentRunStats verdict={makeVerdict({ duration_millis: 4500 })} />);
    expect(screen.getByText('4.5s')).toBeInTheDocument();
  });

  it('formats multi-minute durations as m s', () => {
    wrap(<AgentRunStats verdict={makeVerdict({ duration_millis: 125_000 })} />);
    expect(screen.getByText('2m 5s')).toBeInTheDocument();
  });

  it('formats token counts in k', () => {
    wrap(<AgentRunStats verdict={makeVerdict({ llm_tokens_in: 13_500, llm_tokens_out: 800 })} />);
    expect(screen.getByText('13.5k')).toBeInTheDocument();
    expect(screen.getByText('800')).toBeInTheDocument();
  });

  it('formats million-token counts compactly', () => {
    wrap(<AgentRunStats verdict={makeVerdict({ llm_tokens_in: 2_300_000 })} />);
    expect(screen.getByText('2.3M')).toBeInTheDocument();
  });

  it('renders zero for missing counters', () => {
    wrap(<AgentRunStats verdict={makeVerdict({})} />);
    // "SQL queries" + "Step reads" + tokens all 0.
    expect(screen.getAllByText('0').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('0ms')).toBeInTheDocument();
  });
});
