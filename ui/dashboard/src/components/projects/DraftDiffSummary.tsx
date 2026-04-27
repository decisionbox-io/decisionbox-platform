'use client';

import { useEffect, useMemo, useState } from 'react';
import { Badge, Card, Group, List, Stack, Text } from '@mantine/core';
import { api, DomainPack } from '@/lib/api';

export interface DraftDiffSummaryProps {
  draft: DomainPack;
}

// DraftDiffSummary compares the LLM-generated draft pack against the
// nearest built-in pack and surfaces what's new or different. The
// "nearest" pack is picked by Jaccard similarity over category-name
// tokens — a cheap heuristic that works well in practice (e.g., a
// "social-game" draft lines up with the gaming pack, an "online-shop"
// draft lines up with ecommerce). Excludes the draft itself + the
// system-test pack from the candidate set.
export default function DraftDiffSummary({ draft }: DraftDiffSummaryProps) {
  const [packs, setPacks] = useState<DomainPack[] | null>(null);

  useEffect(() => {
    api.listDomainPacks()
      .then(setPacks)
      .catch(() => setPacks([]));
  }, []);

  const baseline = useMemo(() => pickBaseline(draft, packs ?? []), [draft, packs]);

  if (packs === null) return null; // still loading
  if (!baseline) return null;       // no built-in to compare against

  const newCategories = diffByName(draft.categories, baseline.categories);
  const newAreas = diffByName(draft.analysis_areas.base, baseline.analysis_areas.base);
  const newProfileFields = diffProfileFields(draft.profile_schema.base, baseline.profile_schema.base);

  return (
    <Card withBorder padding="sm" bg="var(--db-bg-muted)">
      <Stack gap={8}>
        <Group gap={6}>
          <Text size="sm" fw={500}>What&apos;s unique about this draft</Text>
          <Badge size="sm" variant="light">vs {baseline.name}</Badge>
        </Group>
        <DiffRow label="Categories" total={draft.categories.length} baseline={baseline.categories.length} extras={newCategories} />
        <DiffRow label="Analysis areas" total={draft.analysis_areas.base.length} baseline={baseline.analysis_areas.base.length} extras={newAreas} />
        <DiffRow label="Profile fields" total={countProfileFields(draft.profile_schema.base)} baseline={countProfileFields(baseline.profile_schema.base)} extras={newProfileFields} />
      </Stack>
    </Card>
  );
}

function DiffRow({ label, total, baseline, extras }: {
  label: string;
  total: number;
  baseline: number;
  extras: string[];
}) {
  return (
    <div>
      <Group gap={6}>
        <Text size="xs" c="dimmed" w={120}>{label}</Text>
        <Text size="xs">{total} <Text span c="dimmed">(baseline: {baseline})</Text></Text>
      </Group>
      {extras.length > 0 && (
        <List size="xs" spacing={2} mt={4} ml={120}>
          {extras.slice(0, 5).map((e) => <List.Item key={e}>{e}</List.Item>)}
          {extras.length > 5 && (
            <Text size="xs" c="dimmed" ml={20}>+ {extras.length - 5} more…</Text>
          )}
        </List>
      )}
    </div>
  );
}

// pickBaseline finds the most category-name-similar built-in pack.
// system-test is excluded (it's a diagnostic pack, not a real
// reference). Self-comparison is excluded too. Returns undefined if
// no candidates exist on this deployment.
function pickBaseline(draft: DomainPack, all: DomainPack[]): DomainPack | undefined {
  const candidates = all.filter((p) => p.is_published && p.slug !== draft.slug && p.slug !== 'system-test');
  if (candidates.length === 0) return undefined;
  let best: { pack: DomainPack; score: number } | undefined;
  const draftTokens = tokenSet(draft.categories.map((c) => c.name).join(' '));
  for (const c of candidates) {
    const candTokens = tokenSet(c.categories.map((cat) => cat.name).join(' '));
    const score = jaccard(draftTokens, candTokens);
    if (!best || score > best.score) best = { pack: c, score };
  }
  return best?.pack;
}

function tokenSet(s: string): Set<string> {
  return new Set(
    s.toLowerCase()
      .replace(/[^a-z0-9 ]+/g, ' ')
      .split(/\s+/)
      .filter((t) => t.length >= 3)
  );
}

function jaccard(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 && b.size === 0) return 0;
  let inter = 0;
  a.forEach((t) => { if (b.has(t)) inter++; });
  return inter / (a.size + b.size - inter);
}

function diffByName<T extends { name: string }>(draft: T[], baseline: T[]): string[] {
  const baseNames = new Set(baseline.map((b) => b.name.toLowerCase()));
  return draft.filter((d) => !baseNames.has(d.name.toLowerCase())).map((d) => d.name);
}

function diffProfileFields(draft: Record<string, unknown>, baseline: Record<string, unknown>): string[] {
  const draftProps = profileProps(draft);
  const baseProps = profileProps(baseline);
  const baseSet = new Set(baseProps);
  return draftProps.filter((p) => !baseSet.has(p));
}

function profileProps(schema: Record<string, unknown>): string[] {
  const props = (schema?.properties as Record<string, unknown>) || {};
  return Object.keys(props);
}

function countProfileFields(schema: Record<string, unknown>): number {
  return profileProps(schema).length;
}

