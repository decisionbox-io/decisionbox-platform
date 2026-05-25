'use client';

import { Button, Card, Group, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { useState } from 'react';
import { VerdictBadge } from './VerdictBadge';
import { ValidationBreakdownDrawer, REFUTER_DISABLED_NOTE } from './ValidationBreakdownDrawer';
import { statusMeta } from './statusMeta';
import type { InsightValidation } from '@/lib/api';

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
  title = 'Validation',
}: {
  validation: InsightValidation;
  title?: string;
}) {
  const [open, setOpen] = useState(false);
  const meta = statusMeta(validation.combined);
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
              {title}
            </Text>
          </Group>
          <VerdictBadge status={validation.combined} size="sm" />
        </Group>
        <Text size="xs" c="dimmed" mb={validation.refuter_disabled || hasBreakdown ? 8 : 0}>
          {meta.tagline}
        </Text>
        {validation.refuter_disabled && (
          <Text size="xs" c="dimmed" mb={hasBreakdown ? 8 : 0} fs="italic">
            {REFUTER_DISABLED_NOTE}
          </Text>
        )}
        {hasBreakdown && (
          <Button
            variant="subtle"
            size="xs"
            onClick={() => setOpen(true)}
            px={0}
          >
            Show breakdown
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
