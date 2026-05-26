/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ClaimList } from '@/components/validation/ClaimList';
import type { ClaimVerdict } from '@/lib/api';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

const claims: ClaimVerdict[] = [
  {
    claim_text: 'Sub-claim A',
    claim_kind: '',
    is_headline: false,
    status: 'supported',
    reasoning: '',
    evidence: { kind: 'step_row', step_id: 7, row: { x: 1 } },
  },
  {
    claim_text: 'Headline claim',
    claim_kind: '',
    is_headline: true,
    status: 'confirmed',
    reasoning: '',
    evidence: { kind: 'none' },
  },
  {
    claim_text: 'Sub-claim B',
    claim_kind: '',
    is_headline: false,
    status: 'rejected',
    reasoning: 'mismatch',
    evidence: {
      kind: 'warehouse_query',
      query_sql: 'SELECT COUNT(*) FROM orders WHERE x = 1',
      row: { c: 0 },
    },
  },
];

describe('ClaimList', () => {
  it('pins the headline claim first regardless of input order', () => {
    wrap(<ClaimList claims={claims} />);
    const rendered = screen.getAllByText(/claim/i).map((el) => el.textContent || '');
    // The first matching "claim" text should be the headline's claim_text.
    expect(rendered[0]).toContain('Headline claim');
  });

  it('renders step-row evidence with the step number', () => {
    wrap(<ClaimList claims={claims} />);
    expect(screen.getByText(/Row from step 7/)).toBeInTheDocument();
  });

  it('renders warehouse-query evidence with the SQL block', () => {
    wrap(<ClaimList claims={claims} />);
    expect(screen.getByText(/Counter-query run by refuter/)).toBeInTheDocument();
    expect(screen.getByText(/SELECT COUNT\(\*\)/)).toBeInTheDocument();
  });

  it('renders "no evidence" for evidence.kind === "none"', () => {
    wrap(<ClaimList claims={claims} />);
    expect(screen.getByText(/No evidence attached/)).toBeInTheDocument();
  });

  it('renders a fallback when the claims array is empty', () => {
    wrap(<ClaimList claims={[]} />);
    expect(screen.getByText(/No per-claim verdicts recorded/)).toBeInTheDocument();
  });
});
