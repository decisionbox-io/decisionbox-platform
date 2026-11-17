'use client';

import { Badge, Group } from '@mantine/core';
import type { Scoring } from '@/lib/api';

// The business-risk flags a finding carries: what KIND of risk it is, beside
// the severity band that says how much it matters.
//
// Derived, like every other field of the scoring block: a flag is the risk
// category the finding's analysis area maps to, so the model that wrote the
// finding cannot give it one. That is why these render as plain labels rather
// than as anything a reader might take for an assessment — the assessment is
// the band next to them.
//
// Renders NOTHING when a document carries no flags, which is every document on
// a deployment whose pack declares no risk categories. An empty chip row with
// a heading over it would read as "no business risk", which is not what an
// unconfigured taxonomy means.

// humanise turns a category id into the words a reader sees: `revenue_leak` ->
// "Revenue leak".
//
// From the id rather than from the pack's own label, deliberately. The label
// lives on the project's taxonomy, which these surfaces do not load -- an
// insight list fetching the prompts document to spell three chips would be a
// request per page for a cosmetic gain. The ids are authored as readable slugs
// for exactly this reason, and the breakdown on the insight page quotes the
// one that set the weight in the same form.
function humanise(id: string): string {
  const words = id.replace(/[_-]+/g, ' ').trim();
  if (!words) return id;
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function RiskFlagChips({
  scoring,
  size = 'sm',
}: {
  scoring: Scoring | undefined;
  size?: 'xs' | 'sm' | 'md';
}) {
  const flags = scoring?.risk_flags;
  if (!flags || flags.length === 0) {
    return null;
  }

  return (
    <Group gap="xs" data-testid="risk-flags">
      {flags.map((flag) => (
        <Badge key={flag} variant="light" color="gray" size={size} data-testid={`risk-flag-${flag}`}>
          {humanise(flag)}
        </Badge>
      ))}
    </Group>
  );
}
