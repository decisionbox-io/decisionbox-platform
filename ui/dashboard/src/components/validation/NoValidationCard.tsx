'use client';

import { Button, Card, Group, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';

// Empty-state card shown when a document has no validation verdict
// yet. Two surfaces:
//   - validationEnabled=true  → primary "Run validation" button.
//   - validationEnabled=false → grey "Validation is disabled" card
//     with a Settings → Advanced link.
//
// The router decides which to render — this component is dumb. The
// `running` flag disables the button while a parent-owned enqueue
// is in flight so a double-click can't race through the API's
// pre-check (the partial-unique-on-active Mongo index is the
// durable defence; this is just UX polish).

export function NoValidationCard({
  validationEnabled,
  onRun,
  running,
  settingsHref,
}: {
  validationEnabled: boolean;
  onRun?: () => void;
  running?: boolean;
  settingsHref?: string;
}) {
  if (!validationEnabled) {
    return (
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
              Validation
            </Text>
          </Group>
        </Group>
        <Text size="xs" c="dimmed" mb={settingsHref ? 8 : 0}>
          Validation is disabled for this project.
        </Text>
        {settingsHref && (
          <Button
            component="a"
            href={settingsHref}
            variant="subtle"
            size="xs"
            px={0}
          >
            Settings → Advanced
          </Button>
        )}
      </Card>
    );
  }
  return (
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
            Validation
          </Text>
        </Group>
      </Group>
      <Text size="xs" c="dimmed" mb={8}>
        No validation has been run for this item yet. Click below to
        verify the headline and per-claim figures against the
        warehouse.
      </Text>
      <Button
        size="xs"
        variant="filled"
        onClick={onRun}
        loading={running}
        disabled={running}
      >
        Run validation
      </Button>
    </Card>
  );
}
