/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { RunErrorIndicator, normalizeRunErrors } from '@/components/common/RunErrorIndicator';

function mount(props: React.ComponentProps<typeof RunErrorIndicator>) {
  return render(
    <MantineProvider>
      <RunErrorIndicator {...props} />
    </MantineProvider>
  );
}

const LONG_ERROR =
  'bedrock/openai-compat: ValidationException: input length 200000 exceeds context length 128000 ' +
  'for model glm-5; reduce the number of tokens and retry the request';

describe('normalizeRunErrors', () => {
  it('returns [] for falsy / blank input', () => {
    expect(normalizeRunErrors(undefined)).toEqual([]);
    expect(normalizeRunErrors(null)).toEqual([]);
    expect(normalizeRunErrors('')).toEqual([]);
    expect(normalizeRunErrors('   ')).toEqual([]);
    expect(normalizeRunErrors([])).toEqual([]);
    expect(normalizeRunErrors(['', '  ', null as unknown as string])).toEqual([]);
  });

  it('wraps a single string and trims it', () => {
    expect(normalizeRunErrors('  boom  ')).toEqual(['boom']);
  });

  it('keeps only non-blank entries from a list, trimmed', () => {
    expect(normalizeRunErrors(['a', '', '  b  ', '   '])).toEqual(['a', 'b']);
  });
});

describe('RunErrorIndicator', () => {
  it('renders no indicator when there are no errors', () => {
    mount({ errors: undefined });
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders no indicator when the error list is all blanks', () => {
    mount({ errors: ['', '   '] });
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('shows a compact warning icon (not the raw error) by default', () => {
    mount({ errors: LONG_ERROR, label: 'Discovery run error' });
    // The trigger button is present, labelled for a11y...
    expect(screen.getByRole('button', { name: 'Discovery run error' })).toBeInTheDocument();
    // ...but the raw error wall is collapsed until the user expands it.
    expect(screen.queryByText(LONG_ERROR)).not.toBeInTheDocument();
  });

  it('expands the full error text when the icon is clicked', async () => {
    mount({ errors: LONG_ERROR, label: 'Discovery run error' });
    fireEvent.click(screen.getByRole('button', { name: 'Discovery run error' }));
    await waitFor(() => {
      expect(screen.getByText(LONG_ERROR)).toBeInTheDocument();
    });
  });

  it('derives a singular heading from a one-item error list', () => {
    mount({ errors: ['area A failed'] });
    expect(
      screen.getByRole('button', { name: '1 area failed during analysis' })
    ).toBeInTheDocument();
  });

  it('derives a plural heading from a multi-item error list and shows each error on expand', async () => {
    mount({ errors: ['area A failed', 'area B failed'] });
    const trigger = screen.getByRole('button', { name: '2 areas failed during analysis' });
    fireEvent.click(trigger);
    await waitFor(() => {
      expect(screen.getByText('area A failed')).toBeInTheDocument();
      expect(screen.getByText('area B failed')).toBeInTheDocument();
    });
  });

  it('toggles the detail closed again without removing the icon', async () => {
    mount({ errors: LONG_ERROR, label: 'Discovery run error' });
    const trigger = screen.getByRole('button', { name: 'Discovery run error' });
    fireEvent.click(trigger);
    await waitFor(() => expect(screen.getByText(LONG_ERROR)).toBeInTheDocument());
    fireEvent.click(trigger);
    // Detail collapses...
    await waitFor(() => expect(screen.queryByText(LONG_ERROR)).not.toBeInTheDocument());
    // ...but the indicator itself remains.
    expect(screen.getByRole('button', { name: 'Discovery run error' })).toBeInTheDocument();
  });
});
