'use client';

import { Box, Drawer, Group, Stack, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { useTranslations } from 'next-intl';
import { VerdictBadge } from './VerdictBadge';
import { AgentVerdictCard } from './AgentVerdictCard';
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

// Controlled drawer that renders the verifier + refuter cards for a
// single validation payload. Stateless on its own — the caller owns
// the open/close state. Used by NewValidationPanel (sidebar card) and
// ValidationLogRow (discovery-overview list).

export function ValidationBreakdownDrawer({
  opened,
  onClose,
  validation,
  headline,
}: {
  opened: boolean;
  onClose: () => void;
  validation: InsightValidation;
  headline?: string;
}) {
  const t = useTranslations('validation');
  const status = validation.combined;
  const tagline = status && TAGLINE_VERDICTS.has(status)
    ? t(`tagline_${status}`)
    : t('tagline_unknown');
  const hasBreakdown = validation.verifier != null || validation.refuter != null;
  return (
    <Drawer
      opened={opened}
      onClose={onClose}
      position="right"
      size="lg"
      title={
        <Group gap="xs">
          <IconShieldCheck size={18} />
          <Text fw={600}>{t('breakdownTitle')}</Text>
          <VerdictBadge status={validation.combined} size="sm" />
        </Group>
      }
    >
      <Stack gap="md">
        {headline && (
          <Text size="sm" fw={600}>{headline}</Text>
        )}
        <Box>
          <Text size="sm" c="dimmed">
            {tagline}
            {validation.refuter_disabled && (
              <> {t('refuterDisabledNote')}</>
            )}
          </Text>
        </Box>
        {validation.verifier && <AgentVerdictCard verdict={validation.verifier} />}
        {validation.refuter && <AgentVerdictCard verdict={validation.refuter} />}
        {!hasBreakdown && (
          <Text size="sm" c="dimmed">
            {t('noPerAgentBreakdown')}
          </Text>
        )}
      </Stack>
    </Drawer>
  );
}
