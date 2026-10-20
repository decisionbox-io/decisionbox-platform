'use client';

import Link from 'next/link';
import { ActionIcon, Badge, Card, Group, Progress, Stack, Text, Tooltip } from '@mantine/core';
import {
  IconBulb, IconExternalLink, IconFlag3, IconGitPullRequest, IconRoute,
  IconTargetArrow,
} from '@tabler/icons-react';
import { GraphRecommendation, GraphTask, LedgerGraph } from '@/lib/api';
import {
  GraphIndex, RunSighting, RunStory, buildStories, humanise,
  recommendationLink, sightingEvidenceLink,
} from './graph';

// Level 1 — Story.
//
// Plain language, no graph, no jargon: a vertical timeline, newest run first,
// one card per run, where every noun is a link. For most readers this alone
// answers "where did this come from", which is the question the flat page could
// not answer at all.
//
// The momentum bar each card carries is the convergence ratio the page used to
// show as a separate chart — folded in here as the timeline's spine, so the
// number sits next to the run it describes instead of in a list of its own.

const MAX_NAMED = 3;

function severityColor(severity?: string): string {
  switch (severity) {
    case 'critical': return 'red';
    case 'high': return 'orange';
    case 'medium': return 'yellow';
    default: return 'gray';
  }
}

// A finding's name and a thread's text are full sentences — often long ones
// written by the model. A Badge upper-cases its content and is sized for a
// word, so these are rendered as clamped text with a small severity dot. The
// full text is a tooltip away.

/** A name that leads somewhere, with the affordance to open it in a new tab.
 *  A tooltip alone could show the text and not get you to the page. */
function LinkedName({
  href, name, icon, color,
}: {
  href: string | null; name: string; icon: React.ReactNode; color?: string;
}) {
  const body = (
    <Group gap={5} wrap="nowrap" style={{ maxWidth: 420, minWidth: 0 }}>
      <span style={{ display: 'flex', flexShrink: 0, color: color ?? 'var(--mantine-color-dimmed)' }}>
        {icon}
      </span>
      <Text size="xs" lineClamp={1} td={href ? 'underline' : undefined}>{name}</Text>
    </Group>
  );
  return (
    <Group gap={3} wrap="nowrap" style={{ minWidth: 0 }}>
      <Tooltip label={name} multiline w={360} openDelay={400}>
        {href ? <Link href={href} style={{ textDecoration: 'none', minWidth: 0 }}>{body}</Link> : body}
      </Tooltip>
      {href && (
        <Tooltip label="Open in a new tab" openDelay={400}>
          <ActionIcon
            component="a" href={href} target="_blank" rel="noopener noreferrer"
            size="xs" variant="subtle" color="gray"
            aria-label={`Open ${name} in a new tab`}
          >
            <IconExternalLink size={12} />
          </ActionIcon>
        </Tooltip>
      )}
    </Group>
  );
}

function SeverityDot({ severity }: { severity?: string }) {
  return (
    <div style={{
      width: 7, height: 7, borderRadius: '50%', flexShrink: 0,
      background: `var(--mantine-color-${severityColor(severity)}-6)`,
    }} />
  );
}

/** A finding as this run saw it, linked to the SQL that run wrote.
 *
 *  Severity and the link both come from the run's own sighting where it has
 *  one: a finding carries its CURRENT state, so reading either off the finding
 *  would make an old run report today's. */
function FindingChip({ projectId, seen, index }: { projectId: string; seen: RunSighting; index: GraphIndex }) {
  const { finding } = seen;
  return (
    <LinkedName
      href={sightingEvidenceLink(projectId, seen, index)}
      name={finding.name || finding.id}
      color={`var(--mantine-color-${severityColor(seen.severity)}-6)`}
      icon={<SeverityDot severity={seen.severity} />}
    />
  );
}

function RecommendationChip({ projectId, rec }: { projectId: string; rec: GraphRecommendation }) {
  return (
    <LinkedName
      href={recommendationLink(projectId, rec)}
      name={rec.title || rec.id}
      color="var(--mantine-color-indigo-6)"
      icon={<IconTargetArrow size={12} />}
    />
  );
}

