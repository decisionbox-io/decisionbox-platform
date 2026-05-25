/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ValidationRouter } from '@/components/validation/ValidationRouter';
import type { InsightValidation, StructuredVerdict, ValidationJob } from '@/lib/api';

// Mock the api module so the router's initial probe + polling don't
// hit the network. The default is "no jobs"; specific tests override
// listValidationJobs before mount.
jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: {
      listValidationJobs: jest.fn().mockResolvedValue([]),
      enqueueValidateInsight: jest.fn(),
      enqueueValidateRecommendation: jest.fn(),
      cancelValidationJob: jest.fn(),
    },
  };
});

// Pull the mock back out for per-test overrides.
// eslint-disable-next-line @typescript-eslint/no-require-imports
const { api } = require('@/lib/api') as { api: { listValidationJobs: jest.Mock } };

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

const baseProps = {
  discoveryId: 'd1',
  docKind: 'insight' as const,
  docId: 'i1',
  validationEnabled: true,
};

function makeVerdict(overrides: Partial<StructuredVerdict> = {}): StructuredVerdict {
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

describe('ValidationRouter — decision tree', () => {
  beforeEach(() => {
    api.listValidationJobs.mockReset();
    api.listValidationJobs.mockResolvedValue([]);
  });

  it('renders LegacyValidationCard when the payload is legacy-shaped', async () => {
    const validation: InsightValidation = { status: 'confirmed', reasoning: 'matched' };
    wrap(<ValidationRouter {...baseProps} validation={validation} />);
    // Legacy card label is the literal "Validation" eyebrow.
    expect(await screen.findByText('Validation')).toBeInTheDocument();
    expect(screen.getByText(/matched/)).toBeInTheDocument();
  });

  it('renders NewValidationPanel when verifier detail is present', async () => {
    const validation: InsightValidation = {
      combined: 'supported',
      verifier: makeVerdict({ overall: 'supported' }),
    };
    wrap(<ValidationRouter {...baseProps} validation={validation} />);
    expect(await screen.findByText('Supported')).toBeInTheDocument();
  });

  it('routes to NoValidationCard when combined=validation_disabled but verifier/refuter are absent', async () => {
    const validation: InsightValidation = { combined: 'validation_disabled' };
    wrap(<ValidationRouter {...baseProps} validation={validation} />);
    expect(await screen.findByRole('button', { name: /Run validation/ })).toBeInTheDocument();
  });

  it('routes to NoValidationCard when combined=skipped_budget_cap but verifier/refuter are absent', async () => {
    const validation: InsightValidation = { combined: 'skipped_budget_cap' };
    wrap(<ValidationRouter {...baseProps} validation={validation} />);
    expect(await screen.findByRole('button', { name: /Run validation/ })).toBeInTheDocument();
  });

  it('renders the disabled empty state when validationEnabled=false and no validation present', async () => {
    wrap(<ValidationRouter {...baseProps} validationEnabled={false} validation={undefined} projectSettingsHref="/p/a/settings#advanced" />);
    expect(await screen.findByText(/Validation is disabled/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Settings/ })).toHaveAttribute('href', '/p/a/settings#advanced');
    expect(screen.queryByRole('button', { name: /Run validation/ })).not.toBeInTheDocument();
  });

  it('renders the progress card when an in-flight job is found', async () => {
    const running: ValidationJob = {
      id: 'job-1',
      project_id: 'p',
      discovery_id: 'd1',
      doc_kind: 'insight',
      doc_id: 'i1',
      status: 'running',
      step: 'verifier',
      attempt: 1,
      enqueued_at: new Date().toISOString(),
      started_at: new Date().toISOString(),
    };
    api.listValidationJobs.mockResolvedValueOnce([running]);
    wrap(<ValidationRouter {...baseProps} validation={undefined} />);
    await waitFor(() => expect(screen.getByText(/Verifier running/)).toBeInTheDocument());
  });
});
