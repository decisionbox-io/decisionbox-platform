'use client';

import { Box, Drawer, Group, Stack, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { VerdictBadge } from './VerdictBadge';
import { AgentVerdictCard } from './AgentVerdictCard';
import { statusMeta } from './statusMeta';
import type { InsightValidation } from '@/lib/api';

// Reason copy used when the backend disabled the refuter for this run.
// Rendered as a small italic note so the decision maker knows the
// combined verdict reflects only the verifier (not silently missing
// data).
const REFUTER_DISABLED_NOTE = 'Refuter was disabled for this run — verdict reflects the verifier only.';

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
  const meta = statusMeta(validation.combined);
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
          <Text fw={600}>Validation breakdown</Text>
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
            {meta.tagline}
            {validation.refuter_disabled && (
              <> {REFUTER_DISABLED_NOTE}</>
            )}
          </Text>
        </Box>
        {validation.verifier && <AgentVerdictCard verdict={validation.verifier} />}
        {validation.refuter && <AgentVerdictCard verdict={validation.refuter} />}
        {!hasBreakdown && (
          <Text size="sm" c="dimmed">
            No per-agent breakdown available for this verdict.
          </Text>
        )}
      </Stack>
    </Drawer>
  );
}

export { REFUTER_DISABLED_NOTE };
