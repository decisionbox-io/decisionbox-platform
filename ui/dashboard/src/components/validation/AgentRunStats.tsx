'use client';

import { Group, Text } from '@mantine/core';
import type { StructuredVerdict } from '@/lib/api';

// One-line technical readout for a single agent run (verifier or
// refuter). Shown under the agent's verdict header in the breakdown
// drawer — keeps the high-level panel free of jargon while still
// surfacing the audit numbers a power user looks for.

function formatTokens(n: number | undefined): string {
  if (n == null || n <= 0) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

function formatDuration(ms: number | undefined): string {
  if (ms == null || ms <= 0) return '0ms';
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${s}s`;
}

export function AgentRunStats({ verdict }: { verdict: StructuredVerdict }) {
  const stats: Array<[string, string]> = [
    ['Tokens in', formatTokens(verdict.llm_tokens_in)],
    ['Tokens out', formatTokens(verdict.llm_tokens_out)],
    ['Duration', formatDuration(verdict.duration_millis)],
    ['SQL queries', String(verdict.queries_issued ?? 0)],
    ['Step reads', String(verdict.step_reads_used ?? 0)],
  ];
  return (
    <Group gap="md" wrap="wrap">
      {stats.map(([label, value]) => (
        <Group key={label} gap={4} align="baseline">
          <Text size="xs" c="dimmed">{label}</Text>
          <Text size="xs" fw={600} style={{ fontVariantNumeric: 'tabular-nums' }}>{value}</Text>
        </Group>
      ))}
    </Group>
  );
}
