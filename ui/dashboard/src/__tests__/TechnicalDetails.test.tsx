/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import TechnicalDetails from '@/components/common/TechnicalDetails';

function renderWithMantine(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

describe('TechnicalDetails', () => {
  it('starts collapsed — button reads "Show"', () => {
    renderWithMantine(
      <TechnicalDetails>
        <div>secret sql</div>
      </TechnicalDetails>
    );
    expect(screen.getByRole('button')).toHaveTextContent(/show/i);
    // aria-expanded reflects collapsed state for screen readers.
    expect(screen.getByRole('button')).toHaveAttribute('aria-expanded', 'false');
  });

  it('reveals children when clicked and toggles the label to "Hide"', () => {
    renderWithMantine(
      <TechnicalDetails>
        <div data-testid="secret">secret sql</div>
      </TechnicalDetails>
    );
    fireEvent.click(screen.getByRole('button'));
    expect(screen.getByRole('button')).toHaveTextContent(/hide/i);
    expect(screen.getByRole('button')).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByTestId('secret')).toBeInTheDocument();
  });

  it('resets to collapsed after unmount/remount — no persistence', () => {
    // The "no persistence" decision is load-bearing. Opening once on page A
    // must NOT leak to page B. We simulate that by unmounting + remounting.
    const { unmount } = renderWithMantine(
      <TechnicalDetails>
        <div>x</div>
      </TechnicalDetails>
    );
    fireEvent.click(screen.getByRole('button'));
    expect(screen.getByRole('button')).toHaveTextContent(/hide/i);

    unmount();

    renderWithMantine(
      <TechnicalDetails>
        <div>x</div>
      </TechnicalDetails>
    );
    // Fresh mount — back to "Show".
    expect(screen.getByRole('button')).toHaveTextContent(/show/i);
    expect(screen.getByRole('button')).toHaveAttribute('aria-expanded', 'false');
  });

  it('accepts a custom label', () => {
    renderWithMantine(
      <TechnicalDetails label="SQL queries">
        <div>x</div>
      </TechnicalDetails>
    );
    expect(screen.getByRole('button')).toHaveTextContent(/show sql queries/i);
  });
});
