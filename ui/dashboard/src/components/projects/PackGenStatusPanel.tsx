'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Badge, Button, Card, Group, Loader, Stack, Text, Textarea, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconCircleDashed, IconWand } from '@tabler/icons-react';
import {
  api, DomainPack, Project, SchemaIndexStatus,
  PROJECT_STATE_PACK_GENERATION,
  PROJECT_STATE_PACK_GENERATION_DONE,
  PROJECT_STATE_PACK_GENERATION_PENDING,
  PROJECT_STATE_READY,
} from '@/lib/api';
import { SchemaIndexPanel } from '../SchemaIndexPanel';
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
  const [resetting, setResetting] = useState(false);
  // Schema-index status drives the "Step 1 / Step 2" progression in the
  // running-state UI. The agent builds the schema index first (which
  // can take many minutes on ERP-scale warehouses), then synthesises
  // the pack. We track the same per-project status the discovery page
  // does — by handing SchemaIndexPanel an onStatusChange callback.
  const [indexStatus, setIndexStatus] = useState<SchemaIndexStatus | null>(null);

  // Poll project state every 3s while the agent is generating. We push
  // the project up on any change to either `state` or
  // `pack_gen_last_error` — the orchestrator can record a failure
  // without flipping state (the failure surfaces in
  // pack_gen_last_error first; the state revert lands on the next
  // successful Mongo write). Done / failed transitions out of
  // pack_generation eventually unmount the poller.
  useEffect(() => {
    if (project.state !== PROJECT_STATE_PACK_GENERATION) return;
    const interval = setInterval(async () => {
      try {
        const next = await api.getProject(project.id);
        if (next.state !== project.state || next.pack_gen_last_error !== project.pack_gen_last_error) {
          onProjectChanged(next);
        }
      } catch { /* ignore poll errors */ }
    }, 3000);
    return () => clearInterval(interval);
  }, [project.id, project.state, project.pack_gen_last_error, onProjectChanged]);

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
    // Two-step progression. The agent runs schema indexing first (long
    // pole on ERP-scale warehouses — minutes to tens of minutes); pack
    // synthesis runs only after the index is ready.
    //   - schema_index_status indexing/pending_indexing → step 1 active
    //   - schema_index_status ready                     → step 1 done,
    //                                                     step 2 active
    //   - schema_index_status cancelled / failed        → recovery banner;
    //                                                     user has to flip
    //                                                     state back to
    //                                                     pack_generation_pending
    //                                                     to re-enter the
    //                                                     wizard.
    // Once the orchestrator finishes synth, project.state moves to
    // pack_generation_done and this branch unmounts entirely.
    const indexReady = indexStatus?.status === 'ready';
    const indexFailed = indexStatus?.status === 'failed';
    const indexCancelled = indexStatus?.status === 'cancelled';
    const indexHalted = indexFailed || indexCancelled;
    return (
      <Card withBorder p="lg">
        <Stack>
          <Group gap={8}>
            {indexHalted ? <IconAlertCircle size={18} color="var(--mantine-color-red-6)" />
              : <Loader size="xs" />}
            <Title order={5}>Generating pack</Title>
            <Badge color={indexFailed ? 'red' : indexCancelled ? 'orange' : 'blue'}>
              {indexFailed ? 'Indexing failed' : indexCancelled ? 'Cancelled' : indexReady ? 'Synthesising' : 'Indexing'}
            </Badge>
          </Group>

          {indexHalted ? (
            <Alert
              color={indexFailed ? 'red' : 'orange'}
              icon={<IconAlertCircle size={16} />}
              title={indexFailed ? 'Schema indexing failed' : 'Schema indexing was cancelled'}
            >
              <Text size="sm">
                Pack generation can&apos;t proceed without a fresh schema index. Click <b>Back to wizard</b> to adjust your blurb model / sources / warehouse and re-run, or use <b>Retry indexing</b> in the panel below to rebuild with the same configuration.
              </Text>
              <Group justify="flex-end" mt="sm">
                <Button
                  size="xs"
                  loading={resetting}
                  onClick={async () => {
                    setResetting(true);
                    try {
                      const next = await api.updateProject(project.id, { state: PROJECT_STATE_PACK_GENERATION_PENDING });
                      onProjectChanged(next);
                      router.push(`/projects/${project.id}/generate`);
                    } catch (e: unknown) {
                      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
                    } finally {
                      setResetting(false);
                    }
                  }}
                >
                  Back to wizard
                </Button>
              </Group>
            </Alert>
          ) : (
            <Text size="sm" c="dimmed">
              The agent reads your warehouse schema and knowledge sources, then synthesises the pack. This usually takes a few minutes; you can leave this page and come back — we&apos;ll update automatically when it&apos;s done.
            </Text>
          )}

          <Stack gap={6}>
            <Text size="sm" fw={500}>Step 1 of 2 — Indexing schema</Text>
            <SchemaIndexPanel
              projectId={project.id}
              title="Schema index"
              onStatusChange={setIndexStatus}
            />
          </Stack>

          <Stack gap={6}>
            <Group gap={6} align="center">
              {project.pack_gen_last_error
                ? <IconAlertCircle size={14} color="var(--mantine-color-red-6)" />
                : indexReady
                  ? <Loader size="xs" />
                  : <IconCircleDashed size={14} color="var(--mantine-color-gray-5)" />}
              <Text size="sm" fw={500}>Step 2 of 2 — Synthesising pack</Text>
              {project.pack_gen_last_error
                ? <Badge size="xs" color="red">Failed</Badge>
                : indexReady
                  ? <Badge size="xs" color="blue">Running</Badge>
                  : null}
            </Group>
            {project.pack_gen_last_error ? (
              <Alert color="red" icon={<IconAlertCircle size={16} />} title="Pack synthesis failed">
                <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{project.pack_gen_last_error}</Text>
                <Text size="xs" c="dimmed" mt={6}>
                  The agent retried 3 times before giving up. Common causes: HTTP timeout on a slow LLM, the model rejecting the prompt, or invalid pack JSON. <b>Back to wizard</b> takes you back to the configuration steps so you can adjust and retry.
                </Text>
                <Group justify="flex-end" mt="sm">
                  <Button
                    size="xs"
                    color="red"
                    loading={resetting}
                    onClick={async () => {
                      setResetting(true);
                      try {
                        const next = await api.updateProject(project.id, { state: PROJECT_STATE_PACK_GENERATION_PENDING });
                        onProjectChanged(next);
                        router.push(`/projects/${project.id}/generate`);
                      } catch (e: unknown) {
                        notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
                      } finally {
                        setResetting(false);
                      }
                    }}
                  >
                    Back to wizard
                  </Button>
                </Group>
              </Alert>
            ) : (
              <Text size="xs" c="dimmed">
                {indexReady
                  ? 'The agent is now feeding the indexed schema + your knowledge sources to the LLM. The retry loop validates the output up to 3 times before giving up.'
                  : indexHalted
                    ? 'Skipped — schema indexing did not finish.'
                    : 'Waits for schema indexing to finish.'}
              </Text>
            )}
          </Stack>
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
            <Title order={5}>Draft pack saved</Title>
            <Badge color="green">Saved — review and accept</Badge>
          </Group>
          <Button onClick={handleStartDiscovery} loading={starting}>Accept &amp; start discovery</Button>
        </Group>
        <Text size="sm" c="dimmed">
          The pack is saved and you can keep iterating. Each <b>Regenerate</b> below rewrites that section inline — no manual save needed. When you&apos;re happy, click <b>Accept &amp; start discovery</b> to lock the pack in and unlock the discovery agent.
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
