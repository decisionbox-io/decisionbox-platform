'use client';

import { Card, Group, Stack, Text } from '@mantine/core';
import { VerdictBadge } from './VerdictBadge';
import { ClaimList } from './ClaimList';
import { AgentRunStats } from './AgentRunStats';
import type { StructuredVerdict } from '@/lib/api';

// Renders one of the two agents in the breakdown drawer. The verifier
// asks "is this consistent with the cited evidence?" — the refuter asks
// "is there evidence that contradicts this?". The label is rendered
// explicitly so a decision maker knows which agent's verdict they're
// reading without having to learn the verbal contract.

const AGENT_LABEL: Record<string, string> = {
  verifier: 'Verifier',
  refuter: 'Refuter',
};

const AGENT_SUBTITLE: Record<string, string> = {
  verifier: 'Checks the claim against the evidence cited by the AI.',
  refuter: 'Searches for evidence that contradicts the claim.',
};

export function AgentVerdictCard({ verdict }: { verdict: StructuredVerdict }) {
  const label = AGENT_LABEL[verdict.mode] ?? verdict.mode;
  const subtitle = AGENT_SUBTITLE[verdict.mode] ?? '';
  return (
    <Card withBorder p="md" radius="sm">
      <Stack gap="sm">
        <Group justify="space-between" align="flex-start">
          <div>
            <Text size="sm" fw={700}>{label}</Text>
            {subtitle && (
              <Text size="xs" c="dimmed">{subtitle}</Text>
            )}
          </div>
          <VerdictBadge status={verdict.overall} size="sm" />
        </Group>
        {verdict.overall_reason && (
          <Text size="xs">{verdict.overall_reason}</Text>
        )}
        <AgentRunStats verdict={verdict} />
        {/* Go encodes a nil `[]ClaimVerdict` as JSON null, which happens
            on failed / unverifiable verdicts (LLM chat failure,
            forced-final parse failure). Defensively coerce to an empty
            array so opening the drawer on those runs doesn't throw. */}
        {(() => {
          const claims = verdict.claim_verdicts ?? [];
          if (claims.length === 0) return null;
          return (
            <Stack gap={4} mt={4}>
              <Text
                size="xs"
                fw={700}
                tt="uppercase"
                c="dimmed"
                style={{ letterSpacing: '0.5px' }}
              >
                Per-claim breakdown ({claims.length})
              </Text>
              <ClaimList claims={claims} />
            </Stack>
          );
        })()}
      </Stack>
    </Card>
  );
}
