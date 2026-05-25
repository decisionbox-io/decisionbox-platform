'use client';

import { Box, Code, Group, Text } from '@mantine/core';
import { IconDatabase, IconSearch, IconAlertCircle } from '@tabler/icons-react';
import type { ClaimEvidence } from '@/lib/api';

// Decision-maker friendly evidence rendering. Three cases:
//   1. step_row   — a row from a cited exploration step. Show step #
//                   + the row as a key:value list.
//   2. warehouse_query — the refuter ran new SQL. Show the query.
//   3. none / empty   — explicitly tell the user no evidence was given.
//
// `additional_rows` carries the count of further rows the agent saw
// but didn't pick — we expose it so the user can tell whether the
// evidence row is representative.

function RowTable({ row }: { row: Record<string, unknown> }) {
  const entries = Object.entries(row);
  if (entries.length === 0) {
    return <Text size="xs" c="dimmed">(empty row)</Text>;
  }
  return (
    <Box
      style={{
        display: 'grid',
        gridTemplateColumns: 'minmax(0, max-content) minmax(0, 1fr)',
        columnGap: 12,
        rowGap: 2,
        fontSize: 12,
        fontVariantNumeric: 'tabular-nums',
      }}
    >
      {entries.map(([k, v]) => (
        <span key={k} style={{ display: 'contents' }}>
          <Text size="xs" c="dimmed">{k}</Text>
          <Text size="xs" fw={500} style={{ wordBreak: 'break-word' }}>
            {v === null ? '∅' : typeof v === 'object' ? JSON.stringify(v) : String(v)}
          </Text>
        </span>
      ))}
    </Box>
  );
}

export function EvidenceCell({ evidence }: { evidence: ClaimEvidence | undefined }) {
  if (!evidence || !evidence.kind || evidence.kind === 'none') {
    return (
      <Group gap={6} c="dimmed">
        <IconAlertCircle size={12} />
        <Text size="xs" c="dimmed">No evidence attached.</Text>
      </Group>
    );
  }

  if (evidence.kind === 'step_row') {
    return (
      <Box>
        <Group gap={6} mb={4}>
          <IconDatabase size={12} />
          <Text size="xs" fw={600}>
            {evidence.step_id != null ? `Row from step ${evidence.step_id}` : 'Row from cited step'}
          </Text>
          {evidence.additional_rows != null && evidence.additional_rows > 0 && (
            <Text size="xs" c="dimmed">
              (+{evidence.additional_rows} more {evidence.additional_rows === 1 ? 'row' : 'rows'} not shown)
            </Text>
          )}
        </Group>
        {evidence.row && <RowTable row={evidence.row} />}
      </Box>
    );
  }

  if (evidence.kind === 'warehouse_query') {
    return (
      <Box>
        <Group gap={6} mb={4}>
          <IconSearch size={12} />
          <Text size="xs" fw={600}>Counter-query run by refuter</Text>
        </Group>
        {evidence.query_sql && (
          <Code block style={{ fontSize: 10, maxHeight: 120, overflow: 'auto' }}>
            {evidence.query_sql}
          </Code>
        )}
        {evidence.row && (
          <Box mt={4}>
            <Text size="xs" c="dimmed" mb={2}>Result:</Text>
            <RowTable row={evidence.row} />
          </Box>
        )}
      </Box>
    );
  }

  return null;
}
