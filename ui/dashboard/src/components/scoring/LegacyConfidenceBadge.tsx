'use client';

import { Badge, HoverCard, Stack, Text } from '@mantine/core';
import { ConfidenceBar } from '@/components/common/UIComponents';

// A document written before confidence was derived.
//
// Its number is the analysis model's own guess, and no existing document is
// rewritten — so rather than render it as a band it cannot support, this shows
// the old number through the old bar with a *legacy* marker and one line
// saying what it is. Measured over 160 such insights the number ran 0.75-0.99
// with a mean of 0.93, which is why presenting it as a grade would be worse
// than presenting it as history.
//
// Re-running validation on the document is what gives it a real band; the
// not-validated card already offers that, and this deliberately does not
// duplicate the call to action.

export function LegacyConfidenceBadge({ confidence }: { confidence: number }) {
  return (
    <HoverCard width={300} shadow="md" withinPortal position="bottom-start" openDelay={120}>
      <HoverCard.Target>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'help' }}>
          <ConfidenceBar confidence={confidence} />
          <Badge size="xs" variant="light" color="gray" data-testid="confidence-legacy">
            legacy
          </Badge>
        </span>
      </HoverCard.Target>
      <HoverCard.Dropdown>
        <Stack gap={4}>
          <Text size="sm" fw={600}>Legacy confidence</Text>
          <Text size="xs" c="dimmed">
            This number was written by the analysis model before confidence was derived from
            the validation verdict. It is shown as stored and has not been recalculated.
          </Text>
        </Stack>
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
