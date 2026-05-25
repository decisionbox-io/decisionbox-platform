import { statusMeta, toneToMantineColor } from '@/components/validation/statusMeta';

describe('statusMeta', () => {
  it('returns distinct tones for the seven new statuses', () => {
    expect(statusMeta('confirmed').tone).toBe('green');
    expect(statusMeta('supported').tone).toBe('green');
    expect(statusMeta('partial').tone).toBe('amber');
    expect(statusMeta('rejected').tone).toBe('red');
    expect(statusMeta('unverifiable').tone).toBe('amber');
    expect(statusMeta('validation_disabled').tone).toBe('grey');
    expect(statusMeta('skipped_budget_cap').tone).toBe('grey');
  });

  it('renders human-readable labels (not raw status keys)', () => {
    expect(statusMeta('skipped_budget_cap').label).toBe('Skipped');
    expect(statusMeta('validation_disabled').label).toBe('Disabled');
    expect(statusMeta('confirmed').label).toBe('Confirmed');
  });

  it('handles legacy values without throwing', () => {
    expect(statusMeta('adjusted').tone).toBe('amber');
    expect(statusMeta('unverified').tone).toBe('grey');
    expect(statusMeta('error').tone).toBe('red');
  });

  it('falls back gracefully for unknown values', () => {
    expect(statusMeta('whatever').label).toBe('Unknown');
    expect(statusMeta(undefined).tone).toBe('grey');
    expect(statusMeta('').tone).toBe('grey');
  });
});

describe('toneToMantineColor', () => {
  it('maps every supported tone to a Mantine palette name', () => {
    expect(toneToMantineColor('green')).toBe('green');
    expect(toneToMantineColor('amber')).toBe('yellow');
    expect(toneToMantineColor('red')).toBe('red');
    expect(toneToMantineColor('grey')).toBe('gray');
    expect(toneToMantineColor('blue')).toBe('blue');
  });
});
