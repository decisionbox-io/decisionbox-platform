'use client';

import { MultiSelect, NumberInput, Stack } from '@mantine/core';
import type { AnalysisAreaConfig, RiskCategory } from '@/lib/api';

// The per-area half of the severity weighting, as an editor.
//
// Two controls, and they are not interchangeable. The CATEGORIES decide both
// the area's weight -- the largest of their configured weights -- and the risk
// flags every finding in it carries, so they are the normal way to weight an
// area: a statement about a category is true of every area that maps to it,
// including the one a pack update adds later. The OVERRIDE is for the single
// area a business weights differently from the rest of its category, and it
// wins.
//
// A component rather than JSX inline on the page, because the enterprise
// dashboard is this app with its own overlay copied over it: an editor written
// inline in the page would be invisible to the overlay-drift guard, and the
// overlay's older copy of the page would shadow the feature silently on every
// enterprise deployment. That has happened three times, most recently to the
// schema editor, which shipped dark for five releases.

// MIN_WEIGHT is the smallest weight this editor will produce. Not a contract
// value: the engine's range is (0, maxWeight], which is open at the bottom and
// so has no smallest member -- a step has to come from somewhere, and two
// decimal places is what the field renders.
//
// The CEILING is a contract value and comes from the engine, through the
// calibration the API serves. A literal here would be the way the editor comes
// to offer a weight the save refuses.
const MIN_WEIGHT = 0.01;

export function AreaRiskConfig({
  area,
  taxonomy,
  globalCategories,
  maxWeight,
  categoryWeights,
  onChange,
}: {
  area: AnalysisAreaConfig;
  // The pack's own risk categories, as seeded onto the project. Empty for a
  // pack that declares none, which still leaves the global ids selectable.
  taxonomy: RiskCategory[] | undefined;
  // The engine's cross-cutting ids and the top of its weight range, from the
  // calibration the API serves. Both undefined while that request is in
  // flight, or against an API older than this build -- which costs the global
  // ids and the ceiling, and leaves the pack's own categories selectable.
  globalCategories: string[] | undefined;
  maxWeight: number | undefined;
  // The project's own overrides of a category's weight. They WIN over the
  // taxonomy's, so a label that ignored them would advertise a weight the
  // scorer does not use -- a pack's 2.5 shown beside a category the project
  // has set to 0.5.
  categoryWeights: Record<string, number> | undefined;
  onChange: (updates: Partial<AnalysisAreaConfig>) => void;
}) {
  const declared = taxonomy ?? [];
  const global = globalCategories ?? [];

  // The weight the SCORER will apply: the project's override when it has one,
  // else what the taxonomy declares.
  //
  // The label is a factual claim about what choosing this category is worth,
  // so showing the pack's number beside a category the project has re-weighted
  // tells an operator the opposite of what the next run will do.
  const effectiveWeight = (id: string, declaredWeight?: number): number | undefined =>
    categoryWeights?.[id] ?? declaredWeight;

  const withWeight = (id: string, label: string, declaredWeight?: number) => {
    const weight = effectiveWeight(id, declaredWeight);
    return { value: id, label: weight ? `${label} (×${weight})` : label };
  };

  const options = [
    ...declared.map((cat) => withWeight(cat.id, cat.label, cat.weight)),
    // The global ids last, and only where the pack has not declared one of
    // them itself -- a pack that declares `compliance` with a weight of its
    // own must not be offered the id twice. A project CAN weight a global id
    // the pack says nothing about, which is how a deployment weights
    // `compliance` without its pack declaring a taxonomy at all, so these
    // carry a weight too when one is set.
    ...global
      .filter((id) => !declared.some((cat) => cat.id === id))
      .map((id) => withWeight(id, id.replace(/[_-]+/g, ' '))),
  ];

  return (
    <Stack gap="sm">
      <MultiSelect
        label="Risk categories"
        description="What kind of risk a finding here is. The area's weight is the largest of their weights."
        placeholder={options.length === 0 ? 'No risk categories available' : 'Select categories'}
        data={options}
        value={area.risk_categories ?? []}
        disabled={options.length === 0}
        onChange={(value) => onChange({ risk_categories: value })}
        data-testid="area-risk-categories"
        clearable
        searchable
      />
      <NumberInput
        label="Severity weight override"
        description={maxWeight
          ? `Overrides the categories' weight for this area alone. ${MIN_WEIGHT}–${maxWeight}; leave empty to use the categories.`
          : "Overrides the categories' weight for this area alone. Leave empty to use the categories."}
        placeholder="From the categories"
        value={area.severity_weight ?? ''}
        min={MIN_WEIGHT}
        max={maxWeight}
        step={0.05}
        decimalScale={2}
        data-testid="area-severity-weight"
        onChange={(value) => onChange({
          // An empty field clears the override rather than storing a zero:
          // zero is not a legal weight, and "no override" is the state the
          // field is cleared INTO.
          severity_weight: value === '' || value === null ? undefined : Number(value),
        })}
      />
    </Stack>
  );
}
