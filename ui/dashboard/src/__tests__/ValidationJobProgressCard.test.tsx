/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { ValidationJobProgressCard } from '@/components/validation/ValidationJobProgressCard';
import type { ValidationJob } from '@/lib/api';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

function makeJob(overrides: Partial<ValidationJob> = {}): ValidationJob {
  return {
    id: 'job-1',
    project_id: 'p',
    discovery_id: 'd',
    doc_kind: 'insight',
    doc_id: 'i',
    status: 'running',
    step: 'verifier',
    attempt: 1,
    enqueued_at: new Date(Date.now() - 5_000).toISOString(),
    started_at: new Date(Date.now() - 5_000).toISOString(),
    ...overrides,
  };
}

describe('ValidationJobProgressCard', () => {
  it('shows the verifier step label when status=running step=verifier', () => {
    wrap(<ValidationJobProgressCard job={makeJob({ step: 'verifier' })} />);
    expect(screen.getByText(/Verifier running/)).toBeInTheDocument();
  });

  it('shows the refuter step label when status=running step=refuter', () => {
    wrap(<ValidationJobProgressCard job={makeJob({ step: 'refuter' })} />);
    expect(screen.getByText(/Refuter running/)).toBeInTheDocument();
  });

  it('shows the queued label when status=pending', () => {
    wrap(<ValidationJobProgressCard job={makeJob({ status: 'pending', step: undefined })} />);
    expect(screen.getByText(/Queued/)).toBeInTheDocument();
  });

  it('calls onCancel(jobId) when the Cancel button is clicked', () => {
    const onCancel = jest.fn();
    wrap(<ValidationJobProgressCard job={makeJob()} onCancel={onCancel} />);
    fireEvent.click(screen.getByRole('button', { name: /Cancel/ }));
    expect(onCancel).toHaveBeenCalledWith('job-1');
  });

  it('shows the error + Try again on status=failed', () => {
    const onRetry = jest.fn();
    wrap(
      <ValidationJobProgressCard
        job={makeJob({ status: 'failed', error: 'agent crashed: ENOMEM' })}
        onRetry={onRetry}
      />
    );
    expect(screen.getByText(/Validation failed/)).toBeInTheDocument();
    expect(screen.getByText(/agent crashed: ENOMEM/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Try again/ }));
    expect(onRetry).toHaveBeenCalled();
  });

  it('shows the cancelled message on status=cancelled', () => {
    wrap(
      <ValidationJobProgressCard
        job={makeJob({ status: 'cancelled' })}
      />
    );
    expect(screen.getByText(/Validation cancelled/)).toBeInTheDocument();
  });

  it('hides the Cancel button on terminal status', () => {
    const onCancel = jest.fn();
    wrap(<ValidationJobProgressCard job={makeJob({ status: 'cancelled' })} onCancel={onCancel} />);
    expect(screen.queryByRole('button', { name: /Cancel/ })).not.toBeInTheDocument();
  });
});
