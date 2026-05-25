/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ValidationPanel } from '@/components/validation/ValidationPanel';
import type { InsightValidation, StructuredVerdict, ClaimVerdict } from '@/lib/api';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

function makeVerdict(overrides: Partial<StructuredVerdict> = {}): StructuredVerdict {
  return {
    doc_id: 'i1',
    doc_kind: 'insight',
    mode: 'verifier',
    claims_considered: ['headline claim'],
    claim_verdicts: [],
    overall: 'supported',
    overall_reason: 'All cited rows match the headline figures.',
    lookups_used: 0,
    queries_issued: 1,
    step_reads_used: 2,
    llm_tokens_in: 1234,
    llm_tokens_out: 567,
    duration_millis: 4500,
    ...overrides,
  };
}

const headlineClaim: ClaimVerdict = {
  claim_text: 'Headline claim',
  claim_kind: '',
  is_headline: true,
  status: 'confirmed',
  reasoning: 'Matches step 1 row exactly.',
  evidence: {
    kind: 'step_row',
    step_id: 1,
    row: { metric: 'late_rate', value: '12.9%' },
  },
};

const subClaim: ClaimVerdict = {
  claim_text: 'Sub claim — 1,234 affected users',
  claim_kind: 'figure',
  is_headline: false,
  status: 'partial',
  reasoning: 'Step 1 reports 1,200, not 1,234.',
  evidence: { kind: 'none' },
};

describe('ValidationPanel — new shape', () => {
  it('renders the combined verdict and tagline', () => {
    const v: InsightValidation = { combined: 'rejected', verifier: makeVerdict({ overall: 'supported' }), refuter: makeVerdict({ mode: 'refuter', overall: 'rejected' }) };
    wrap(<ValidationPanel validation={v} />);
    expect(screen.getByText('Rejected')).toBeInTheDocument();
    expect(screen.getByText(/Evidence disagrees/)).toBeInTheDocument();
  });

  it('renders the refuter-disabled note when set', () => {
    const v: InsightValidation = {
      combined: 'supported',
      verifier: makeVerdict({ overall: 'supported' }),
      refuter_disabled: true,
    };
    wrap(<ValidationPanel validation={v} />);
    expect(screen.getByText(/Refuter was disabled/)).toBeInTheDocument();
  });

  it('hides the breakdown button when no verifier/refuter present', () => {
    const v: InsightValidation = { combined: 'validation_disabled' };
    wrap(<ValidationPanel validation={v} />);
    expect(screen.queryByRole('button', { name: /Show breakdown/ })).not.toBeInTheDocument();
  });

  it('opens the drawer and renders both agent cards on click', async () => {
    const v: InsightValidation = {
      combined: 'partial',
      verifier: makeVerdict({ overall: 'supported', claim_verdicts: [headlineClaim, subClaim] }),
      refuter: makeVerdict({ mode: 'refuter', overall: 'rejected', overall_reason: 'Counter-row in step 4.' }),
    };
    wrap(<ValidationPanel validation={v} />);
    fireEvent.click(screen.getByRole('button', { name: /Show breakdown/ }));
    // Mantine Drawer transitions in via a portal — wait for the body content.
    expect(await screen.findByText('Verifier')).toBeInTheDocument();
    expect(screen.getByText('Refuter')).toBeInTheDocument();
    // Headline pin renders the literal "HEADLINE" tag in upper-case.
    expect(screen.getByText('Headline')).toBeInTheDocument();
    // Sub-claim reasoning shows up.
    expect(screen.getByText(/Step 1 reports 1,200/)).toBeInTheDocument();
  });
});

describe('ValidationPanel — legacy shape', () => {
  it('renders legacy fields when only the status field is set', () => {
    const v: InsightValidation = {
      status: 'confirmed',
      verified_count: 5,
      original_count: 5,
      reasoning: 'Re-ran the SQL — same count.',
    };
    wrap(<ValidationPanel validation={v} />);
    expect(screen.getByText('Validation')).toBeInTheDocument();
    expect(screen.getByText('5 verified')).toBeInTheDocument();
    expect(screen.getByText(/Re-ran the SQL/)).toBeInTheDocument();
    // No breakdown drawer for legacy.
    expect(screen.queryByRole('button', { name: /Show breakdown/ })).not.toBeInTheDocument();
  });

  it('renders legacy status badge using the meta table', () => {
    const v: InsightValidation = { status: 'rejected', reasoning: 'mismatch' };
    wrap(<ValidationPanel validation={v} />);
    expect(screen.getByText('Rejected')).toBeInTheDocument();
  });
});
