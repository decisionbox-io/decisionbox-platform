'use client';

import { ComponentType, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, Modal, Stack, Stepper, Text, Title,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconWand, IconUpload } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import WarehouseConfigPanel from '@/components/projects/WarehouseConfigPanel';
import ProvidersPanel from '@/components/projects/ProvidersPanel';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import { SchemaIndexPanel } from '@/components/SchemaIndexPanel';
import {
  api, Project, ProviderMeta, SchemaIndexStatus,
  PROJECT_STATE_PACK_GENERATION_PENDING,
  PROJECT_STATE_PACK_GENERATION,
  PROJECT_STATE_PACK_GENERATION_DONE,
} from '@/lib/api';

// KnowledgeSourcesPanel ships in the enterprise overlay only. We probe
// for it at runtime — when present (enterprise build) we render the
// real panel inside step 1; when absent (community build) we fall back
// to a placeholder alert so the wizard still loads.
type KnowledgeSourcesPanelProps = {
  projectId: string;
  variant: 'page' | 'wizard';
  intro?: string;
  onReadyChange?: (ready: boolean) => void;
};

function useKnowledgeSourcesPanel(): ComponentType<KnowledgeSourcesPanelProps> | null {
  const [Comp, setComp] = useState<ComponentType<KnowledgeSourcesPanelProps> | null>(null);
  useEffect(() => {
    let cancelled = false;
    import('@/components/projects/KnowledgeSourcesPanel')
      .then((mod) => {
        if (!cancelled && mod?.default) setComp(() => mod.default as ComponentType<KnowledgeSourcesPanelProps>);
      })
      .catch(() => { /* community build — panel not present */ });
    return () => { cancelled = true; };
  }, []);
  return Comp;
}

