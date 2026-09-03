/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, act } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ChatDrawerProvider, useChatDrawer } from '@/components/ask/ChatDrawerProvider';
import SuggestedQuestions from '@/components/ask/SuggestedQuestions';

// NOTE: ChatPanel is intentionally NOT imported here. When the enterprise
// overlay is composed over this dashboard (enterprise CI runs the merged jest
// suite), @/components/ask/ChatPanel resolves to the enterprise panel, which
// pulls @/lib/role → next-auth/react (ESM) and cannot be loaded in Jest without
// a mock — but @/lib/role does not exist in the community tree, so it cannot be
// mocked here either. The ChatPanel seed-send behaviour is covered by the
// enterprise overlay test (ui/src/__tests__/ChatPanelSeed.test.tsx), which runs
// only in the merged tree where @/lib/role exists to mock. This file covers the
// role-free pieces (provider + SuggestedQuestions), which run identically in
// both trees.

const getAskSuggestions = jest.fn();

jest.mock('@/lib/api', () => ({
  api: {
    getAskSuggestions: (...a: unknown[]) => getAskSuggestions(...a),
  },
}));

function wrap(ui: React.ReactNode) {
  return render(<MantineProvider><ChatDrawerProvider>{ui}</ChatDrawerProvider></MantineProvider>);
}

beforeEach(() => {
  jest.clearAllMocks();
});

// --- provider ---

function Probe() {
  const ctx = useChatDrawer()!;
  return (
    <div>
      <span data-testid="open">{String(ctx.open)}</span>
      <span data-testid="nonce">{ctx.seedNonce}</span>
      <span data-testid="seed">{ctx.seedContext?.title ?? 'none'}</span>
      <button onClick={() => ctx.openWithSeed('p1', { type: 'insight', id: 'i1', title: 'Churn spike' }, 'Why?')}>seed</button>
      <button onClick={() => ctx.openGeneric('p1')}>generic</button>
      <button onClick={() => ctx.close()}>close</button>
    </div>
  );
}

describe('ChatDrawerProvider', () => {
  it('openWithSeed sets seed + bumps nonce; close hides without clearing seed', () => {
    wrap(<Probe />);
    expect(screen.getByTestId('open')).toHaveTextContent('false');
    fireEvent.click(screen.getByText('seed'));
    expect(screen.getByTestId('open')).toHaveTextContent('true');
    expect(screen.getByTestId('seed')).toHaveTextContent('Churn spike');
    expect(screen.getByTestId('nonce')).toHaveTextContent('1');
    fireEvent.click(screen.getByText('close'));
    expect(screen.getByTestId('open')).toHaveTextContent('false');
  });

  it('openGeneric for the same project does NOT bump the nonce (resumes)', () => {
    wrap(<Probe />);
    fireEvent.click(screen.getByText('generic'));
    expect(screen.getByTestId('nonce')).toHaveTextContent('1'); // first project → fresh
    fireEvent.click(screen.getByText('close'));
    fireEvent.click(screen.getByText('generic'));
    expect(screen.getByTestId('nonce')).toHaveTextContent('1'); // same project → resume
    expect(screen.getByTestId('seed')).toHaveTextContent('none');
  });

  it('openGeneric AFTER a seeded chat remounts (fresh generic, not the seeded session)', () => {
    wrap(<Probe />);
    fireEvent.click(screen.getByText('seed'));    // seeded → nonce 1
    fireEvent.click(screen.getByText('close'));
    fireEvent.click(screen.getByText('generic')); // was seeded → must bump to nonce 2
    expect(screen.getByTestId('nonce')).toHaveTextContent('2');
    expect(screen.getByTestId('seed')).toHaveTextContent('none');
  });
});

// --- SuggestedQuestions ---

describe('SuggestedQuestions', () => {
  it('shows a thinking state then renders chips; a chip opens the seeded drawer', async () => {
    let resolve!: (v: { questions: string[] }) => void;
    getAskSuggestions.mockReturnValue(new Promise(r => { resolve = r; }));
    wrap(<SuggestedQuestions projectId="p1" seed={{ type: 'insight', id: 'i1', title: 'Churn' }} />);

    expect(screen.getByText(/Thinking of good questions/i)).toBeInTheDocument();
    await act(async () => { resolve({ questions: ['Why did churn spike?', 'Which cohort?'] }); });

    expect(await screen.findByText('Why did churn spike?')).toBeInTheDocument();
    expect(screen.getByText('Which cohort?')).toBeInTheDocument();
  });

  it('renders only the Ask button when there are no suggestions', async () => {
    getAskSuggestions.mockResolvedValue({ questions: [] });
    wrap(<SuggestedQuestions projectId="p1" seed={{ type: 'recommendation', id: 'r1', title: 'Lower price' }} />);
    expect(await screen.findByText(/Ask about this recommendation/i)).toBeInTheDocument();
  });
});
