import { runNoveltyFraction, runNoveltyLabel } from '@/components/ledger/StoryTimeline';
import type { GraphRun } from '@/lib/api';

function run(over: Partial<GraphRun> = {}): GraphRun {
  return {
    node_id: 'run:r1',
    run_id: 'r1',
    new_findings: 0,
    total_findings: 0,
    marginal_ratio: 0,
    ...over,
  } as GraphRun;
}

describe('run novelty on the story timeline', () => {
  // The bar read marginal_ratio, which divides a run's new findings by the
  // WHOLE active ledger — a growth rate. So a run that added ten entirely new
  // findings to a hundred-finding ledger was shown as "9% new", under a tooltip
  // that said "10 of 110 findings were new in this run" about a run that
  // produced ten.
  it('reports a fully novel run as 100% new, not as its growth ratio', () => {
    const r = run({ new_findings: 10, total_findings: 110, marginal_ratio: 10 / 110, run_findings: 10 });
    expect(runNoveltyFraction(r)).toBe(1);
    expect(runNoveltyLabel(r)).toBe(
      '10 of the 10 findings this run surfaced were new — the ledger now holds 110',
    );
  });

  it('reports a repeating run as mostly not-new', () => {
    const r = run({ new_findings: 10, total_findings: 110, marginal_ratio: 10 / 110, run_findings: 100 });
    expect(runNoveltyFraction(r)).toBeCloseTo(0.1);
  });

  it('a run that repeated everything is 0% new', () => {
    const r = run({ new_findings: 0, total_findings: 400, marginal_ratio: 0, run_findings: 40 });
    expect(runNoveltyFraction(r)).toBe(0);
  });

  it('falls back to the growth ratio for a run recorded before it was measured, and says so', () => {
    const r = run({ new_findings: 41, total_findings: 422, marginal_ratio: 0.0972 });
    expect(runNoveltyFraction(r)).toBeCloseTo(0.0972);
    // The label must not claim a repeat rate it cannot know.
    expect(runNoveltyLabel(r)).toContain('its repeat rate is unknown');
    expect(runNoveltyLabel(r)).not.toContain('were new —');
  });

  it('treats run_findings of zero as not measured rather than as a zero denominator', () => {
    const r = run({ new_findings: 5, total_findings: 50, marginal_ratio: 0.1, run_findings: 0 });
    expect(Number.isFinite(runNoveltyFraction(r))).toBe(true);
    expect(runNoveltyFraction(r)).toBeCloseTo(0.1);
  });

  it('clamps impossible counts instead of rendering a bar past 100%', () => {
    expect(runNoveltyFraction(run({ new_findings: 50, run_findings: 10 }))).toBe(1);
    expect(runNoveltyFraction(run({ new_findings: -5, run_findings: 10 }))).toBe(0);
    expect(runNoveltyFraction(run({ marginal_ratio: 1.5 }))).toBe(1);
  });
});
