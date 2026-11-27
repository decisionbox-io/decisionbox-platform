/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import InsightsPage from '@/app/projects/[id]/insights/page';
import RunPage from '@/app/projects/[id]/discoveries/[runId]/page';

// The same findings are listed in two places: the project's insights page and
// the page for the run that produced them. A reader moving between them is
// looking at one set of documents, so a column that exists on one and not the
// other is not a difference in emphasis — it is one of the two lists quietly
// withholding something.
//
// The defect this guards was not a component behaving wrongly but a component
// never being CALLED: the business-risk chips were added to the project list
// when the weighting work landed and not to the run page, so for one release
// the kind of risk a finding carried was visible on one screen and absent on
// the other — 20 chips against 0, on the same twenty findings. Nothing failed,
// because the chips' own tests were about the chips.
//
// So both pages are rendered here rather than scanned as source: a source
// check still passes for a call behind an always-false condition, or one
// handed no scoring, both of which render nothing.

jest.mock('next/navigation', () => ({
  useParams: () => ({ id: 'p1', runId: 'r1' }),
  useRouter: () => ({ push: jest.fn(), back: jest.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true, default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

function scoredInsight() {
  return {
  id: 'i1',
  name: 'Swiftpay failures rose 7.1x',
  description: 'd',
  analysis_area: 'payment-failure-analysis',
  severity: 'high',
  confidence: 0.95,
  affected_count: 2803,
  discovered_at: '2026-11-26T00:00:00Z',
  validation: { combined: 'confirmed' },
  evidence_scoring: {
    confidence_band: 'high', confidence_score: 0.95,
    severity_band: 'high', severity_score: 1, magnitude_score: 1,
    priority_tier: 'critical', priority_score: 3.2,
    risk_flags: ['payment-risk', 'revenue-leak'],
    engine_version: 1,
    },
  };
}

function discoveryRef() {
  return {
    id: 'd1', run_id: 'r1', project_id: 'p1', status: 'completed',
    created_at: '2026-11-26T00:00:00Z',
    insights: [scoredInsight()], recommendations: [],
  };
}

jest.mock('@/lib/api', () => ({
  ApiError: class extends Error { status = 0; },
  api: {
    getProject: jest.fn().mockResolvedValue({ id: 'p1', name: 'Acme', domain: 'd', category: 'c' }),
    listDiscoveries: jest.fn().mockResolvedValue([discoveryRef()]),
    getDiscoveryById: jest.fn().mockResolvedValue(discoveryRef()),
    listFeedback: jest.fn().mockResolvedValue([]),
    listReadIDs: jest.fn().mockResolvedValue([]),
    listExplorationSteps: jest.fn().mockResolvedValue([]),
    listAnalysisSteps: jest.fn().mockResolvedValue([]),
    listValidationResults: jest.fn().mockResolvedValue([]),
    getRecommendationLog: jest.fn().mockResolvedValue(null),
    listProjectQuestions: jest.fn().mockResolvedValue([]),
    searchInsights: jest.fn().mockResolvedValue({ results: [] }),
    markRead: jest.fn().mockResolvedValue({}),
  },
}));
const wrap = (ui: React.ReactElement) => render(<MantineProvider>{ui}</MantineProvider>);

describe('both insight lists show the row its own business-risk flags', () => {
  it.each([
    ['the project insights list', () => <InsightsPage />],
    ['the discovery run page', () => <RunPage />],
  ])('%s', async (_name, Page) => {
    wrap(<Page />);
    await waitFor(() => expect(screen.getByText(/Swiftpay failures rose/)).toBeInTheDocument());

    // The category as authored, not a humanised form: in a list the chip sits
    // beside the analysis-area id and two spellings read as two kinds of thing.
    expect(screen.getByTestId('risk-flag-payment-risk')).toHaveTextContent('payment-risk');
    expect(screen.getByTestId('risk-flag-revenue-leak')).toHaveTextContent('revenue-leak');
  });
});