// PackGenWizardPage hosts the three-step pack-generation wizard for
// projects in pack_generation_pending state. Existing per-project pages
// for sources / warehouse / providers are reused via panel components,
// so this route only owns the stepper chrome and the launch button.
export default function PackGenWizardPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const [project, setProject] = useState<Project | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [active, setActive] = useState(0);
  const [launching, setLaunching] = useState(false);
  const [discarding, setDiscarding] = useState(false);
  const [discardOpened, { open: openDiscard, close: closeDiscard }] = useDisclosure(false);
  const KnowledgeSourcesPanel = useKnowledgeSourcesPanel();

  // Blurb-model state — optional. Defaults to "use analysis LLM"; when
  // the user enables it they pick a separate (typically cheaper +
  // faster) model that the schema indexer uses to generate per-table
  // descriptions. Without this step users were silently running blurb
  // generation on their analysis LLM, which on ERP-scale warehouses
  // (1k+ tables) burns serious tokens.
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  const [savingBlurb, setSavingBlurb] = useState(false);
  // Schema-index status drives the "can we generate yet?" gate on the
  // Generate step. The orchestrator hard-rejects pack-gen when this
  // isn't "ready" (an empty schema slice would otherwise produce a
  // pack with hallucinated table names) so the wizard mirrors that
  // gate visually instead of letting the user click into an error.
  const [indexStatus, setIndexStatus] = useState<SchemaIndexStatus | null>(null);
  const [kickingIndex, setKickingIndex] = useState(false);

  useEffect(() => {
    Promise.all([api.getProject(id), api.listLLMProviders()])
      .then(([p, llmProvs]) => {
        setProject(p);
        setLlmProviders(llmProvs);
        // Hydrate the blurb editor from any persisted blurb_llm so
        // returning to the wizard mid-setup picks up the prior choice.
        if (p.blurb_llm?.provider && p.blurb_llm?.model) {
          setBlurb({
            enabled: true,
            provider: p.blurb_llm.provider,
            model: p.blurb_llm.model,
            config: p.blurb_llm.config || {},
            apiKey: '',
          });
        }
        // Send the user back to the project page if there is no draft
        // pack-gen flow active. The project page renders its own state-
        // specific banners (running, draft preview, ready) so we don't
        // duplicate them here.
        if (p.state !== PROJECT_STATE_PACK_GENERATION_PENDING) {
          router.replace(`/projects/${id}`);
        }
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [id, router]);

  // refresh re-fetches the project doc + applies it to local state.
  // Called after async writes (pack-generate failure, schema-index
  // status changes, etc.) so the wizard reflects the latest server
  // state without a full page reload.
  const refresh = async () => {
    try {
      const p = await api.getProject(id);
      setProject(p);
    } catch { /* ignore */ }
  };

  const warehouseReady = Boolean(project?.warehouse?.provider && (project.warehouse.datasets || []).length > 0);
  const llmReady = Boolean(project?.llm?.provider && project?.llm?.model);
  const embeddingReady = Boolean(project?.embedding?.provider && project?.embedding?.model);
  const providersReady = llmReady && embeddingReady;
  // Blurb step is valid in either configuration: "use analysis LLM"
  // (blurb.enabled === false → falls back to project.llm) or a fully-
  // chosen separate model.
  const blurbValid = !blurb.enabled || (Boolean(blurb.provider) && Boolean(blurb.model));
  const indexReady = indexStatus?.status === 'ready';
  const indexInFlight = indexStatus?.status === 'indexing' || indexStatus?.status === 'pending_indexing';
  const indexNeedsTrigger = indexStatus !== null && !indexReady && !indexInFlight;

  // Kick a fresh schema-index run. Used by the inline "Build / Re-index
  // schema" button on the Generate step — the SchemaIndexPanel embedded
  // below also exposes Cancel / Retry actions, so this is just the
  // entry-point shortcut for users who land on the Generate step
  // before the worker has touched the project.
  const handleStartIndex = async () => {
    if (!warehouseReady) return;
    setKickingIndex(true);
    try {
      await api.reindexSchema(id);
      // Optimistic state flip: SchemaIndexPanel polls every 2s, so
      // without this the parent's gating UI lags 0–2s behind the
      // user's click. Setting status to pending_indexing immediately
      // means the gate, the badge, and the disabled-button hint all
      // update on the same tick the user clicks. The next real poll
      // overwrites with whatever the worker has actually done by
      // then.
      setIndexStatus({ status: 'pending_indexing' } as SchemaIndexStatus);
      notifications.show({ title: 'Indexing started', message: 'Schema indexing is running. Generate will unlock once it is ready.', color: 'blue' });
    } catch (e: unknown) {
      notifications.show({ title: 'Could not start indexing', message: (e as Error).message, color: 'red' });
    } finally {
      setKickingIndex(false);
    }
  };

  const handleSaveBlurb = async () => {
    if (!project) return;
    setSavingBlurb(true);
    try {
      const payload: Partial<Project> = blurb.enabled && blurb.provider && blurb.model
        ? {
            blurb_llm: {
              provider: blurb.provider,
              model: blurb.model,
              config: Object.fromEntries(
                Object.entries(blurb.config || {}).filter(([k]) => k !== 'model' && k !== 'api_key'),
              ),
            },
          }
        // Explicit null in the wire payload would be ideal but the API
        // shape only treats omission as "no override". Leaving the
        // existing blurb_llm in place when the user disables the
        // switch is acceptable — the agent always falls back to the
        // analysis LLM if blurb_llm is missing OR if the editor isn't
        // enabled in the wizard.
        : {};
      const saved = await api.updateProject(project.id, payload);
      if (blurb.enabled && blurb.apiKey) {
        await api.setSecret(project.id, 'blurb-llm-api-key', blurb.apiKey);
        setBlurb((prev) => ({ ...prev, apiKey: '' }));
      }
      setProject(saved);
      notifications.show({ title: 'Saved', message: 'Blurb model updated', color: 'green' });
      setActive(2);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSavingBlurb(false);
    }
  };

  const handleLaunch = async () => {
    setLaunching(true);
    // Fire pack-gen as a background HTTP call and redirect IMMEDIATELY
    // to the project page. The orchestrator runs synchronously and
    // can take 4+ minutes on big warehouses; awaiting it from the
    // wizard meant a 4-minute spinner with no live progress. The
    // project page's PackGenStatusPanel polls state + shows the
    // schema-index + synth steps live, so handing the user off as
    // soon as we know the request landed is strictly better UX.
    //
    // We poll the project doc here for up to ~3s waiting for the
    // orchestrator's first action — flipping state to
    // pack_generation — so the project page mounts with the
    // running-state UI directly, not the pending-state branch.
    const bgPromise: Promise<unknown> = api.packGenerate(id).catch((e: unknown) => {
      const msg = (e as Error).message;
      const friendly = msg.toLowerCase().includes('not available')
        ? 'Pack generation is not available on this deployment. Contact your administrator.'
        : msg;
      notifications.show({ title: 'Could not start generation', message: friendly, color: 'red' });
      // Re-fetch the project so any pack_gen_last_error the
      // orchestrator wrote shows up on the next render.
      void refresh();
    });
    void bgPromise;

    // Poll up to 15 × 200ms (3s) for state flip; bail early if the
    // project moves to pack_generation OR if something errored out
    // (state went back to pack_generation_pending with a fresh error).
    for (let i = 0; i < 15; i++) {
      await new Promise((r) => setTimeout(r, 200));
      try {
        const p = await api.getProject(id);
        if (
          p.state === PROJECT_STATE_PACK_GENERATION ||
          p.state === PROJECT_STATE_PACK_GENERATION_DONE ||
          (p.pack_gen_last_error && p.pack_gen_last_error !== project?.pack_gen_last_error)
        ) {
          setProject(p);
          break;
        }
      } catch { /* transient — keep polling */ }
    }
    setLaunching(false);
    router.push(`/projects/${id}`);
  };

  const handleDiscard = async () => {
    setDiscarding(true);
    try {
      await api.deleteProject(id);
      notifications.show({ title: 'Draft discarded', message: 'Project removed.', color: 'gray' });
      router.push('/');
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setDiscarding(false);
      closeDiscard();
    }
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  // Once we know the project is being generated or done, the redirect
  // effect above kicks in. Render nothing extra to avoid flashing the
  // wizard chrome on the way out.
  if (project.state === PROJECT_STATE_PACK_GENERATION || project.state === PROJECT_STATE_PACK_GENERATION_DONE) {
    return <Shell><Loader /></Shell>;
  }

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: project.name, href: `/projects/${id}` },
    { label: 'Generate pack' },
  ];

  const topActions = (
    <Group gap="xs">
      {/* The wizard auto-saves at every step (each panel calls
          PUT /projects on Save), so "back to projects" is a no-op
          relative to persistence. The previous label "Save and
          finish later" implied a manual save action that doesn't
          exist. */}
      <Button variant="subtle" size="xs" onClick={() => router.push('/')}>Back to projects</Button>
      <Button variant="subtle" color="red" size="xs" onClick={openDiscard}>Discard</Button>
    </Group>
  );

  return (
    <Shell breadcrumb={breadcrumb} actions={topActions}>
      <Stack gap="lg" maw={720}>
        <Stack gap={4}>
          <Group gap={8}>
            <IconWand size={20} />
            <Title order={3}>Generate domain pack</Title>
          </Group>
          <Text size="sm" c="dimmed">
            <b>{project.generate_pack?.pack_name}</b>
            {' '}({project.generate_pack?.pack_slug})
          </Text>
        </Stack>

        <Stepper active={active} onStepClick={setActive} allowNextStepsSelect={false}>
          <Stepper.Step label="LLM + embedding" description="Required to index sources">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <Text size="sm" c="dimmed">
                  Pack generation runs against your LLM provider, and knowledge sources are indexed by your embedding provider. Configure both before uploading sources.
                </Text>
                <ProvidersPanel projectId={id} variant="wizard" onSaved={(saved) => {
                  setProject(saved);
                  if (saved.llm?.provider && saved.llm?.model && saved.embedding?.provider && saved.embedding?.model) {
                    setActive(1);
                  }
                }} />
              </Stack>
            </Card>
          </Stepper.Step>

          <Stepper.Step label="Blurb model" description="Cheap model for schema descriptions">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <Text size="sm" c="dimmed">
                  Schema indexing generates a one-line description (&quot;blurb&quot;) per warehouse table that the agent embeds into Qdrant for retrieval. On ERP-scale schemas (1k+ tables) this is the bulk of the indexing token spend, and your analysis LLM is overkill — pick a cheap+fast model here. Spike winners against a real 2K-table ERP: Bedrock <code>qwen.qwen3-32b-v1:0</code>, OpenAI <code>gpt-4.1-nano</code>. Leave the switch off to reuse the analysis LLM.
                </Text>
                <BlurbLLMEditor
                  llmProviders={llmProviders}
                  value={blurb}
                  onChange={setBlurb}
                />
                <Group justify="flex-end">
                  <Button onClick={handleSaveBlurb} loading={savingBlurb} disabled={!blurbValid}>
                    Save and continue
                  </Button>
                </Group>
              </Stack>
            </Card>
          </Stepper.Step>

          <Stepper.Step label="Knowledge sources" description="What the LLM will read">
            {!providersReady ? (
              <Card withBorder p="lg" mt="md">
                <Alert color="orange" icon={<IconAlertCircle size={16} />} title="Configure providers first">
                  <Text size="sm">
                    Knowledge sources are embedded as soon as you upload them, so an embedding provider must already be saved. Go back to step 1 and finish configuring LLM + embedding before adding sources.
                  </Text>
                </Alert>
              </Card>
            ) : KnowledgeSourcesPanel ? (
              <div style={{ marginTop: 'var(--mantine-spacing-md)' }}>
                <KnowledgeSourcesPanel
                  projectId={id}
                  variant="wizard"
                  intro="Upload website URLs, DOCX/XLSX/CSV/MD/TXT files, or paste free-text notes describing your business. The agent embeds these and uses them — together with your warehouse schema — to synthesize the pack."
                />
              </div>
            ) : (
              <Card withBorder p="lg" mt="md">
                <Stack>
                  <Group gap={6}>
                    <IconUpload size={18} />
                    <Title order={5}>Knowledge sources</Title>
                  </Group>
                  <Text size="sm" c="dimmed">
                    Upload website URLs, DOCX/XLSX/CSV/MD/TXT files, or paste free-text notes describing your business. The agent embeds these and uses them — together with your warehouse schema — to synthesize the pack.
                  </Text>
                  <Alert color="blue" icon={<IconAlertCircle size={16} />} title="Knowledge sources panel not loaded">
                    This deployment does not have the knowledge-sources plugin installed. Pack generation is unlikely to produce a useful result without sources.
                  </Alert>
                </Stack>
              </Card>
            )}
          </Stepper.Step>

          <Stepper.Step label="Warehouse" description="Connect your data">
            <Card withBorder p="lg" mt="md">
              <WarehouseConfigPanel projectId={id} variant="wizard" onSaved={(saved) => {
                setProject(saved);
                if (saved.warehouse?.provider && (saved.warehouse.datasets || []).length > 0) {
                  setActive(4);
                }
              }} />
            </Card>
          </Stepper.Step>

          <Stepper.Step label="Generate" description="Run the agent">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <Title order={5}>Ready to generate</Title>
                <Text size="sm" c="dimmed">
                  Pack generation needs a fresh schema index of your warehouse — without it the LLM can only guess at table names. The panel below shows the current state; the <b>Generate pack</b> button unlocks once the index is ready.
                </Text>
                {project.pack_gen_last_error && (
                  <Alert color="red" icon={<IconAlertCircle size={16} />} title="Last attempt failed">
                    <Text size="sm" style={{ whiteSpace: 'pre-wrap' }}>{project.pack_gen_last_error}</Text>
                    <Text size="xs" c="dimmed" mt={6}>
                      Common fixes: index the warehouse first if not already done, fix the warehouse configuration if indexing failed, sharpen the pack description, or pick a more capable LLM.
                    </Text>
                  </Alert>
                )}
                <SummaryRow label="Pack name" value={project.generate_pack?.pack_name || ''} />
                <SummaryRow label="Pack slug" value={project.generate_pack?.pack_slug || ''} />
                <SummaryRow label="LLM" value={llmReady ? `${project.llm.provider} / ${project.llm.model}` : 'Not configured'} ok={llmReady} />
                <SummaryRow label="Embedding" value={embeddingReady ? `${project.embedding.provider} / ${project.embedding.model}` : 'Not configured'} ok={embeddingReady} />
                <SummaryRow
                  label="Blurb model"
                  value={
                    project.blurb_llm?.provider && project.blurb_llm?.model
                      ? `${project.blurb_llm.provider} / ${project.blurb_llm.model}`
                      : 'Reuse analysis LLM'
                  }
                />
                <SummaryRow label="Warehouse" value={warehouseReady ? `${project.warehouse.provider} / ${(project.warehouse.datasets || []).join(', ')}` : 'Not configured'} ok={warehouseReady} />
                <SummaryRow
                  label="Schema index"
                  value={
                    indexStatus === null
                      ? 'Loading…'
                      : indexReady
                        ? 'Ready'
                        : indexInFlight
                          ? `Building (${indexStatus?.status})`
                          : indexStatus?.status
                            ? `Not ready (${indexStatus.status})`
                            : 'Not built yet'
                  }
                  ok={indexReady}
                />

                {/* Inline schema-index control. Same panel the discovery
                    page uses — Build / Cancel / Retry / Re-index buttons
                    appear depending on the current status. Wires
                    onStatusChange so the Generate button below can gate
                    on the same state without a separate poll. */}
                {warehouseReady && (
                  <SchemaIndexPanel
                    projectId={id}
                    title="Schema index"
                    onStatusChange={setIndexStatus}
                  />
                )}

                <Group justify="flex-end" mt="sm">
                  {indexNeedsTrigger && (
                    <Button
                      variant="default"
                      onClick={handleStartIndex}
                      loading={kickingIndex}
                      disabled={!warehouseReady}
                    >
                      Build schema index
                    </Button>
                  )}
                  <Button
                    onClick={handleLaunch}
                    loading={launching}
                    disabled={!warehouseReady || !providersReady || !indexReady}
                  >
                    Generate pack
                  </Button>
                </Group>
                {!indexReady && warehouseReady && (
                  <Text size="xs" c="dimmed" ta="right">
                    Generate is locked until <b>Schema index: Ready</b>. {indexInFlight && 'Indexing is running — the button will unlock automatically.'}
                  </Text>
                )}
              </Stack>
            </Card>
          </Stepper.Step>
        </Stepper>

        <Group justify="space-between">
          <Button variant="default" onClick={() => setActive((c) => Math.max(0, c - 1))} disabled={active === 0}>Back</Button>
          {active < 4 && (
            <Button
              onClick={() => setActive((c) => c + 1)}
              disabled={
                (active === 0 && !providersReady) ||
                (active === 1 && !blurbValid) ||
                (active === 3 && !warehouseReady)
              }
            >
              Next
            </Button>
          )}
        </Group>
      </Stack>

      <Modal opened={discardOpened} onClose={closeDiscard} title="Discard this draft?" centered>
        <Stack>
          <Text size="sm">
            This deletes the project and any uploaded knowledge sources. This cannot be undone.
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={closeDiscard} disabled={discarding}>Cancel</Button>
            <Button color="red" loading={discarding} onClick={handleDiscard}>Discard</Button>
          </Group>
        </Stack>
      </Modal>
    </Shell>
  );
}

function SummaryRow({ label, value, ok }: { label: string; value: string; ok?: boolean }) {
  return (
    <Group justify="space-between">
      <Text size="sm" c="dimmed">{label}</Text>
      <Text size="sm" c={ok === false ? 'red' : undefined}>{value || '—'}</Text>
    </Group>
  );
}
