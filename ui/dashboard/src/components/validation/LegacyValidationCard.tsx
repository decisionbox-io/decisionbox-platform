'use client';

// Renders the legacy validation payload — single `status` field
// plus original/verified counts plus a free-text reasoning.
// Lifted verbatim from the old inline CompactValidationCard so we can
// delete this whole file in one commit when legacy docs are retired.
//
// DO NOT add new features here. New rendering goes in
// NewValidationPanel.tsx.

import { Card, Group, Text } from '@mantine/core';
import { VerdictBadge } from './VerdictBadge';
import type { InsightValidation } from '@/lib/api';

export function LegacyValidationCard({ validation }: { validation: InsightValidation }) {
  return (
    <Card withBorder p="md">
      <Group justify="space-between" mb={6}>
        <Text
          size="xs"
          fw={600}
          tt="uppercase"
          c="dimmed"
          style={{ letterSpacing: '0.5px' }}
        >
          Validation
        </Text>
        <VerdictBadge status={validation.status} size="sm" />
      </Group>
      {(validation.original_count != null || validation.verified_count != null) && (
        <Group gap={4} mb={6}>
          {validation.original_count != null && (
            <Text size="xs" c="dimmed">{validation.original_count.toLocaleString()}</Text>
          )}
          {validation.verified_count != null && (
            <>
              <Text size="xs" c="dimmed">→</Text>
              <Text size="xs" fw={600}>{validation.verified_count.toLocaleString()} verified</Text>
            </>
          )}
        </Group>
      )}
      {validation.reasoning && (
        <Text size="xs" c="dimmed" lineClamp={3}>{validation.reasoning}</Text>
      )}
    </Card>
  );
}
