'use client';

import { Box, Button, Group, Text } from '@mantine/core';
import { useState } from 'react';
import { VerdictBadge } from './VerdictBadge';
import { ValidationBreakdownDrawer } from './ValidationBreakdownDrawer';
import { isNewValidation } from './validationShape';
import { LegacyValidationCard } from './LegacyValidationCard';
import type { InsightValidation, ValidationLogEntry } from '@/lib/api';

// Discovery-overview list row. One entry per validated insight (or
// recommendation). Compact by default — analysis area, verdict badge,
// one-line reason — with a "Show breakdown" affordance that opens the
// shared drawer. Falls through to LegacyValidationCard for old shapes
// so the same list can mix legacy + new entries during the migration
// window.
//
// `analysis_area` is the only ValidationLogEntry-specific field we
// surface here; everything else flows through the shared InsightValidation
// adapter so the drawer renders identically across surfaces.

function logEntryToValidation(entry: ValidationLogEntry): InsightValidation {
  return {
    // Legacy mirror — kept so isNewValidation() can still fall back.
    status: entry.status,
    verified_count: entry.verified_count,
    original_count: entry.claimed_count,
    query: entry.query,
    reasoning: entry.reasoning,
    validated_at: entry.validated_at,
    // New shape — present only when the backend wrote them.
    verifier: entry.verifier,
    refuter: entry.refuter,
    combined: entry.combined,
    refuter_disabled: entry.refuter_disabled,
  };
}

export function ValidationLogRow({ entry }: { entry: ValidationLogEntry }) {
  const [open, setOpen] = useState(false);
  const validation = logEntryToValidation(entry);

  if (!isNewValidation(validation)) {
    return <LegacyValidationCard validation={validation} />;
  }

  const hasBreakdown = validation.verifier != null || validation.refuter != null;
  const oneLineReason = validation.verifier?.overall_reason
    ?? validation.refuter?.overall_reason
    ?? '';

  return (
    <>
      <Box
        style={{
          border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius)',
          padding: '10px 12px',
        }}
      >
        <Group justify="space-between" mb={4} align="center">
          <Text size="xs" fw={500}>{entry.analysis_area}</Text>
          <VerdictBadge status={validation.combined} size="xs" />
        </Group>
        {oneLineReason && (
          <Text size="xs" c="dimmed" lineClamp={2} mb={hasBreakdown ? 6 : 0}>
            {oneLineReason}
          </Text>
        )}
        {hasBreakdown && (
          <Button
            variant="subtle"
            size="compact-xs"
            onClick={() => setOpen(true)}
            px={0}
          >
            Show breakdown
          </Button>
        )}
      </Box>
      <ValidationBreakdownDrawer
        opened={open}
        onClose={() => setOpen(false)}
        validation={validation}
      />
    </>
  );
}
