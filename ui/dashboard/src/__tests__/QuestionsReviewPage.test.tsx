/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import QuestionsReviewPage from '@/app/projects/[id]/questions/page';
import { DiscoveryQuestion } from '@/lib/api';

jest.mock('next/navigation', () => ({ useParams: () => ({ id: 'p1' }) }));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
// Shell pulls in the whole app chrome — stub it to a passthrough so the test
// exercises the page body, not the nav.
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true, default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

const listProjectQuestions = jest.fn();
jest.mock('@/lib/api', () => ({
  ApiError: class extends Error { status = 0; },
  api: {
    getProject: jest.fn().mockResolvedValue({ id: 'p1', name: 'Acme' }),
    listProjectQuestions: (...a: unknown[]) => listProjectQuestions(...a),
    answerQuestion: jest.fn().mockResolvedValue({}),
    dismissQuestion: jest.fn().mockResolvedValue({}),
  },
}));

function q(partial: Partial<DiscoveryQuestion>): DiscoveryQuestion {
  return {
    id: 'x', project_id: 'p1', run_id: 'r1', discovery_id: 'd1',
    question: 'Q', rationale: '', linked_target: { type: 'insight', id: 'i1' },
    answer_type: 'free_text', status: 'pending', created_at: '2026-01-01T00:00:00Z',
    ...partial,
  };
}

function wrap() {
  return render(<MantineProvider><QuestionsReviewPage /></MantineProvider>);
}

beforeEach(() => { jest.clearAllMocks(); });

describe('QuestionsReviewPage', () => {
  it('lists all statuses with per-tab counts and filters', async () => {
    listProjectQuestions.mockResolvedValue([
      q({ id: 'p', status: 'pending', question: 'Pending one' }),
      q({ id: 'a', status: 'answered', question: 'Answered one', answer: 'Because' }),
      q({ id: 'd', status: 'dismissed', question: 'Dismissed one' }),
    ]);
    wrap();
    // All three visible under the default "All" filter.
    await waitFor(() => expect(screen.getByText('Pending one')).toBeInTheDocument());
    expect(screen.getByText('Answered one')).toBeInTheDocument();
    expect(screen.getByText('Dismissed one')).toBeInTheDocument();

    // Filter to Answered → only the answered card, with its recorded answer.
    fireEvent.click(screen.getByRole('button', { name: /Answered/ }));
    expect(screen.getByText('Answered one')).toBeInTheDocument();
    expect(screen.getByText(/Because/)).toBeInTheDocument();
    expect(screen.queryByText('Pending one')).not.toBeInTheDocument();
    expect(screen.queryByText('Dismissed one')).not.toBeInTheDocument();
  });

  it('shows an empty state when there are no questions', async () => {
    listProjectQuestions.mockResolvedValue([]);
    wrap();
    await waitFor(() => expect(screen.getByText('No questions yet')).toBeInTheDocument());
  });
});
