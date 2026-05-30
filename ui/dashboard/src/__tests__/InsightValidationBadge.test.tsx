/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import {
  InsightValidationBadge,
  resolveValidationStatus,
  validationSortRank,
} from '@/components/validation/InsightValidationBadge';
import type { InsightValidation } from '@/lib/api';

describe('resolveValidationStatus', () => {
  it('prefers the new-shape combined verdict', () => {
    const v: InsightValidation = { combined: 'rejected', status: 'verified' };
    expect(resolveValidationStatus(v)).toBe('rejected');
  });

  it('falls back to the legacy status when combined is absent', () => {
    const v: InsightValidation = { status: 'adjusted' };
    expect(resolveValidationStatus(v)).toBe('adjusted');
  });

  it('returns the neutral "unverified" state when validation is undefined', () => {
    expect(resolveValidationStatus(undefined)).toBe('unverified');
  });

  it('returns "unverified" when neither combined nor status is set', () => {
    expect(resolveValidationStatus({})).toBe('unverified');
  });
});

describe('validationSortRank', () => {
  it('ranks positive verdicts ahead of the neutral fallback', () => {
    expect(validationSortRank({ combined: 'confirmed' }))
      .toBeLessThan(validationSortRank({ combined: 'supported' }));
    expect(validationSortRank({ combined: 'supported' }))
      .toBeLessThan(validationSortRank(undefined));
  });

  it('keeps "agent never ran" states behind real verdicts', () => {
    expect(validationSortRank({ combined: 'rejected' }))
      .toBeLessThan(validationSortRank({ combined: 'validation_disabled' }));
    expect(validationSortRank({ combined: 'skipped_budget_cap' }))
      .toBeLessThan(validationSortRank(undefined));
  });

  it('sorts a mixed list trustworthy-first', () => {
    const list: (InsightValidation | undefined)[] = [
      undefined,
      { combined: 'supported' },
      { combined: 'confirmed' },
      { combined: 'partial' },
    ];
    const sorted = [...list].sort((a, b) => validationSortRank(a) - validationSortRank(b));
    expect(sorted.map(v => resolveValidationStatus(v))).toEqual([
      'confirmed', 'supported', 'partial', 'unverified',
    ]);
  });
});

function mount(validation?: InsightValidation) {
  return render(
    <MantineProvider>
      <InsightValidationBadge validation={validation} />
    </MantineProvider>,
  );
}

describe('InsightValidationBadge', () => {
  it('renders the combined verdict label', () => {
    mount({ combined: 'supported' });
    expect(screen.getByText('Supported')).toBeInTheDocument();
  });

  it('renders the neutral "Unverified" label when validation is absent', () => {
    mount(undefined);
    expect(screen.getByText('Unverified')).toBeInTheDocument();
  });

  it('renders the legacy status when combined is missing', () => {
    mount({ status: 'adjusted' });
    expect(screen.getByText('Adjusted')).toBeInTheDocument();
  });
});
