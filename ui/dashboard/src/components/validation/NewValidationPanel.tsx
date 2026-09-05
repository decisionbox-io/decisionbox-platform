'use client';

import { Button, Card, Group, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { VerdictBadge } from './VerdictBadge';
import { ValidationBreakdownDrawer } from './ValidationBreakdownDrawer';
import { DatasourceBadge } from '@/components/common/UIComponents';
import type { InsightValidation } from '@/lib/api';

// Verdict statuses that carry a localized one-line tagline. Anything
// outside the set falls back to the "unknown" tagline — matching the
// English fallback in statusMeta.
const TAGLINE_VERDICTS = new Set([
  'confirmed',
  'supported',
  'partial',
  'rejected',
  'unverifiable',
  'validation_disabled',
  'skipped_budget_cap',
  'adjusted',
  'unverified',
  'error',
]);

// Compact sidebar-sized card for the plan-v5 validation shape. Renders:
//   - Combined verdict badge.
//   - Decision-friendly tagline.
//   - "Show breakdown" button (only when verifier/refuter detail exists)
//     that opens the shared ValidationBreakdownDrawer.
//
// Used in the insight + recommendation detail sidebars. For the
// discovery-overview list, see ValidationLogRow.tsx — it uses the same
// breakdown drawer.

export function NewValidationPanel({
  validation,
  title,
}: {
  validation: InsightValidation;
  title?: string;
}) {
  const t = useTranslations('validation');
  const [open, setOpen] = useState(false);
  const status = validation.combined;
  const tagline = status && TAGLINE_VERDICTS.has(status)
    ? t(`tagline_${status}`)
    : t('tagline_unknown');
  const hasBreakdown = validation.verifier != null || validation.refuter != null;
  return (
    <>
      <Card withBorder p="md">
        <Group justify="space-between" mb={6} align="center">
          <Group gap={6}>
            <IconShieldCheck size={14} color="var(--db-text-secondary)" />
            <Text
              size="xs"
              fw={600}
              tt="uppercase"
              c="dimmed"
              style={{ letterSpacing: '0.5px' }}
            >
              {title ?? t('title')}
            </Text>
          </Group>
          <Group gap={6} align="center">
            <DatasourceBadge warehouseId={validation.warehouse_id} />
            <VerdictBadge status={validation.combined} size="sm" />
          </Group>
        </Group>
        <Text size="xs" c="dimmed" mb={validation.refuter_disabled || hasBreakdown ? 8 : 0}>
          {tagline}
        </Text>
        {validation.refuter_disabled && (
          <Text size="xs" c="dimmed" mb={hasBreakdown ? 8 : 0} fs="italic">
            {t('refuterDisabledNote')}
          </Text>
        )}
        {hasBreakdown && (
          <Button
            variant="subtle"
            size="xs"
            onClick={() => setOpen(true)}
            px={0}
          >
            {t('showBreakdown')}
          </Button>
        )}
      </Card>
      <ValidationBreakdownDrawer
        opened={open}
        onClose={() => setOpen(false)}
        validation={validation}
      />
    </>
  );
}
