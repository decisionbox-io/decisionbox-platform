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
  labels = 'words',
}: {
  scoring: Scoring | undefined;
  size?: 'xs' | 'sm' | 'md';
  /**
   * How a category reads. `words` is the humanised form ("Operational cost"),
   * which is right where the chip sits in prose -- the detail page and the
   * derivation. `ids` is the category id as authored ("operational-cost"),
   * which is right in a LIST, where the chip sits next to the analysis-area
   * id and two spellings of the same kind of slug, one shouted and one not,
   * read as two different kinds of thing.
   */
  labels?: 'words' | 'ids';
}) {
  const flags = scoring?.risk_flags;
  if (!flags || flags.length === 0) {
    return null;
  }

  return (
    <Group gap="xs" data-testid="risk-flags">
      {flags.map((flag) => (
        <Badge
          key={flag}
          variant="light"
          color="gray"
          size={size}
          // An id is quoted, not announced: uppercasing `operational-cost`
          // would make it a third spelling rather than the one it is.
          tt={labels === 'ids' ? 'none' : undefined}
          // NOT the band labels' width floor. Those are a closed vocabulary
          // of short words, so a floor is safe; a category id is whatever a
          // pack's author wrote, and one long enough overflows its cell
          // instead of giving way -- `customer_identity_and_access_management`
          // ran 100px past the card's right edge in the assessment panel.
          // These wrap inside the cell instead, so a long id costs a line
          // rather than disappearing.
          style={{ height: 'auto', whiteSpace: 'normal' }}
          styles={{ label: { whiteSpace: 'normal', overflow: 'visible', textOverflow: 'clip', overflowWrap: 'anywhere' } }}
          data-testid={`risk-flag-${flag}`}
        >
          {labels === 'ids' ? flag : humanise(flag)}
        </Badge>
      ))}
    </Group>
  );
}
