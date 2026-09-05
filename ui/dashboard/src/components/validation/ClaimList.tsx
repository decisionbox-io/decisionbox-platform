'use client';

import { Card, Group, Stack, Text } from '@mantine/core';
import { useTranslations } from 'next-intl';
import { VerdictBadge } from './VerdictBadge';
import { EvidenceCell } from './EvidenceCell';
import type { ClaimVerdict } from '@/lib/api';

// Per-claim breakdown. The headline claim is pinned first regardless of
// its position in the input array — it's the load-bearing claim for the
// decision maker. Within the remaining claims we preserve the original
// order (it mirrors the document's narrative).

function sortHeadlineFirst(claims: ClaimVerdict[]): ClaimVerdict[] {
  const headline = claims.find((c) => c.is_headline);
  const rest = claims.filter((c) => !c.is_headline);
  return headline ? [headline, ...rest] : rest;
}

export function ClaimList({ claims }: { claims: ClaimVerdict[] }) {
  const t = useTranslations('validation');
  if (claims.length === 0) {
    return (
      <Text size="xs" c="dimmed">{t('noClaimVerdicts')}</Text>
    );
  }
  const sorted = sortHeadlineFirst(claims);
  return (
    <Stack gap="xs">
      {sorted.map((c, idx) => (
        <Card key={idx} withBorder p="sm" radius="sm">
          <Group justify="space-between" align="flex-start" mb={4} wrap="nowrap">
            <Group gap={6} align="center">
              {c.is_headline && (
                <Text
                  size="xs"
                  fw={700}
                  tt="uppercase"
                  c="dimmed"
                  style={{ letterSpacing: '0.5px' }}
                >
                  {t('headline')}
                </Text>
              )}
              <VerdictBadge status={c.status} size="xs" />
            </Group>
          </Group>
          <Text size="sm" fw={500} mb={4}>{c.claim_text}</Text>
          {c.reasoning && (
            <Text size="xs" c="dimmed" mb={6}>{c.reasoning}</Text>
          )}
          <EvidenceCell evidence={c.evidence} />
        </Card>
      ))}
    </Stack>
  );
}
