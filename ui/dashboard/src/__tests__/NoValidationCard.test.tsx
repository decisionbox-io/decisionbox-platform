/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { NoValidationCard } from '@/components/validation/NoValidationCard';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

describe('NoValidationCard', () => {
  it('renders the Run validation button when validation is enabled', () => {
    const onRun = jest.fn();
    wrap(<NoValidationCard validationEnabled={true} onRun={onRun} />);
    const btn = screen.getByRole('button', { name: /Run validation/ });
    expect(btn).toBeInTheDocument();
    fireEvent.click(btn);
    expect(onRun).toHaveBeenCalledTimes(1);
  });

  it('renders the disabled message + Settings link when validation is off', () => {
    wrap(
      <NoValidationCard
        validationEnabled={false}
        settingsHref="/projects/abc/settings#advanced"
      />
    );
    expect(screen.getByText(/Validation is disabled/)).toBeInTheDocument();
    const link = screen.getByRole('link', { name: /Settings/ });
    expect(link).toHaveAttribute('href', '/projects/abc/settings#advanced');
    expect(screen.queryByRole('button', { name: /Run validation/ })).not.toBeInTheDocument();
  });

  it('disables the Run button while a parent enqueue is in flight', () => {
    wrap(<NoValidationCard validationEnabled={true} onRun={() => {}} running={true} />);
    expect(screen.getByRole('button', { name: /Run validation/ })).toBeDisabled();
  });
});
