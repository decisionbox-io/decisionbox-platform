'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Badge, Button, Card, Group, Loader, Stack, Text, Textarea, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconWand } from '@tabler/icons-react';
import {
  api, DomainPack, Project,
  PROJECT_STATE_PACK_GENERATION,
  PROJECT_STATE_PACK_GENERATION_DONE,
  PROJECT_STATE_PACK_GENERATION_PENDING,
  PROJECT_STATE_READY,
} from '@/lib/api';
import DraftDiffSummary from './DraftDiffSummary';

export interface PackGenStatusPanelProps {
  project: Project;
  onProjectChanged: (next: Project) => void;
}

// PackGenStatusPanel renders the lifecycle UI for a project that is in
// any pack_generation_* state. It owns its own polling loop while the
// agent is generating, and surfaces a draft-pack preview + regenerate-
// section affordance once the agent finishes. The parent project page
// hides its discovery UI while this panel is visible.
export default function PackGenStatusPanel({ project, onProjectChanged }: PackGenStatusPanelProps) {
  const router = useRouter();
  const [pack, setPack] = useState<DomainPack | null>(null);
  const [packError, setPackError] = useState<string | null>(null);
  const [feedback, setFeedback] = useState<Record<string, string>>({});
  const [lastFeedback, setLastFeedback] = useState<Record<string, string>>({});
  const [regenerating, setRegenerating] = useState<string | null>(null);
  const [starting, setStarting] = useState(false);

  // Poll project state every 3s while the agent is generating; stop as
  // soon as state moves out of pack_generation. The done state hands
  // off to the static draft-preview branch and unmounts the poller.
  useEffect(() => {
    if (project.state !== PROJECT_STATE_PACK_GENERATION) return;
    const interval = setInterval(async () => {
      try {
        const next = await api.getProject(project.id);
        if (next.state !== project.state) onProjectChanged(next);
      } catch { /* ignore poll errors */ }
    }, 3000);
    return () => clearInterval(interval);
  }, [project.id, project.state, onProjectChanged]);

  // Load the draft pack when generation finishes so we can show the
  // user what the agent produced before they commit to discovery.
  useEffect(() => {
    if (project.state !== PROJECT_STATE_PACK_GENERATION_DONE) return;
    const slug = project.domain || project.generate_pack?.pack_slug;
    if (!slug) return;
    api.getDomainPack(slug)
      .then(setPack)
      .catch((e) => setPackError((e as Error).message));
  }, [project.state, project.domain, project.generate_pack?.pack_slug]);

  const handleRegenerate = async (section: string) => {
    const fb = (feedback[section] || '').trim();
    if (!fb) {
      notifications.show({ title: 'Add feedback', message: 'Tell the agent what to change.', color: 'orange' });
      return;
    }
    setRegenerating(section);
    try {
      await api.packRegenerateSection(project.id, { section, feedback: fb });
      const slug = project.domain || project.generate_pack?.pack_slug;
      if (slug) {
        const next = await api.getDomainPack(slug);
        setPack(next);
      }
      setLastFeedback((prev) => ({ ...prev, [section]: fb }));
      setFeedback((prev) => ({ ...prev, [section]: '' }));
      notifications.show({ title: 'Section regenerated', message: section, color: 'green' });
    } catch (e: unknown) {
      // Failed regenerate keeps the user's feedback in the textarea so
      // they can adjust and retry without retyping.
      notifications.show({ title: 'Could not regenerate', message: (e as Error).message, color: 'red' });
    } finally {
      setRegenerating(null);
    }
  };

  const handleStartDiscovery = async () => {
    setStarting(true);
    try {
      const next = await api.updateProject(project.id, { state: PROJECT_STATE_READY });
      onProjectChanged(next);
      notifications.show({ title: 'Pack accepted', message: 'You can run discovery now.', color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setStarting(false);
    }
  };

  if (project.state === PROJECT_STATE_PACK_GENERATION_PENDING) {
    return (
      <Card withBorder p="lg">
        <Stack>
          <Group gap={8}>
            <IconWand size={18} />
            <Title order={5}>Pack generation — draft</Title>
            <Badge color={project.pack_gen_last_error ? 'red' : 'gray'}>
              {project.pack_gen_last_error ? 'Last attempt failed' : 'Pending'}
            </Badge>
          </Group>
          {project.pack_gen_last_error && (
            <Alert color="red" icon={<IconAlertCircle size={16} />} title="Pack generation failed">
              <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{project.pack_gen_last_error}</Text>
              <Text size="xs" c="dimmed" mt={6}>
                Adjust your knowledge sources or pack description in the wizard, then retry.
              </Text>
            </Alert>
          )}
          <Text size="sm" c="dimmed">
            Pick up where you left off in the wizard: upload knowledge sources, connect your warehouse, then launch the agent.
          </Text>
          <Group justify="flex-end">
            <Button onClick={() => router.push(`/projects/${project.id}/generate`)}>
              {project.pack_gen_last_error ? 'Retry in wizard' : 'Continue setup'}
            </Button>
          </Group>
        </Stack>
      </Card>
    );
  }

  if (project.state === PROJECT_STATE_PACK_GENERATION) {
    return (
      <Card withBorder p="lg">
        <Stack>
          <Group gap={8}>
            <Loader size="xs" />
            <Title order={5}>Generating pack</Title>
            <Badge color="blue">Running</Badge>
          </Group>
          <Text size="sm" c="dimmed">
            The agent is reading your knowledge sources and warehouse schema. This usually takes a few minutes — you can leave this page and come back. We&apos;ll update automatically when it&apos;s done.
          </Text>
        </Stack>
      </Card>
    );
  }

  // PROJECT_STATE_PACK_GENERATION_DONE
  return (
    <Card withBorder p="lg">
      <Stack>
        <Group gap={8} justify="space-between">
          <Group gap={8}>
            <IconWand size={18} />
            <Title order={5}>Draft pack ready</Title>
            <Badge color="green">Awaiting review</Badge>
          </Group>
          <Button onClick={handleStartDiscovery} loading={starting}>Start discovery</Button>
        </Group>
        <Text size="sm" c="dimmed">
          Review what the agent generated. Use the feedback boxes to regenerate any section that needs adjustment, then click <b>Start discovery</b>.
        </Text>

        {packError && (
          <Alert color="red" icon={<IconAlertCircle size={16} />}>{packError}</Alert>
        )}

        {!pack && !packError && (
          <Group gap={8}><Loader size="xs" /><Text size="sm" c="dimmed">Loading draft…</Text></Group>
        )}

        {pack && (
          <Stack gap="md">
            <DraftDiffSummary draft={pack} />
            <DraftSection
              title="Pack metadata"
              section="metadata"
              value={`${pack.name} (${pack.slug}) — ${pack.description || 'no description'}`}
              feedback={feedback['metadata'] || ''}
              lastFeedback={lastFeedback['metadata']}
              hint='e.g. "softer tone, mention growth-stage SaaS"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, metadata: v }))}
              onRegenerate={() => handleRegenerate('metadata')}
              busy={regenerating === 'metadata'}
            />
            <DraftSection
              title={`Categories (${pack.categories.length})`}
              section="categories"
              value={pack.categories.map((c) => `• ${c.name} — ${c.description}`).join('\n')}
              feedback={feedback['categories'] || ''}
              lastFeedback={lastFeedback['categories']}
              hint='e.g. "split power-users into a separate category"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, categories: v }))}
              onRegenerate={() => handleRegenerate('categories')}
              busy={regenerating === 'categories'}
            />
            <DraftSection
              title={`Analysis areas (${pack.analysis_areas.base.length})`}
              section="analysis_areas"
              value={pack.analysis_areas.base.map((a) => `[P${a.priority}] ${a.name} — ${a.description}`).join('\n')}
              feedback={feedback['analysis_areas'] || ''}
              lastFeedback={lastFeedback['analysis_areas']}
              hint='e.g. "drop the churn area, add weekly engagement"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, analysis_areas: v }))}
              onRegenerate={() => handleRegenerate('analysis_areas')}
              busy={regenerating === 'analysis_areas'}
            />
            <DraftSection
              title="Profile schema"
              section="profile_schema"
              value={JSON.stringify(pack.profile_schema.base, null, 2)}
              feedback={feedback['profile_schema'] || ''}
              lastFeedback={lastFeedback['profile_schema']}
              hint='e.g. "add a tier_count field"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, profile_schema: v }))}
              onRegenerate={() => handleRegenerate('profile_schema')}
              busy={regenerating === 'profile_schema'}
              monospace
            />
            <DraftSection
              title="Base context prompt"
              section="base_context"
              value={pack.prompts.base.base_context}
              feedback={feedback['base_context'] || ''}
              lastFeedback={lastFeedback['base_context']}
              hint='e.g. "emphasize that the data is event-level, not aggregated"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, base_context: v }))}
              onRegenerate={() => handleRegenerate('base_context')}
              busy={regenerating === 'base_context'}
              monospace
            />
            <DraftSection
              title="Exploration prompt"
              section="exploration"
              value={pack.prompts.base.exploration}
              feedback={feedback['exploration'] || ''}
              lastFeedback={lastFeedback['exploration']}
              hint='e.g. "encourage cohort comparisons over week-over-week"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, exploration: v }))}
              onRegenerate={() => handleRegenerate('exploration')}
              busy={regenerating === 'exploration'}
              monospace
            />
            <DraftSection
              title="Recommendations prompt"
              section="recommendations"
              value={pack.prompts.base.recommendations}
              feedback={feedback['recommendations'] || ''}
              lastFeedback={lastFeedback['recommendations']}
              hint='e.g. "make recommendations more actionable for marketing"'
              onFeedback={(v) => setFeedback((p) => ({ ...p, recommendations: v }))}
              onRegenerate={() => handleRegenerate('recommendations')}
              busy={regenerating === 'recommendations'}
              monospace
            />
          </Stack>
        )}
      </Stack>
    </Card>
  );
}

function DraftSection({
  title, value, feedback, lastFeedback, hint, onFeedback, onRegenerate, busy, monospace,
}: {
  title: string;
  section: string;
  value: string;
  feedback: string;
  lastFeedback?: string;
  hint?: string;
  onFeedback: (v: string) => void;
  onRegenerate: () => void;
  busy: boolean;
  monospace?: boolean;
}) {
  return (
    <div style={{ borderTop: '1px solid var(--db-border-default)', paddingTop: 12 }}>
      <Group justify="space-between" mb={6}>
        <Group gap={6}>
          <Text size="sm" fw={500}>{title}</Text>
          {lastFeedback && (
            <Badge size="xs" variant="light" color="gray" title={`Previous feedback: ${lastFeedback}`}>
              regenerated
            </Badge>
          )}
        </Group>
      </Group>
      <pre style={{
        background: 'var(--db-bg-muted)',
        padding: 8,
        borderRadius: 'var(--db-radius)',
        fontFamily: monospace ? 'monospace' : 'inherit',
        fontSize: 12,
        whiteSpace: 'pre-wrap',
        margin: 0,
        maxHeight: 240,
        overflow: 'auto',
      }}>{value}</pre>
      <Group align="flex-start" gap="xs" mt={8}>
        <Textarea
          placeholder={hint || 'Feedback for this section'}
          value={feedback}
          onChange={(e) => onFeedback(e.target.value)}
          autosize minRows={1} maxRows={4}
          style={{ flex: 1 }}
          size="xs"
        />
        <Button size="xs" variant="default" onClick={onRegenerate} loading={busy}>Regenerate</Button>
      </Group>
    </div>
  );
}
