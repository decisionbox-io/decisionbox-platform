/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, act } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import {
  ProjectRunStatus,
  formatElapsed,
  formatRunTimestamp,
} from '@/components/projects/ProjectRunStatus';

function mount(props: React.ComponentProps<typeof ProjectRunStatus>) {
  return render(
    <MantineProvider>
      <ProjectRunStatus {...props} />
    </MantineProvider>
  );
}

describe('formatElapsed', () => {
  it('formats seconds under a minute', () => {
    expect(formatElapsed(0)).toBe('0s');
    expect(formatElapsed(45)).toBe('45s');
  });

  it('formats minutes and seconds with zero-padded seconds', () => {
    expect(formatElapsed(252)).toBe('4m 12s'); // matches the issue example
    expect(formatElapsed(65)).toBe('1m 05s');
  });

  it('formats hours with zero-padded minutes and seconds', () => {
    expect(formatElapsed(3852)).toBe('1h 04m 12s');
  });

  it('clamps negative input (clock skew) to 0s', () => {
    expect(formatElapsed(-10)).toBe('0s');
  });
});

describe('formatRunTimestamp', () => {
  it('renders local YYYY-MM-DD HH:MM', () => {
    const iso = '2026-05-29T14:30:00';
    const d = new Date(iso);
    const expected =
      `${d.getFullYear()}-` +
      `${String(d.getMonth() + 1).padStart(2, '0')}-` +
      `${String(d.getDate()).padStart(2, '0')} ` +
      `${String(d.getHours()).padStart(2, '0')}:` +
      `${String(d.getMinutes()).padStart(2, '0')}`;
    expect(formatRunTimestamp(iso)).toBe(expected);
    expect(formatRunTimestamp(iso)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  });

  it('returns "" for an unparseable timestamp', () => {
    expect(formatRunTimestamp('not-a-date')).toBe('');
  });
});

describe('ProjectRunStatus', () => {
  afterEach(() => jest.useRealTimers());

  it('renders nothing when the project has never run', () => {
    // Rendered bare (no MantineProvider) since the empty-status path
    // returns null without using any Mantine component — a provider
    // would inject its own <style> tags and defeat the empty check.
    const { container } = render(<ProjectRunStatus status="" startedAt={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows a Running badge with live elapsed time that ticks up', () => {
    jest.useFakeTimers();
    // Pin "now" to 4m 12s after the run start.
    const start = new Date('2026-05-29T14:00:00Z');
    const now = new Date(start.getTime() + 252_000);
    jest.setSystemTime(now);

    mount({ status: 'running', startedAt: start.toISOString() });

    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getByText('4m 12s')).toBeInTheDocument();

    // Advance the clock by 1s — the interval fires and the elapsed
    // label must keep counting up. (advanceTimersByTime also advances
    // the mocked Date, so no separate setSystemTime is needed.)
    act(() => {
      jest.advanceTimersByTime(1000);
    });
    expect(screen.getByText('4m 13s')).toBeInTheDocument();
  });

  it('treats pending the same as running', () => {
    jest.useFakeTimers();
    const start = new Date('2026-05-29T14:00:00Z');
    jest.setSystemTime(new Date(start.getTime() + 5_000));
    mount({ status: 'pending', startedAt: start.toISOString() });
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getByText('5s')).toBeInTheDocument();
  });

  it('shows a Completed badge with the completion timestamp', () => {
    mount({
      status: 'completed',
      startedAt: '2026-05-29T14:00:00',
      completedAt: '2026-05-29T14:30:00',
    });
    expect(screen.getByText('Completed')).toBeInTheDocument();
    expect(screen.getByText(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/)).toBeInTheDocument();
  });

  it('shows a Failed badge', () => {
    mount({ status: 'failed', startedAt: '2026-05-29T14:00:00' });
    expect(screen.getByText('Failed')).toBeInTheDocument();
  });

  it('shows a Cancelled badge', () => {
    mount({ status: 'cancelled', startedAt: '2026-05-29T14:00:00' });
    expect(screen.getByText('Cancelled')).toBeInTheDocument();
  });

  it('renders an unknown status verbatim', () => {
    mount({ status: 'archiving', startedAt: null });
    expect(screen.getByText('archiving')).toBeInTheDocument();
  });
});
