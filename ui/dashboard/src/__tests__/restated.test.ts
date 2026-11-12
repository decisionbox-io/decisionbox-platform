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
  // These lists show one row per subject across runs. Collapsing to a single
  // row breaks in both directions once restatements are hidden — drop the
  // canonical and a twice-reported subject vanishes; drop the restatement and
  // the reveal control has nothing to reveal. Both happened, in consecutive
  // review rounds.
  const key = (d: { title: string }) => d.title;

  it('keeps the canonical AND the restatement for one subject', () => {
    const rows = [
      { title: 'Reprice the long tail', id: 'restated', duplicate_of: 'canonical', similarity_score: 0.95 },
      { title: 'Reprice the long tail', id: 'canonical' },
    ];
    const collapsed = collapseByKey(rows, key);
    expect(collapsed).toHaveLength(2);

    const { shown, restated } = splitRestated(collapsed);
    // Default view: the canonical, so the subject is never invisible.
    expect(shown.map(r => r.id)).toEqual(['canonical']);
    // Toggle: the restatement, with the score that hid it.
    expect(restated.map(r => r.id)).toEqual(['restated']);
    expect(restated[0].similarity_score).toBe(0.95);
  });

  it('still collapses repeats of the same kind, newest first', () => {
    const rows = [
      { title: 'a', id: 'new' },
      { title: 'a', id: 'old' },
      { title: 'a', id: 'dup-new', duplicate_of: 'x' },
      { title: 'a', id: 'dup-old', duplicate_of: 'x' },
    ];
    expect(collapseByKey(rows, key).map(r => r.id)).toEqual(['new', 'dup-new']);
  });

  it('leaves distinct subjects alone and preserves their order', () => {
    const rows = [{ title: 'a' }, { title: 'b' }, { title: 'c' }];
    expect(collapseByKey(rows, key).map(r => r.title)).toEqual(['a', 'b', 'c']);
  });

  it('keeps a subject whose only row is a restatement, for the toggle to reveal', () => {
    const rows = [{ title: 'only copy', duplicate_of: 'elsewhere', similarity_score: 0.9 }];
    const collapsed = collapseByKey(rows, key);
    expect(splitRestated(collapsed).restated).toHaveLength(1);
    expect(splitRestated(collapsed).shown).toHaveLength(0);
  });

  it('the toggle count is the number of rows it can actually produce', () => {
    // The count and the reveal come from the same collapsed list, so a header
    // can never offer more than the toggle will show.
    const rows = [
      { title: 'a' }, { title: 'a', duplicate_of: 'a1' },
      { title: 'b', duplicate_of: 'b1' },
      { title: 'c' },
    ];
    const { shown, restated } = splitRestated(collapseByKey(rows, key));
    expect(shown).toHaveLength(2);
    expect(restated).toHaveLength(2);
  });
});
