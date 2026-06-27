'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, Select, Stack, Stepper, Text, TextInput, Textarea, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import { EmbeddingEditor, EmbeddingState, emptyEmbeddingState } from '@/components/EmbeddingEditor';
import { WarehouseFormFields, WarehouseFormState, emptyWarehouseFormState, buildDefaults } from '@/components/projects/WarehouseFormFields';
import { LLMFormFields, LLMFormState, emptyLLMFormState, AIPhase } from '@/components/projects/LLMFormFields';
import { api, Domain, Category, ProviderMeta, EmbeddingProviderMeta, LiveModel } from '@/lib/api';

export default function NewProjectPage() {
  const router = useRouter();
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);

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
  // Optional per-project blurb LLM override.
  // Defaults to "use analysis LLM" — when the user turns the switch on,
  // the component renders a full provider + live-model picker.
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  // Embedding provider is mandatory — schema indexing will not start
  // without one. We require it up front instead of letting
  // the user finish creation and then immediately hit a "failed" banner
  // on the project-detail page.
  const [embedding, setEmbedding] = useState<EmbeddingState>(emptyEmbeddingState);

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
          const methods = first.auth_methods ?? [];
          setEmbedding({
            provider: first.id,
            authMethod: methods.length === 1 ? methods[0].id : '',
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
          const methods = first.auth_methods ?? [];
          setLlm((prev) => ({
            ...prev,
            provider: first.id,
            authMethod: methods.length === 1 ? methods[0].id : '',
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

  // LLM credential requirement for the selected auth method. The normal
  // flow gates this via the "Load models" button; the endpoint branch
  // below has no such step, so it checks the same condition directly.
  const llmAuthMethods = selectedLLM?.auth_methods || [];
  const llmSelectedAuthMethod = llmAuthMethods.find((m) => m.id === llm.authMethod);
  const llmNeedsCredential = (llmSelectedAuthMethod?.fields || []).some((f) => f.type === 'credential');

  const canProceed = [
    () => name && domain && category,
    () => warehouse.provider && warehouse.config['dataset'] && (whAuthMethods.length === 0 || warehouse.authMethod) && (!authNeedsCredential || warehouse.credential),
    // AI step: must be in the "model" phase (models loaded) and have a
    // model selected. The credentials phase uses its own "Load models"
    // button instead of Next. A user-deployed endpoint is the exception:
    // it has no model picker, so the credentials phase alone is complete
    // once the endpoint ID is filled.
    () => llm.provider && (
      llm.config['endpoint_id']?.trim()
        ? ((llmAuthMethods.length === 0 || llm.authMethod) && (!llmNeedsCredential || llm.apiKey))
        : (aiPhase === 'model' && llm.config['model'])
    ),
    // Embedding step: mandatory — schema indexing won't start without
    // a provider + model. API key required when the provider asks for
    // one (OpenAI, Voyage, etc); cloud-creds providers (Bedrock,
    // Vertex) skip that check.
    () => Boolean(embedding.provider) && Boolean(embedding.model) && (!embNeedsKey || Boolean(embedding.apiKey)),
    // Blurb step: valid when the user either chose "use analysis LLM"
    // (blurb.enabled === false) or picked a model.
    () => !blurb.enabled || (blurb.provider && blurb.model),
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
      // Build the config map the backend expects: every form-state
      // field plus auth_method + credentials_json (every provider
      // factory reads the credential from cfg["credentials_json"] now;
      // api_key as a top-level key was removed during the auth-method
      // refactor — the live-list call must mirror what the agent
      // sends at indexing time).
      const config: Record<string, string> = { ...llm.config };
      if (llm.authMethod) config['auth_method'] = llm.authMethod;
      if (llm.apiKey) config['credentials_json'] = llm.apiKey;
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
          // A user-deployed endpoint identifies its own model, so persist
          // an empty model — the hidden picker's catalog default must not
          // leak into the saved config.
          model: llm.config['endpoint_id']?.trim() ? '' : (llm.config['model'] || ''),
          config: {
            ...Object.fromEntries(
              // Drop wire_override in endpoint mode: it is hidden in the
              // form but a stale value would be rejected by the provider
              // (an endpoint always uses the OpenAI chat-completions wire).
              Object.entries(llm.config).filter(([k]) =>
                k !== 'model' && k !== 'api_key' &&
                !(k === 'wire_override' && llm.config['endpoint_id']?.trim())
              )
            ),
            ...(llm.authMethod ? { auth_method: llm.authMethod } : {}),
          },
        },
        embedding: {
          provider: embedding.provider,
          model: embedding.model,
          // Persist every form-state config field (project_id, location,
          // region, …) plus auth_method when picked. Required for
          // Vertex (project_id + location), Bedrock (region), and any
          // provider whose factory reads non-credential settings from
          // ProviderConfig. Drop "model" because it lives in the
          // top-level Model field, not Config.
          config: {
            ...Object.fromEntries(Object.entries(embedding.config).filter(([k]) => k !== 'model')),
            ...(embedding.authMethod ? { auth_method: embedding.authMethod } : {}),
          },
        },
        // Only send blurb_llm when the user explicitly overrode it; otherwise
        // the agent falls back to the analysis LLM (its own fallback path).
        ...(blurb.enabled && blurb.provider && blurb.model
          ? {
              blurb_llm: {
                provider: blurb.provider,
                model: blurb.model,
                config: {
                  ...Object.fromEntries(
                    Object.entries(blurb.config).filter(([k]) => k !== 'model' && k !== 'api_key')
                  ),
                  ...(blurb.authMethod ? { auth_method: blurb.authMethod } : {}),
                },
              },
            }
          : {}),
      });
      // Save secrets in parallel — sequential awaits left a ~1s race
      // window between project creation (which sets
      // schema_index_status=pending_indexing) and the schema-index
      // worker's next poll. The worker would claim the project before
      // embedding-credentials was stored and fail with "API key is
      // required". Promise.all compresses the window to a single
      // round-trip (~250ms) which is shorter than the worker's poll
      // interval, so in practice the race no longer fires.
      if (project.id) {
        const writes: Promise<unknown>[] = [];
        if (llm.apiKey) writes.push(api.setSecret(project.id, 'llm-credentials', llm.apiKey));
        if (warehouse.credential) writes.push(api.setSecret(project.id, 'warehouse-credentials', warehouse.credential));
        // Blurb-LLM credentials are stored separately. Only written when
        // the user supplied one — otherwise the agent falls back to
        // `llm-credentials`.
        if (blurb.enabled && blurb.apiKey) writes.push(api.setSecret(project.id, 'blurb-llm-credentials', blurb.apiKey));
        // Embedding credentials — required by the worker pre-flight if
        // the provider exposes a credential field. Safe to save
        // conditionally on user input (empty → skip, preserves an
        // existing stored key on re-creates).
        if (embedding.apiKey) writes.push(api.setSecret(project.id, 'embedding-credentials', embedding.apiKey));
        await Promise.all(writes);
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

              <Stepper.Completed>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Title order={4}>Ready to create</Title>
                    <Text><strong>Name:</strong> {name}</Text>
                    <Text><strong>Domain:</strong> {domain} / {category}</Text>
                    <Text><strong>Warehouse:</strong> {selectedWarehouse?.name} / {warehouse.config['dataset']}</Text>
                    <Text><strong>LLM:</strong> {selectedLLM?.name} / {llm.config['endpoint_id']?.trim() ? `endpoint ${llm.config['endpoint_id']}` : llm.config['model']}</Text>
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
              {active < 5 && <Button onClick={() => setActive((c) => c + 1)} disabled={!canProceed[active]?.()}>Next</Button>}
            </Group>
          </>
        )}
      </Stack>
    </Shell>
  );
}
