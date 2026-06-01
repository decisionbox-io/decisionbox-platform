'use client';

import { Badge, Card, Group, SimpleGrid, Stack, Text } from '@mantine/core';
import { IconServer, IconCpu } from '@tabler/icons-react';
import type { SystemComponent } from '@/lib/api';

/**
 * withDashboardSelf prepends the running dashboard's own build as a
 * component — the one component the API cannot know, since the dashboard
 * is a separate image. If the server inventory already reports a
 * "Dashboard" (e.g. a deployment wired a heartbeat source), the
 * server's entry wins and self is dropped, so we never show it twice.
 */
export function withDashboardSelf(
  server: SystemComponent[],
  self: SystemComponent,
): SystemComponent[] {
  const hasDashboard = server.some((c) => c.name.toLowerCase() === self.name.toLowerCase());
  return hasDashboard ? server : [self, ...server];
}

export interface ComponentGroup {
  kind: string;
  label: string;
  items: SystemComponent[];
}

const KIND_ORDER: Record<string, number> = { service: 0, worker: 1 };
const KIND_LABEL: Record<string, string> = { service: 'Services', worker: 'Workers' };

function kindLabel(kind: string): string {
  return KIND_LABEL[kind] ?? `${kind.charAt(0).toUpperCase()}${kind.slice(1)}`;
}

/**
 * groupByKind buckets components by their `kind`, ordering known kinds
 * (services, then workers) first and any other kind after, alphabetically.
 * Items within a group are sorted by name. Pure — drives both render and tests.
 */
export function groupByKind(components: SystemComponent[]): ComponentGroup[] {
  const buckets = new Map<string, SystemComponent[]>();
  for (const c of components) {
    const list = buckets.get(c.kind) ?? [];
    list.push(c);
    buckets.set(c.kind, list);
  }
  return [...buckets.entries()]
    .sort(([a], [b]) => {
      const oa = KIND_ORDER[a] ?? 99;
      const ob = KIND_ORDER[b] ?? 99;
      return oa !== ob ? oa - ob : a.localeCompare(b);
    })
    .map(([kind, items]) => ({
      kind,
      label: kindLabel(kind),
      items: [...items].sort((a, b) => a.name.localeCompare(b.name)),
    }));
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <Group gap={6} wrap="nowrap" align="baseline">
      <Text size="xs" c="dimmed" style={{ minWidth: 72, flexShrink: 0 }}>{label}</Text>
      <Text size="xs" style={{ fontFamily: 'var(--mantine-font-family-monospace)', wordBreak: 'break-all' }}>
        {value}
      </Text>
    </Group>
  );
}

function ComponentCard({ component }: { component: SystemComponent }) {
  const isWorker = component.kind === 'worker';
  return (
    <Card withBorder radius="md" padding="md">
      <Stack gap="xs">
        <Group justify="space-between" wrap="nowrap">
          <Group gap={8} wrap="nowrap">
            {isWorker ? <IconCpu size={18} /> : <IconServer size={18} />}
            <Text fw={600}>{component.name}</Text>
          </Group>
          <Badge variant="light" color={isWorker ? 'grape' : 'blue'}>{component.kind}</Badge>
        </Group>

        <Stack gap={2}>
          <Field label="Version" value={component.version || 'unknown'} />
          {component.commit && <Field label="Commit" value={component.commit} />}
          {component.build_date && <Field label="Built" value={component.build_date} />}
          {component.runs_in && <Field label="Runs in" value={component.runs_in} />}
        </Stack>

        {component.note && (
          <Text size="xs" c="dimmed" fs="italic">{component.note}</Text>
        )}
      </Stack>
    </Card>
  );
}

/**
 * SystemInventory renders the component inventory grouped by kind. It
 * renders whatever it is given — no component list is hardcoded here.
 */
export function SystemInventory({ components }: { components: SystemComponent[] }) {
  if (components.length === 0) {
    return <Text c="dimmed">No components reported.</Text>;
  }
  return (
    <Stack gap="xl">
      {groupByKind(components).map((group) => (
        <Stack key={group.kind} gap="sm">
          <Text size="sm" fw={600} tt="uppercase" c="dimmed" style={{ letterSpacing: '0.6px' }}>
            {group.label}
          </Text>
          <SimpleGrid cols={{ base: 1, sm: 2 }} spacing="md">
            {group.items.map((c) => (
              <ComponentCard key={`${c.kind}:${c.name}`} component={c} />
            ))}
          </SimpleGrid>
        </Stack>
      ))}
    </Stack>
  );
}
