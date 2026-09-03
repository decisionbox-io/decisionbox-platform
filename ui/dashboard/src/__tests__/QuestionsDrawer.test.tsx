/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import QuestionsDrawer from '@/components/common/QuestionsDrawer';
import { DiscoveryQuestion } from '@/lib/api';

jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/lib/api', () => ({
  api: { answerQuestion: jest.fn().mockResolvedValue({}), dismissQuestion: jest.fn().mockResolvedValue({}) },
}));

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

function q(id: string): DiscoveryQuestion {
  return {
    id, project_id: 'p1', run_id: 'r1', discovery_id: 'd1',
    question: `Question ${id}`, rationale: 'why',
    linked_target: { type: 'insight', id: 'i1' },
    answer_type: 'free_text', status: 'pending', created_at: '',
  };
}

beforeEach(() => { jest.clearAllMocks(); localStorage.clear(); });

describe('QuestionsDrawer', () => {
  it('renders nothing when there are no questions', () => {
    wrap(<QuestionsDrawer projectId="p1" questions={[]} onResolved={jest.fn()} />);
    // The drawer itself emits no markup (MantineProvider still injects <style>).
    expect(screen.queryByLabelText('Clarifying questions')).not.toBeInTheDocument();
    expect(screen.queryByText('Questions to answer')).not.toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('is open by default with a title, count, and one card per question', () => {
    wrap(<QuestionsDrawer projectId="p1" questions={[q('q1'), q('q2')]} onResolved={jest.fn()} />);
    expect(screen.getByText('Questions to answer')).toBeInTheDocument();
    expect(screen.getByText('Question q1')).toBeInTheDocument();
    expect(screen.getByText('Question q2')).toBeInTheDocument();
  });

  it('collapses to a reopen tab and expands again, persisting the choice', () => {
    wrap(<QuestionsDrawer projectId="p1" questions={[q('q1')]} onResolved={jest.fn()} storageKey="k1" />);
    // Collapse
    fireEvent.click(screen.getByLabelText('Collapse questions'));
    expect(screen.queryByText('Questions to answer')).not.toBeInTheDocument();
    expect(localStorage.getItem('k1')).toBe('1');
    // A slim tab remains — clicking it reopens the panel.
    fireEvent.click(screen.getByTitle('1 question to answer'));
    expect(screen.getByText('Questions to answer')).toBeInTheDocument();
    expect(localStorage.getItem('k1')).toBe('0');
  });

  it('starts collapsed when the stored preference says so', () => {
    localStorage.setItem('k2', '1');
    wrap(<QuestionsDrawer projectId="p1" questions={[q('q1')]} onResolved={jest.fn()} storageKey="k2" />);
    // The saved preference is applied after mount → panel is collapsed.
    expect(screen.queryByText('Questions to answer')).not.toBeInTheDocument();
    expect(screen.getByTitle('1 question to answer')).toBeInTheDocument();
  });
});