function TaskChip({ task }: { task: GraphTask }) {
  // Prefer the short plain-language title; the detailed text is the fallback
  // for threads opened before titles existed, and it can be a paragraph.
  const label = task.title || task.text || task.id;
  return (
    <Tooltip label={task.text || label} multiline w={360} openDelay={400}>
      <Group gap={5} wrap="nowrap" style={{ maxWidth: 420, minWidth: 0 }}>
        <span style={{ display: 'flex', flexShrink: 0, color: 'var(--mantine-color-grape-6)' }}>
          {task.kind === 'hypothesis' ? <IconBulb size={12} /> : <IconRoute size={12} />}
        </span>
        <Text size="xs" lineClamp={1}>{label}</Text>
      </Group>
    </Tooltip>
  );
}

/** Names the first few items and says how many more there are, rather than
 *  rendering a wall of chips on a run that found forty things. */
function ChipRow({
  icon, label, children, extra,
}: {
  icon: React.ReactNode; label: string; children: React.ReactNode; extra: number;
}) {
  return (
    <Group gap={8} wrap="nowrap" align="flex-start">
      <Group gap={4} wrap="nowrap" style={{ width: 104, flexShrink: 0 }}>
        {icon}
        <Text size="xs" c="dimmed">{label}</Text>
      </Group>
      <Stack gap={2} style={{ minWidth: 0, flex: 1 }}>
        {children}
        {extra > 0 && <Text size="xs" c="dimmed">+{extra} more</Text>}
      </Stack>
    </Group>
  );
}

