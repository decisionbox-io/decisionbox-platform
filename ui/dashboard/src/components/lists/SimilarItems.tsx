'use client';

import Link from 'next/link';
import { Badge, Card, Group, Stack, Text, Title } from '@mantine/core';
import { SearchResultItem } from '@/lib/api';

interface Props {
  label: string;
  items: SearchResultItem[];
  // Detail URL factory. Callers pass a builder so we don't re-encode the
  // insight-vs-recommendation path convention here.
  hrefFor: (item: SearchResultItem) => string;
}

const severityColors: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray',
};

// SimilarItems renders semantic-search matches as full-width rich cards
// below the main content. Designed for exploration — each card shows enough
// context (name, description snippet, severity, area, affected count where
// available) that the user can decide whether to click through without
// having to navigate first. Complements the compact right-sidebar that lists
// explicitly-related items. Related items answer "jump to the exact item I
// need"; similar items answer "what else looks like this?"
export default function SimilarItems({ label, items, hrefFor }: Props) {
  if (items.length === 0) return null;

  return (
    <div style={{ marginTop: 24 }}>
      <Title order={4} mb="sm">{label}</Title>
      <Stack gap="sm">
        {items.map(sim => {
          const isDuplicate = sim.score > 0.95;
          return (
            <Link
              key={sim.id}
              href={hrefFor(sim)}
              style={{ textDecoration: 'none', color: 'inherit' }}
            >
              <Card
                withBorder
                padding="md"
                style={{
                  cursor: 'pointer',
                  transition: 'border-color 120ms ease, transform 120ms ease',
                }}
                onMouseEnter={e => {
                  e.currentTarget.style.borderColor = 'var(--db-border-strong)';
                }}
                onMouseLeave={e => {
                  e.currentTarget.style.borderColor = 'var(--db-border-default)';
                }}
              >
                <Group justify="space-between" wrap="nowrap" align="flex-start" gap="md">
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <Text size="sm" fw={600} mb={4}>
                      {sim.name || sim.title}
                    </Text>
                    {sim.description && (
                      <Text size="xs" c="dimmed" lineClamp={2} mb={6}>
                        {sim.description}
                      </Text>
                    )}
                    <Group gap={6} wrap="wrap">
                      {sim.severity && (
                        <Badge size="xs" variant="light" color={severityColors[sim.severity] || 'gray'}>
                          {sim.severity}
                        </Badge>
                      )}
                      {sim.analysis_area && (
                        <Badge size="xs" variant="outline">{sim.analysis_area}</Badge>
                      )}
                      {sim.discovered_at && (
                        <Text size="xs" c="dimmed">
                          {new Date(sim.discovered_at).toLocaleDateString()}
                        </Text>
                      )}
                    </Group>
                  </div>
                  <Badge
                    size="sm"
                    variant="light"
                    color={isDuplicate ? 'orange' : 'blue'}
                    style={{ flexShrink: 0 }}
                  >
                    {Math.round(sim.score * 100)}% {isDuplicate ? 'duplicate' : 'match'}
                  </Badge>
                </Group>
              </Card>
            </Link>
          );
        })}
      </Stack>
    </div>
  );
}
