'use client';

import { Badge, HoverCard, Stack, Text } from '@mantine/core';
import { SeverityBadge as SeverityWord } from '@/components/common/UIComponents';

// A document written before severity was derived.
//
// Its word is the analysis model's own assessment of the finding it had just
// produced, and no existing document is rewritten — so rather than render it
// as a band it cannot support, this shows the stored word with a *legacy*
// marker and one line saying what it is. `critical` only ever appears here:
// the vocabulary is three bands now, and the word is the top priority tier.
//
// Re-running validation on the document does NOT give it a severity band:
// severity is magnitude × weight, and a validation run changes neither. Only a
// new discovery run grades a finding on the current engine, so this
// deliberately offers no call to action.

export function LegacySeverityBadge({ severity }: { severity: string }) {
  if (!severity) return null;
  return (
    <HoverCard width={300} shadow="md" withinPortal position="bottom-start" openDelay={120}>
      <HoverCard.Target>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'help' }}>
          <SeverityWord severity={severity} type="severity" />
          <Badge size="xs" variant="light" color="gray" data-testid="severity-legacy">
            legacy
          </Badge>
        </span>
      </HoverCard.Target>
      <HoverCard.Dropdown>
        <Stack gap={4}>
          <Text size="sm" fw={600}>Legacy severity</Text>
          <Text size="xs" c="dimmed">
            This word was written by the analysis model before severity was derived from the
            figures the platform re-checks. It is shown as stored and has not been
            recalculated. A new discovery run grades the finding on the current engine.
          </Text>
        </Stack>
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
