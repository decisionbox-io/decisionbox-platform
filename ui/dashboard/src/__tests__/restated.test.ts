import { collapseByKey, isRestated, restatedToggleLabel, splitRestated } from '@/lib/restated';

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

describe('collapseByKey', () => {
  // These lists show one row per subject across runs. They kept whichever came
  // first, which breaks once restatements are hidden: if the first row for a
  // subject is a restatement and the canonical it points at shares the key,
  // the canonical was dropped by the collapse and the restatement was then
  // hidden by the split — so a subject reported TWICE disappeared entirely.
  const key = (d: { title: string }) => d.title;

  it('keeps a subject visible when the newest row is the restatement', () => {
    const rows = [
      { title: 'Reprice the long tail', duplicate_of: 'rec-old', similarity_score: 0.95 },
      { title: 'Reprice the long tail' },
    ];
    const collapsed = collapseByKey(rows, key);
    expect(collapsed).toHaveLength(1);
    expect(isRestated(collapsed[0])).toBe(false);
    // And therefore it survives the split that hides restatements.
    expect(splitRestated(collapsed).shown).toHaveLength(1);
  });

  it('keeps the first row when neither is a restatement', () => {
    // Previous behaviour: the newest wins, and these lists are built
    // newest-run first.
    const rows = [
      { title: 'Reprice the long tail', id: 'new' },
      { title: 'Reprice the long tail', id: 'old' },
    ];
    expect(collapseByKey(rows, key).map(r => r.id)).toEqual(['new']);
  });

  it('does not replace a canonical with a later restatement', () => {
    const rows = [
      { title: 'Reprice the long tail', id: 'canonical' },
      { title: 'Reprice the long tail', id: 'restated', duplicate_of: 'canonical' },
    ];
    expect(collapseByKey(rows, key).map(r => r.id)).toEqual(['canonical']);
  });

  it('leaves distinct subjects alone and preserves their order', () => {
    const rows = [{ title: 'a' }, { title: 'b' }, { title: 'c' }];
    expect(collapseByKey(rows, key).map(r => r.title)).toEqual(['a', 'b', 'c']);
  });

  it('keeps a subject whose only row is a restatement, for the toggle to reveal', () => {
    // It must still be reachable: the suppression is a judgement that can be
    // wrong, and this is the only row for that subject.
    const rows = [{ title: 'only copy', duplicate_of: 'elsewhere', similarity_score: 0.9 }];
    const collapsed = collapseByKey(rows, key);
    expect(collapsed).toHaveLength(1);
    expect(splitRestated(collapsed).restated).toHaveLength(1);
  });
});
