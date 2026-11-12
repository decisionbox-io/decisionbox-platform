import { isRestated, restatedToggleLabel, splitRestated } from '@/lib/restated';

// Phase 9 marks a document a restatement of another and the API's list and
// count endpoints exclude it. The dashboard reads the copy EMBEDDED in the
// discovery, so until #539 the suppression reached the API and none of the
// screens anybody looks at. These are the rules the three pages share.

describe('isRestated', () => {
  it('treats an absent mark as "not a restatement", not as unknown', () => {
    // `duplicate_of` is omitempty on the wire, so every document written
    // before the mark existed has no such key — which is most of an existing
    // deployment's corpus. Reading absence as unknown would hide all of it.
    expect(isRestated({})).toBe(false);
    expect(isRestated({ duplicate_of: undefined })).toBe(false);
    expect(isRestated({ duplicate_of: '' })).toBe(false);
  });

  it('is a restatement when it points at something', () => {
    expect(isRestated({ duplicate_of: 'ins-1', similarity_score: 0.91 })).toBe(true);
  });
});

describe('splitRestated', () => {
  const docs = [
    { id: 'a' },
    { id: 'b', duplicate_of: 'a', similarity_score: 0.93 },
    { id: 'c' },
    { id: 'd', duplicate_of: 'c', similarity_score: 0.9 },
  ];

  it('splits without losing or reordering anything', () => {
    const { shown, restated } = splitRestated(docs);
    expect(shown.map(d => d.id)).toEqual(['a', 'c']);
    expect(restated.map(d => d.id)).toEqual(['b', 'd']);
    expect(shown.length + restated.length).toBe(docs.length);
  });

  it('never hides a canonical document', () => {
    // The thing a restatement points AT must stay visible, or hiding the
    // duplicate removes the information instead of de-duplicating it.
    const { shown } = splitRestated(docs);
    for (const r of splitRestated(docs).restated) {
      expect(shown.some(d => d.id === r.duplicate_of)).toBe(true);
    }
  });

  it('handles a list with nothing marked, and an empty list', () => {
    expect(splitRestated([{ id: 'a' }]).restated).toEqual([]);
    expect(splitRestated([])).toEqual({ shown: [], restated: [] });
  });
});

describe('restatedToggleLabel', () => {
  it('reads as English for one and for many', () => {
    expect(restatedToggleLabel(1, false)).toBe('Show 1 restated item');
    expect(restatedToggleLabel(3, false)).toBe('Show 3 restated items');
  });

  it('says what the click will do, not what the state is', () => {
    expect(restatedToggleLabel(3, true)).toBe('Hide 3 restated items');
  });
});
