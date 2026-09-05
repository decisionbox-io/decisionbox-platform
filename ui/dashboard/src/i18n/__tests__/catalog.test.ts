import en from '@/i18n/messages/en.json';
import tr from '@/i18n/messages/tr.json';

type Tree = Record<string, unknown>;

// Flatten a nested catalog into dot-path keys, so a missing or extra key at any
// depth is caught. Leaves are strings.
function flatten(obj: Tree, prefix = ''): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      Object.assign(out, flatten(value as Tree, path));
    } else {
      out[path] = value as string;
    }
  }
  return out;
}

describe('base catalog parity (en ↔ tr)', () => {
  const enFlat = flatten(en as Tree);
  const trFlat = flatten(tr as Tree);

  it('tr has exactly the same keys as en (no missing, no extra)', () => {
    const enKeys = Object.keys(enFlat).sort();
    const trKeys = Object.keys(trFlat).sort();
    const missingInTr = enKeys.filter((k) => !(k in trFlat));
    const extraInTr = trKeys.filter((k) => !(k in enFlat));
    expect({ missingInTr, extraInTr }).toEqual({ missingInTr: [], extraInTr: [] });
  });

  it('no empty values in either catalog', () => {
    const emptyEn = Object.entries(enFlat).filter(([, v]) => !v || !v.trim()).map(([k]) => k);
    const emptyTr = Object.entries(trFlat).filter(([, v]) => !v || !v.trim()).map(([k]) => k);
    expect({ emptyEn, emptyTr }).toEqual({ emptyEn: [], emptyTr: [] });
  });

  it('every leaf is a string', () => {
    for (const [k, v] of Object.entries(enFlat)) {
      expect(typeof v).toBe('string');
      expect(k).toBeTruthy();
    }
  });
});
