'use client';

// Renders the legacy validation payload — single `status` field
// plus original/verified counts plus a free-text reasoning.
// Lifted verbatim from the old inline CompactValidationCard so we can
// delete this whole file in one commit when legacy docs are retired.
//
// DO NOT add new features here. New rendering goes in
// NewValidationPanel.tsx.

import { Card, Group, Text } from '@mantine/core';
import { useTranslations } from 'next-intl';
import { VerdictBadge } from './VerdictBadge';
import { useFormat } from '@/lib/format';
import type { InsightValidation } from '@/lib/api';

export function LegacyValidationCard({ validation }: { validation: InsightValidation }) {
  const t = useTranslations('validation');
  const fmt = useFormat();
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
          {t('title')}
        </Text>
        <VerdictBadge status={validation.status} size="sm" />
      </Group>
      {(validation.original_count != null || validation.verified_count != null) && (
        <Group gap={4} mb={6}>
          {validation.original_count != null && (
            <Text size="xs" c="dimmed">{fmt.number(validation.original_count)}</Text>
          )}
          {validation.verified_count != null && (
            <>
              <Text size="xs" c="dimmed">→</Text>
              <Text size="xs" fw={600}>{t('nVerified', { count: fmt.number(validation.verified_count) })}</Text>
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
