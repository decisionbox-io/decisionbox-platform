import { deepMerge } from '@/i18n/deepMerge';

describe('deepMerge', () => {
  it('overlays leaves, overlay wins on collision', () => {
    const out = deepMerge({ a: '1', b: '2' }, { b: 'B', c: 'C' });
    expect(out).toEqual({ a: '1', b: 'B', c: 'C' });
  });

  it('merges nested objects recursively', () => {
    const out = deepMerge(
      { nav: { a: '1', b: '2' }, common: { save: 'Save' } },
      { nav: { b: 'B', c: 'C' } },
    );
    expect(out).toEqual({
      nav: { a: '1', b: 'B', c: 'C' },
      common: { save: 'Save' },
    });
  });

  it('enterprise override wins on a nested key (the criterion-4 behaviour)', () => {
    const base = { nav: { askInsights: 'Ask Insights', insights: 'Insights' } };
    const overlay = { nav: { askInsights: 'Ask', governance: 'Governance' } };
    const out = deepMerge(base, overlay) as { nav: Record<string, string> };
    expect(out.nav.askInsights).toBe('Ask'); // override wins
    expect(out.nav.insights).toBe('Insights'); // base preserved
    expect(out.nav.governance).toBe('Governance'); // enterprise-only added
  });

  it('empty overlay returns the base unchanged', () => {
    const base = { nav: { a: '1' } };
    expect(deepMerge(base, {})).toEqual(base);
  });

  it('does not mutate the base object', () => {
    const base = { nav: { a: '1' } };
    deepMerge(base, { nav: { b: '2' } });
    expect(base).toEqual({ nav: { a: '1' } });
  });

  it('overlay replaces (does not merge) when base leaf is a scalar', () => {
    // A scalar base value overridden by an object, and vice-versa: overlay wins
    // wholesale — no attempt to merge mismatched shapes.
    expect(deepMerge({ a: 'x' }, { a: { nested: '1' } })).toEqual({ a: { nested: '1' } });
    expect(deepMerge({ a: { nested: '1' } }, { a: 'x' })).toEqual({ a: 'x' });
  });
});
