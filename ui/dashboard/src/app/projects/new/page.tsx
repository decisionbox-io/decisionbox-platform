'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, SegmentedControl, Select, Stack, Stepper, Text, TextInput, Textarea, Title, NumberInput, Switch,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconWand } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import { EmbeddingEditor, EmbeddingState, emptyEmbeddingState } from '@/components/EmbeddingEditor';
import { WarehouseFormFields, WarehouseFormState, emptyWarehouseFormState, buildDefaults } from '@/components/projects/WarehouseFormFields';
import { LLMFormFields, LLMFormState, emptyLLMFormState, AIPhase } from '@/components/projects/LLMFormFields';
import { api, Domain, Category, ProviderMeta, EmbeddingProviderMeta, LiveModel, PROJECT_STATE_PACK_GENERATION_PENDING } from '@/lib/api';

type Mode = 'builtin' | 'generate';

export default function NewProjectPage() {
  const router = useRouter();
  const [mode, setMode] = useState<Mode>('builtin');
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);

  // Generate-mode fields (used only when mode === 'generate'). The user
  // names the pack here; the wizard at /projects/{id}/generate then
  // collects sources, warehouse, and providers before launching pack-gen.
  const [genPackName, setGenPackName] = useState('');
  const [genPackSlug, setGenPackSlug] = useState('');
  const [genPackSlugTouched, setGenPackSlugTouched] = useState(false);
  const [genPackDescription, setGenPackDescription] = useState('');

  // Data from API (dynamic)
  const [domains, setDomains] = useState<Domain[]>([]);
  const [warehouseProviders, setWarehouseProviders] = useState<ProviderMeta[]>([]);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [embeddingProviders, setEmbeddingProviders] = useState<EmbeddingProviderMeta[]>([]);
  const [dataLoading, setDataLoading] = useState(true);
  const [dataError, setDataError] = useState<string | null>(null);

  // Form state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [domain, setDomain] = useState('');
  const [category, setCategory] = useState('');
  const [warehouse, setWarehouse] = useState<WarehouseFormState>(emptyWarehouseFormState);
  const [llm, setLlm] = useState<LLMFormState>(emptyLLMFormState);

  // AI step is split in two phases:
  //   'credentials' — pick provider + fill API key / cloud creds
  //   'model'       — pick model from the live-loaded list
  // Advancing from 'credentials' to 'model' runs the live-list call; if
  // the upstream fails the user still gets the catalog as a fallback
  // and an inline error.
  const [aiPhase, setAiPhase] = useState<AIPhase>('credentials');
  const [aiLoading, setAiLoading] = useState(false);
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);
  const [liveError, setLiveError] = useState<string | null>(null);
  // Optional per-project blurb LLM override (PLAN-SCHEMA-RETRIEVAL.md §6.2).
  // Defaults to "use analysis LLM" — when the user turns the switch on,
  // the component renders a full provider + live-model picker.
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  // Embedding provider is mandatory — schema indexing will not start
  // without one (plan §3.7). We require it up front instead of letting
  // the user finish creation and then immediately hit a "failed" banner
  // on the project-detail page.
  const [embedding, setEmbedding] = useState<EmbeddingState>(emptyEmbeddingState);
  const [scheduleEnabled, setScheduleEnabled] = useState(true);
  const [scheduleCron, setScheduleCron] = useState('0 2 * * *');
  const [maxSteps, setMaxSteps] = useState(100);

  useEffect(() => {
    Promise.all([
      api.listDomains(),
      api.listWarehouseProviders(),
      api.listLLMProviders(),
      api.listEmbeddingProviders(),
    ])
      .then(([domainsData, whProviders, llmProvs, embProvs]) => {
        setDomains(domainsData);
        setWarehouseProviders(whProviders);
        setLlmProviders(llmProvs);
        setEmbeddingProviders(embProvs || []);
        // Pre-select the first embedding provider (usually OpenAI per
        // the spike winners). The user can change it, but the field
        // starts populated so the common case is one click.
        if ((embProvs || []).length > 0) {
          const openai = embProvs.find((p) => p.id === 'openai');
          const first = openai || embProvs[0];
          setEmbedding({
            provider: first.id,
            model: first.models.find((m) => m.id === 'text-embedding-3-large')?.id || first.models[0]?.id || '',
            config: {},
            apiKey: '',
          });
        }

        if (domainsData.length === 1) {
          setDomain(domainsData[0].id);
          if (domainsData[0].categories.length === 1) setCategory(domainsData[0].categories[0].id);
        }
        if (whProviders.length > 0) {
          const first = whProviders[0];
          setWarehouse((prev) => ({
            ...prev,
            provider: first.id,
            config: buildDefaults(first.config_fields),
            authMethod: first.auth_methods?.length === 1 ? first.auth_methods[0].id : '',
          }));
        }
        if (llmProvs.length > 0) {
          const claude = llmProvs.find((p) => p.id === 'claude');
          const first = claude || llmProvs[0];
          setLlm((prev) => ({
            ...prev,
            provider: first.id,
            config: buildDefaults(first.config_fields),
          }));
        }
      })
      .catch((e) => setDataError(e.message))
      .finally(() => setDataLoading(false));
  }, []);

  const categories: Category[] = domains.find((d) => d.id === domain)?.categories || [];
  const selectedWarehouse = warehouseProviders.find((p) => p.id === warehouse.provider);
  const selectedLLM = llmProviders.find((p) => p.id === llm.provider);

  const whAuthMethods = selectedWarehouse?.auth_methods || [];
  const selectedAuthMethod = whAuthMethods.find((m) => m.id === warehouse.authMethod);
  const authCredentialField = (selectedAuthMethod?.fields || []).find((f) => f.type === 'credential');
  const authNeedsCredential = authCredentialField?.required ?? false;

  const embProviderMeta = embeddingProviders.find((p) => p.id === embedding.provider);
  const embNeedsKey = embProviderMeta?.config_fields.some(
    (f) => f.type === 'credential' || f.key === 'api_key'
  ) ?? false;

  const canProceed = [
    () => name && domain && category,
    () => warehouse.provider && warehouse.config['dataset'] && (whAuthMethods.length === 0 || warehouse.authMethod) && (!authNeedsCredential || warehouse.credential),
    // AI step: must be in the "model" phase (models loaded) and have a
    // model selected. The credentials phase uses its own "Load models"
    // button instead of Next.
    () => aiPhase === 'model' && llm.provider && llm.config['model'],
    // Embedding step: mandatory — schema indexing won't start without
    // a provider + model. API key required when the provider asks for
    // one (OpenAI, Voyage, etc); cloud-creds providers (Bedrock,
    // Vertex) skip that check.
    () => Boolean(embedding.provider) && Boolean(embedding.model) && (!embNeedsKey || Boolean(embedding.apiKey)),
    // Blurb step: valid when the user either chose "use analysis LLM"
    // (blurb.enabled === false) or picked a model.
    () => !blurb.enabled || (blurb.provider && blurb.model),
    () => true,
  ];

  // Monotonic request id so a stale response from an in-flight fetch
  // (e.g. user clicked Load models twice, or switched provider mid-
  // flight) doesn't overwrite newer state.
  const loadReqIdRef = useRef(0);

  const loadLiveModels = async () => {
    if (!llm.provider) return;
    const reqId = ++loadReqIdRef.current;
    const provider = llm.provider;
    setAiLoading(true);
    setLiveError(null);
    try {
      // Build the config map the backend expects: every field the user
      // filled in, plus api_key as its own key (the factories all read
      // cfg["api_key"]).
      const config: Record<string, string> = { ...llm.config };
      if (llm.apiKey) config['api_key'] = llm.apiKey;
      const resp = await api.listLiveLLMModels(provider, config);
      if (reqId !== loadReqIdRef.current) return; // superseded
      setLiveModels(resp.models);
      if (resp.live_error) setLiveError(resp.live_error);
      setAiPhase('model');
    } catch (e: unknown) {
      if (reqId !== loadReqIdRef.current) return; // superseded
      setLiveError((e as Error).message);
      // Still advance to phase 2 — user can type a model manually.
      setAiPhase('model');
    } finally {
      if (reqId === loadReqIdRef.current) setAiLoading(false);
    }
  };

  // Create the draft project for "Generate one for me" mode. The
  // project starts in pack_generation_pending state with empty
  // domain/category — those are populated by the agent after the
  // generated pack is saved. Sources, warehouse, and providers are
  // collected on the wizard at /projects/{id}/generate.
  const handleCreateGenerateDraft = async () => {
    if (!name || !genPackName || !genPackSlug) return;
    setLoading(true);
    try {
      const project = await api.createProject({
        name,
        description,
        domain: '',
        category: '',
        state: PROJECT_STATE_PACK_GENERATION_PENDING,
        generate_pack: {
          enabled: true,
          pack_name: genPackName,
          pack_slug: genPackSlug,
          ...(genPackDescription ? { description: genPackDescription } : {}),
        },
      });
      notifications.show({ title: 'Draft created', message: 'Continue to the pack-gen wizard', color: 'green' });
      router.push(`/projects/${project.id}/generate`);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setLoading(false);
    }
  };

  const handleCreate = async () => {
    setLoading(true);
    try {
      const project = await api.createProject({
        name, description, domain, category,
        warehouse: {
          provider: warehouse.provider,
          project_id: warehouse.config['project_id'] || '',
          datasets: (warehouse.config['dataset'] || '').split(',').map((d) => d.trim()).filter(Boolean),
          location: warehouse.config['location'] || '',
          filter_field: warehouse.filterField,
          filter_value: warehouse.filterValue,
          config: {
            ...Object.fromEntries(
              Object.entries(warehouse.config).filter(([k]) => k !== 'project_id' && k !== 'location' && k !== 'dataset')
            ),
            ...(warehouse.authMethod ? { auth_method: warehouse.authMethod } : {}),
          },
        },
        llm: {
          provider: llm.provider,
          model: llm.config['model'] || '',
          config: Object.fromEntries(
            Object.entries(llm.config).filter(([k]) => k !== 'model' && k !== 'api_key')
          ),
        },
        embedding: {
          provider: embedding.provider,
          model: embedding.model,
        },
        // Only send blurb_llm when the user explicitly overrode it; otherwise
        // the agent falls back to the analysis LLM (its own fallback path).
        ...(blurb.enabled && blurb.provider && blurb.model
          ? {
              blurb_llm: {
                provider: blurb.provider,
                model: blurb.model,
                config: Object.fromEntries(
                  Object.entries(blurb.config).filter(([k]) => k !== 'model' && k !== 'api_key')
                ),
              },
            }
          : {}),
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
      });
      // Save secrets
      if (llm.apiKey && project.id) {
        await api.setSecret(project.id, 'llm-api-key', llm.apiKey);
      }
      if (warehouse.credential && project.id) {
        await api.setSecret(project.id, 'warehouse-credentials', warehouse.credential);
      }
      // Blurb-LLM key is stored separately. Only written when the user
      // supplied one — otherwise the agent falls back to `llm-api-key`.
      if (blurb.enabled && blurb.apiKey && project.id) {
        await api.setSecret(project.id, 'blurb-llm-api-key', blurb.apiKey);
      }
      // Embedding key — required by the worker pre-flight if the
      // provider exposes a credential field. Safe to save conditionally
      // on user input (empty → skip, preserves an existing stored key
      // on re-creates).
      if (embedding.apiKey && project.id) {
        await api.setSecret(project.id, 'embedding-api-key', embedding.apiKey);
      }

      notifications.show({ title: 'Project created', message: project.name, color: 'green' });
      router.push(`/projects/${project.id}`);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setLoading(false);
    }
  };

  return (
    <Shell>
      <Stack gap="lg" maw={700}>
        <Title order={2}>New Project</Title>

        {dataError && (
          <Alert icon={<IconAlertCircle size={16} />} title="Cannot load configuration" color="red">{dataError}</Alert>
        )}

        {dataLoading && (
          <Group><Loader size="sm" /><Text size="sm" c="dimmed">Loading configuration...</Text></Group>
        )}

        {!dataLoading && !dataError && (
          <SegmentedControl
            value={mode}
            onChange={(v) => setMode(v as Mode)}
            data={[
              { value: 'builtin', label: 'Use a built-in pack' },
              { value: 'generate', label: 'Generate one for me' },
            ]}
            fullWidth
          />
        )}

        {!dataLoading && !dataError && mode === 'generate' && (
          <Card withBorder p="lg">
            <Stack>
              <Group gap={6}>
                <IconWand size={18} />
                <Title order={4}>Generate a domain pack</Title>
              </Group>
              <Text size="sm" c="dimmed">
                We&apos;ll create a draft project, then walk you through uploading knowledge sources and connecting your warehouse. The agent reads everything and synthesizes the full pack — categories, profile, analysis areas, and prompts — for you.
              </Text>
              <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="My Domain Discovery" />
              <Textarea label="Project Description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional" />
              <TextInput label="Pack Name" required value={genPackName}
                onChange={(e) => {
                  const v = e.target.value;
                  setGenPackName(v);
                  if (!genPackSlugTouched) setGenPackSlug(slugify(v));
                }}
                placeholder="Acme Marketplace" />
              <TextInput label="Pack Slug" required value={genPackSlug}
                onChange={(e) => { setGenPackSlug(e.target.value); setGenPackSlugTouched(true); }}
                description="Lowercase, hyphen-separated. Used as the pack&apos;s identifier."
                placeholder="acme-marketplace" />
              <Textarea label="Description (optional)" value={genPackDescription}
                onChange={(e) => setGenPackDescription(e.target.value)}
                placeholder="One or two sentences about the business — anything you want the LLM to weight heavily." />
              <Group justify="flex-end">
                <Button onClick={handleCreateGenerateDraft} loading={loading}
                  disabled={!name || !genPackName || !slugIsValid(genPackSlug)}>
                  Create draft and continue
                </Button>
              </Group>
            </Stack>
          </Card>
        )}

        {!dataLoading && !dataError && mode === 'builtin' && (
          <>
            <Stepper active={active} onStepClick={setActive}>
              <Stepper.Step label="Basics" description="Name and domain">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="My Game Analytics" />
                    <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional description" />
                    <Select label="Domain" required placeholder="Select a domain"
                      data={domains.map((d) => ({ value: d.id, label: d.id.charAt(0).toUpperCase() + d.id.slice(1) }))}
                      value={domain} onChange={(v) => { setDomain(v || ''); setCategory(''); }} />
                    {domain && categories.length > 0 && (
                      <Select label="Category" required placeholder="Select a category"
                        data={categories.map((c) => ({ value: c.id, label: c.name }))}
                        value={category} onChange={(v) => setCategory(v || '')} />
                    )}
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Warehouse" description="Data source">
                <Card withBorder p="lg" mt="md">
                  <WarehouseFormFields
                    providers={warehouseProviders}
                    value={warehouse}
                    onChange={setWarehouse}
                  />
                </Card>
              </Stepper.Step>

              <Stepper.Step label="AI" description="Provider + model">
                <Card withBorder p="lg" mt="md">
                  <LLMFormFields
                    providers={llmProviders}
                    value={llm}
                    onChange={setLlm}
                    phase={aiPhase}
                    onPhaseChange={(next) => {
                      setAiPhase(next);
                      if (next === 'credentials') {
                        setLiveModels(null);
                        setLiveError(null);
                      }
                    }}
                    liveModels={liveModels}
                    liveError={liveError}
                    loading={aiLoading}
                    onLoadModels={loadLiveModels}
                  />
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Embedding" description="Vector model">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Text size="sm" c="dimmed">
                      Used to embed schema blurbs (for retrieval during discovery) and discovered insights (for semantic search). Schema indexing will not start until this is configured. Default recommendation from the spike against a real 2K-table ERP: OpenAI <code>text-embedding-3-large</code>.
                    </Text>
                    <EmbeddingEditor
                      providers={embeddingProviders}
                      value={embedding}
                      onChange={setEmbedding}
                      required
                    />
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Blurb Model" description="Schema-index LLM">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Text size="sm" c="dimmed">
                      The blurb model generates per-table descriptions during schema indexing (the ones the retriever embeds in Qdrant). A separate cheap + fast model here usually pays off — spike winners were Bedrock <code>qwen.qwen3-32b-v1:0</code> and OpenAI <code>gpt-4.1-nano</code>. Leave off to reuse the analysis LLM.
                    </Text>
                    <BlurbLLMEditor
                      llmProviders={llmProviders}
                      value={blurb}
                      onChange={setBlurb}
                    />
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Schedule" description="Discovery schedule">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Switch label="Enable automatic discovery" checked={scheduleEnabled}
                      onChange={(e) => setScheduleEnabled(e.currentTarget.checked)} />
                    {scheduleEnabled && (
                      <TextInput label="Cron Expression" value={scheduleCron}
                        onChange={(e) => setScheduleCron(e.target.value)} description="Default: daily at 2 AM UTC" />
                    )}
                    <NumberInput label="Max Exploration Steps" value={maxSteps}
                      onChange={(v) => setMaxSteps(Number(v) || 100)} min={10} max={500} />
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Completed>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Title order={4}>Ready to create</Title>
                    <Text><strong>Name:</strong> {name}</Text>
                    <Text><strong>Domain:</strong> {domain} / {category}</Text>
                    <Text><strong>Warehouse:</strong> {selectedWarehouse?.name} / {warehouse.config['dataset']}</Text>
                    <Text><strong>LLM:</strong> {selectedLLM?.name} / {llm.config['model']}</Text>
                    <Text>
                      <strong>Embedding:</strong>{' '}
                      {embProviderMeta?.name || embedding.provider} / {embedding.model}
                    </Text>
                    <Text>
                      <strong>Blurb model:</strong>{' '}
                      {blurb.enabled && blurb.model
                        ? `${llmProviders.find((p) => p.id === blurb.provider)?.name || blurb.provider} / ${blurb.model}`
                        : 'same as analysis LLM'}
                    </Text>
                    <Button onClick={handleCreate} loading={loading} fullWidth mt="md">Create Project</Button>
                  </Stack>
                </Card>
              </Stepper.Completed>
            </Stepper>

            <Group justify="flex-end">
              {active > 0 && <Button variant="default" onClick={() => setActive((c) => c - 1)}>Back</Button>}
              {active < 6 && <Button onClick={() => setActive((c) => c + 1)} disabled={!canProceed[active]?.()}>Next</Button>}
            </Group>
          </>
        )}
      </Stack>
    </Shell>
  );
}

// Mirror of the API's slug regex (^[a-z][a-z0-9-]*$). Used to drive the
// auto-derived slug field in generate-mode and the disabled state on the
// Create button.
const SLUG_RE = /^[a-z][a-z0-9-]*$/;

function slugify(input: string): string {
  return input
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

function slugIsValid(slug: string): boolean {
  return SLUG_RE.test(slug);
}
