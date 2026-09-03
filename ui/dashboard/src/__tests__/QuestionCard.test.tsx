/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import QuestionCard from '@/components/common/QuestionCard';
import QuestionsPanel from '@/components/common/QuestionsPanel';
import { DiscoveryQuestion } from '@/lib/api';

jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/lib/api', () => ({
  api: {
    answerQuestion: jest.fn().mockResolvedValue({}),
    dismissQuestion: jest.fn().mockResolvedValue({}),
    getDiscoveryById: jest.fn().mockResolvedValue({ insights: [], recommendations: [] }),
  },
}));

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { api } = require('@/lib/api');

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

function q(partial: Partial<DiscoveryQuestion>): DiscoveryQuestion {
  return {
    id: 'q1', project_id: 'p1', run_id: 'r1', discovery_id: 'd1',
    question: 'Is code 4 closed?', rationale: 'opaque enum',
    linked_target: { type: 'insight', id: 'i1' },
    answer_type: 'boolean', status: 'pending', created_at: '',
    ...partial,
  };
}

beforeEach(() => jest.clearAllMocks());

describe('QuestionCard', () => {
  it('answers a boolean question in one selection + submit', async () => {
    const onResolved = jest.fn();
    wrap(<QuestionCard projectId="p1" question={q({})} onResolved={onResolved} />);
    fireEvent.click(screen.getByText('Yes'));
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }));
    await waitFor(() => expect(api.answerQuestion).toHaveBeenCalledWith('p1', 'q1', expect.objectContaining({ answer_bool: true })));
    await waitFor(() => expect(onResolved).toHaveBeenCalledWith('q1'));
  });

  it('renders single_choice options including the Other escape and requires a note for Other', async () => {
    const question = q({
      answer_type: 'single_choice',
      options: [{ id: 'a', label: 'Alpha' }, { id: '__other', label: 'Other / add a note' }],
    });
    wrap(<QuestionCard projectId="p1" question={question} onResolved={jest.fn()} />);
    expect(screen.getByLabelText('Alpha')).toBeInTheDocument();
    // Select Other → a textarea appears; submitting without text does not call the API.
    fireEvent.click(screen.getByLabelText('Other / add a note'));
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }));
    await waitFor(() => expect(api.answerQuestion).not.toHaveBeenCalled());
    // Provide the note → submits with the option + note.
    fireEvent.change(screen.getByPlaceholderText('Add a note…'), { target: { value: 'Legal-risk score' } });
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }));
    await waitFor(() => expect(api.answerQuestion).toHaveBeenCalledWith('p1', 'q1', expect.objectContaining({
      answer_option_ids: ['__other'], answer_note: 'Legal-risk score',
    })));
  });

  it('dismisses without answering', async () => {
    const onResolved = jest.fn();
    wrap(<QuestionCard projectId="p1" question={q({})} onResolved={onResolved} />);
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => expect(api.dismissQuestion).toHaveBeenCalledWith('p1', 'q1'));
    await waitFor(() => expect(onResolved).toHaveBeenCalledWith('q1'));
    expect(api.answerQuestion).not.toHaveBeenCalled();
  });

  it('renders a free_text answer box', async () => {
    wrap(<QuestionCard projectId="p1" question={q({ answer_type: 'free_text' })} onResolved={jest.fn()} />);
    fireEvent.change(screen.getByPlaceholderText('Your answer…'), { target: { value: 'closed flag' } });
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }));
    await waitFor(() => expect(api.answerQuestion).toHaveBeenCalledWith('p1', 'q1', { answer: 'closed flag' }));
  });

  it('renders answered questions read-only with the recorded answer + a KB link, no controls', () => {
    wrap(<QuestionCard projectId="p1" question={q({
      status: 'answered', answer: 'Yes, code 4 means closed', answered_at: '2026-09-01T00:00:00Z',
    })} onResolved={jest.fn()} />);
    expect(screen.getByText('Answered')).toBeInTheDocument();
    expect(screen.getByText(/Yes, code 4 means closed/)).toBeInTheDocument();
    // No answer controls / buttons on a resolved card.
    expect(screen.queryByRole('button', { name: 'Submit' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Dismiss' })).not.toBeInTheDocument();
    // Points at the knowledge base where the materialized note can be edited.
    expect(screen.getByText('Edit in Knowledge Sources →').closest('a'))
      .toHaveAttribute('href', '/projects/p1/sources');
  });

  it('renders dismissed questions read-only with a muted note', () => {
    wrap(<QuestionCard projectId="p1" question={q({ status: 'dismissed' })} onResolved={jest.fn()} />);
    expect(screen.getByText('Dismissed')).toBeInTheDocument();
    expect(screen.getByText(/not sent to the next run/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Submit' })).not.toBeInTheDocument();
  });
});

describe('QuestionsPanel', () => {
  it('renders nothing when there are no questions', () => {
    wrap(<QuestionsPanel projectId="p1" questions={[]} onResolved={jest.fn()} />);
    expect(screen.queryByText('Questions to answer')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Submit' })).not.toBeInTheDocument();
  });

  it('renders a header + one card per question', () => {
    wrap(<QuestionsPanel projectId="p1" questions={[q({ id: 'q1' }), q({ id: 'q2' })]} onResolved={jest.fn()} />);
    expect(screen.getByText('Questions to answer')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Submit' })).toHaveLength(2);
  });
});