function sentence(story: RunStory): string {
  const parts: string[] = [];
  if (story.newFindings.length > 0) {
    const high = story.highCount > 0 ? ` (${story.highCount} high or critical)` : '';
    parts.push(`found ${story.newFindings.length} new finding${story.newFindings.length === 1 ? '' : 's'}${high}`);
  }
  if (story.seenAgain.length > 0) {
    parts.push(`saw ${story.seenAgain.length} again`);
  }
  if (story.closedTasks.length > 0) {
    parts.push(`closed ${story.closedTasks.length} thread${story.closedTasks.length === 1 ? '' : 's'}`);
  }
  if (story.openedTasks.length > 0) {
    parts.push(`opened ${story.openedTasks.length}`);
  }
  if (story.proposals.length > 0) {
    parts.push(`proposed ${story.proposals.length} playbook change${story.proposals.length === 1 ? '' : 's'}`);
  }
  if (parts.length === 0) return 'Recorded no new findings or threads.';
  const text = parts.join(', ');
  return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`;
}

export default function StoryTimeline({
  projectId, graph, index,
}: {
  projectId: string; graph: LedgerGraph; index: GraphIndex;
}) {
  const stories = buildStories(index);

  if (stories.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        No runs have reflected yet. Each completed discovery run adds a chapter here.
      </Text>
    );
  }

  return (
    <Stack gap={0}>
      {stories.map((story, i) => {
        const closed = story.closedTasks;
        const dateLabel = story.run.date
          ? new Date(story.run.date).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
          : 'date unknown';
        return (
          <Group key={story.run.node_id} gap="md" wrap="nowrap" align="stretch">
            {/* The spine: the momentum bar, next to the run it describes. */}
            <Stack gap={0} align="center" style={{ width: 28, flexShrink: 0 }}>
              <div style={{
                width: 10, height: 10, borderRadius: '50%',
                background: 'var(--mantine-color-blue-6)', marginTop: 20,
              }} />
              {i < stories.length - 1 && (
                <div style={{ width: 2, flex: 1, background: 'var(--db-border-default)', minHeight: 24 }} />
              )}
            </Stack>

            <Card withBorder radius="md" padding="md" mb="md" style={{ flex: 1, minWidth: 0 }}>
              <Group justify="space-between" wrap="nowrap" align="center">
                <Group gap="xs" wrap="nowrap">
                  <Text fw={600} size="sm">
                    {story.ordinal > 0 ? `Run ${story.ordinal}` : 'A run'}
                  </Text>
                  <Text size="xs" c="dimmed">· {dateLabel}</Text>
                  {story.run.run_type && (
                    <Badge size="xs" variant="light" color="gray" tt="none">
                      {humanise(story.run.run_type)}
                    </Badge>
                  )}
                </Group>
                <Tooltip
                  label={`${story.run.new_findings} of ${story.run.total_findings} findings were new in this run`}
                  openDelay={200}
                >
                  <Group gap={6} wrap="nowrap" style={{ width: 150, flexShrink: 0 }}>
                    <Progress
                      value={Math.round((story.run.marginal_ratio ?? 0) * 100)}
                      size="sm" radius="sm" style={{ flex: 1 }}
                    />
                    <Text size="xs" c="dimmed" style={{ flexShrink: 0 }}>
                      {Math.round((story.run.marginal_ratio ?? 0) * 100)}% new
                    </Text>
                  </Group>
                </Tooltip>
              </Group>

              <Text size="sm" mt={6}>{sentence(story)}</Text>

              <Stack gap={6} mt={10}>
                {story.newFindings.length > 0 && (
                  <ChipRow
                    icon={<IconFlag3 size={13} />}
                    label="Found"
                    extra={Math.max(0, story.newFindings.length - MAX_NAMED)}
                  >
                    {story.newFindings.slice(0, MAX_NAMED).map((seen) => (
                      <FindingChip key={seen.finding.node_id} projectId={projectId} seen={seen} index={index} />
                    ))}
                  </ChipRow>
                )}
                {closed.length > 0 && (
                  <ChipRow
                    icon={<IconRoute size={13} />}
                    label="Closed"
                    extra={Math.max(0, closed.length - MAX_NAMED)}
                  >
                    {closed.slice(0, MAX_NAMED).map((t) => <TaskChip key={t.node_id} task={t} />)}
                  </ChipRow>
                )}
                {story.openedTasks.length > 0 && (
                  <ChipRow
                    icon={<IconRoute size={13} />}
                    label="Opened"
                    extra={Math.max(0, story.openedTasks.length - MAX_NAMED)}
                  >
                    {story.openedTasks.slice(0, MAX_NAMED).map((t) => <TaskChip key={t.node_id} task={t} />)}
                  </ChipRow>
                )}
                {story.recommendations.length > 0 && (
                  <ChipRow
                    icon={<IconTargetArrow size={13} />}
                    label="Recommended"
                    extra={Math.max(0, story.recommendations.length - MAX_NAMED)}
                  >
                    {story.recommendations.slice(0, MAX_NAMED).map((rec) => (
                      <RecommendationChip key={rec.node_id} projectId={projectId} rec={rec} />
                    ))}
                  </ChipRow>
                )}
                {story.proposals.length > 0 && (
                  <ChipRow
                    icon={<IconGitPullRequest size={13} />}
                    label="Proposed"
                    extra={Math.max(0, story.proposals.length - MAX_NAMED)}
                  >
                    {story.proposals.slice(0, MAX_NAMED).map((p) => (
                      <Badge key={p.node_id} size="sm" variant="light" color="yellow" tt="none">
                        {humanise(p.area_name || p.area_id)}
                      </Badge>
                    ))}
                  </ChipRow>
                )}
              </Stack>

              {/* Why each closed thread closed — the single most useful line on
                  the card, and the one nothing in the old page could show. */}
              {closed.some((t) => t.close_reason) && (
                <Stack gap={2} mt={10}>
                  {closed.filter((t) => t.close_reason).slice(0, MAX_NAMED).map((t) => (
                    <Text key={t.node_id} size="xs" c="dimmed" fs="italic" lineClamp={2}>
                      “{t.close_reason}” — {t.title || t.text}
                    </Text>
                  ))}
                </Stack>
              )}

              {story.learnings.length > 0 && (
                <Stack gap={2} mt={10}>
                  {story.learnings.slice(0, MAX_NAMED).map((l) => (
                    <Group key={l.node_id} gap={6} wrap="nowrap" align="flex-start">
                      <IconBulb size={13} style={{ marginTop: 2, flexShrink: 0 }} />
                      <Text size="xs" c="dimmed">
                        {l.category ? `${humanise(l.category)}: ` : ''}{l.note}
                      </Text>
                    </Group>
                  ))}
                </Stack>
              )}
            </Card>
          </Group>
        );
      })}
      {graph.truncated?.runs && (
        <Text size="xs" c="dimmed" mt={4}>
          Older runs are not loaded. This project has more history than one page shows.
        </Text>
      )}
    </Stack>
  );
}
