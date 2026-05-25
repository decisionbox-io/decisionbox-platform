import { isNewValidation, isLegacyValidation } from '@/components/validation/validationShape';
import type { InsightValidation } from '@/lib/api';

describe('validationShape', () => {
  it('treats a payload with combined as new', () => {
    const v: InsightValidation = { combined: 'confirmed' };
    expect(isNewValidation(v)).toBe(true);
    expect(isLegacyValidation(v)).toBe(false);
  });

  it('treats a payload with only verifier as new', () => {
    const v: InsightValidation = {
      verifier: {
        doc_id: 'i1',
        doc_kind: 'insight',
        mode: 'verifier',
        claims_considered: [],
        claim_verdicts: [],
        overall: 'supported',
        overall_reason: '',
        lookups_used: 0,
        queries_issued: 0,
        step_reads_used: 0,
        llm_tokens_in: 0,
        llm_tokens_out: 0,
        duration_millis: 0,
      },
    };
    expect(isNewValidation(v)).toBe(true);
  });

  it('treats a payload with only legacy status as legacy', () => {
    const v: InsightValidation = { status: 'confirmed', verified_count: 5, original_count: 5 };
    expect(isLegacyValidation(v)).toBe(true);
    expect(isNewValidation(v)).toBe(false);
  });

  it('treats an empty payload as legacy (predicate is symmetric)', () => {
    const v: InsightValidation = {};
    expect(isNewValidation(v)).toBe(false);
    expect(isLegacyValidation(v)).toBe(true);
  });

  it('a transitional payload with both shapes is new (combined wins)', () => {
    const v: InsightValidation = { status: 'confirmed', combined: 'partial' };
    expect(isNewValidation(v)).toBe(true);
  });
});
