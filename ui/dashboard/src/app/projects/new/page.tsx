'use client';

import { useEffect, useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
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
  const t = useTranslations('newProject');
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);

  // Managed-inference deployments preset AI config server-side, so the AI,
  // Embedding, and Blurb wizard steps are skipped entirely. Default false
  // (self-hosted); fetched with the rest of the config before the stepper
  // renders, so the step set is fixed for the whole flow (no flash, no
  // shifting indices mid-wizard).
  const [aiConfigManaged, setAiConfigManaged] = useState(false);

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
      // Capability probe — a failure means an older API without it, which
      // is definitionally unmanaged, so default to showing the AI steps.
      api.getAppConfig().catch(() => ({ ai_config_managed: false })),
    ])
      .then(([domainsData, whProviders, llmProvs, embProvs, appConfig]) => {
        setDomains(domainsData);
        setWarehouseProviders(whProviders);
        setLlmProviders(llmProvs);
        setEmbeddingProviders(embProvs || []);
        setAiConfigManaged(appConfig.ai_config_managed);
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

  // Wizard steps, in order. In managed-inference mode the AI, Embedding,
  // and Blurb steps drop out — their config is preset server-side. The
  // set is fixed at load (aiConfigManaged is resolved before the stepper
  // renders), so `active` indexes a stable list for the whole flow.
  const stepIds = aiConfigManaged
    ? ['basics', 'warehouse']
    : ['basics', 'warehouse', 'ai', 'embedding', 'blurb'];

  // Per-step "can advance" predicate, keyed by step id rather than by
  // position so dropping the AI steps can't shift the wrong guard onto a
  // remaining step.
  const canProceedById: Record<string, () => boolean> = {
    basics: () => Boolean(name && domain && category),
    warehouse: () => Boolean(warehouse.provider && warehouse.config['dataset'] && (whAuthMethods.length === 0 || warehouse.authMethod) && (!authNeedsCredential || warehouse.credential)),
    // AI step: must be in the "model" phase (models loaded) and have a
    // model selected. The credentials phase uses its own "Load models"
    // button instead of Next. A user-deployed endpoint is the exception:
    // it has no model picker, so the credentials phase alone is complete
    // once the endpoint ID is filled.
    ai: () => Boolean(llm.provider && (
      llm.config['endpoint_id']?.trim()
        ? ((llmAuthMethods.length === 0 || llm.authMethod) && (!llmNeedsCredential || llm.apiKey))
        : (aiPhase === 'model' && llm.config['model'])
    )),
    // Embedding step: mandatory — schema indexing won't start without
    // a provider + model. API key required when the provider asks for
    // one (OpenAI, Voyage, etc); cloud-creds providers (Bedrock,
    // Vertex) skip that check.
    embedding: () => Boolean(embedding.provider) && Boolean(embedding.model) && (!embNeedsKey || Boolean(embedding.apiKey)),
    // Blurb step: valid when the user either chose "use analysis LLM"
    // (blurb.enabled === false) or picked a model.
    blurb: () => Boolean(!blurb.enabled || (blurb.provider && blurb.model)),
  };

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
        // In managed mode the server presets llm/embedding/blurb_llm from
        // the gateway config and ignores anything sent here, so omit them
        // entirely — the wizard never collected them.
        ...(aiConfigManaged ? {} : {
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
        }),
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
        if (warehouse.credential) writes.push(api.setSecret(project.id, 'warehouse-credentials', warehouse.credential));
        // AI credentials are never written in managed mode — the gateway
        // key rides the deployment env fallback, and the server refuses
        // per-project AI credential writes anyway.
        if (!aiConfigManaged) {
          if (llm.apiKey) writes.push(api.setSecret(project.id, 'llm-credentials', llm.apiKey));
          // Blurb-LLM credentials are stored separately. Only written when
          // the user supplied one — otherwise the agent falls back to
          // `llm-credentials`.
          if (blurb.enabled && blurb.apiKey) writes.push(api.setSecret(project.id, 'blurb-llm-credentials', blurb.apiKey));
          // Embedding credentials — required by the worker pre-flight if
          // the provider exposes a credential field. Safe to save
          // conditionally on user input (empty → skip, preserves an
          // existing stored key on re-creates).
          if (embedding.apiKey) writes.push(api.setSecret(project.id, 'embedding-credentials', embedding.apiKey));
        }
        await Promise.all(writes);
      }

      notifications.show({ title: t('createdTitle'), message: project.name, color: 'green' });
      router.push(`/projects/${project.id}`);
    } catch (e: unknown) {
      notifications.show({ title: t('errorTitle'), message: (e as Error).message, color: 'red' });
    } finally {
      setLoading(false);
    }
  };

  return (
    <Shell>
      <Stack gap="lg" maw={700}>
        <Title order={2}>{t('title')}</Title>

        {dataError && (
          <Alert icon={<IconAlertCircle size={16} />} title={t('loadErrorTitle')} color="red">{dataError}</Alert>
        )}

        {dataLoading && (
          <Group><Loader size="sm" /><Text size="sm" c="dimmed">{t('loadingConfig')}</Text></Group>
        )}

        {!dataLoading && !dataError && (
          <>
            <Stepper active={active} onStepClick={setActive}>
              <Stepper.Step label={t('stepBasics')} description={t('stepBasicsDesc')}>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <TextInput label={t('nameLabel')} required value={name} onChange={(e) => setName(e.target.value)} placeholder={t('namePlaceholder')} />
                    <Textarea label={t('descriptionLabel')} value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t('descriptionPlaceholder')} />
                    <Select label={t('domainLabel')} required placeholder={t('domainPlaceholder')}
                      data={domains.map((d) => ({ value: d.id, label: d.id }))}
                      value={domain} onChange={(v) => { setDomain(v || ''); setCategory(''); }} />
                    {domain && categories.length > 0 && (
                      <Select label={t('categoryLabel')} required placeholder={t('categoryPlaceholder')}
                        data={categories.map((c) => ({ value: c.id, label: c.name }))}
                        value={category} onChange={(v) => setCategory(v || '')} />
                    )}
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label={t('stepWarehouse')} description={t('stepWarehouseDesc')}>
                <Card withBorder p="lg" mt="md">
                  <WarehouseFormFields
                    providers={warehouseProviders}
                    value={warehouse}
                    onChange={setWarehouse}
                  />
                </Card>
              </Stepper.Step>

              {/* AI / Embedding / Blurb steps are hidden in managed-inference
                  mode — their config is preset server-side. Mantine's Stepper
                  filters out these `false` children, so the remaining step
                  indices collapse cleanly. */}
              {!aiConfigManaged && (
              <Stepper.Step label={t('stepAi')} description={t('stepAiDesc')}>
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
              )}

              {!aiConfigManaged && (
              <Stepper.Step label={t('stepEmbedding')} description={t('stepEmbeddingDesc')}>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Text size="sm" c="dimmed">
                      {t.rich('embeddingHelp', { code: (chunks) => <code>{chunks}</code> })}
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
              )}

              {!aiConfigManaged && (
              <Stepper.Step label={t('stepBlurb')} description={t('stepBlurbDesc')}>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Text size="sm" c="dimmed">
                      {t.rich('blurbHelp', { code: (chunks) => <code>{chunks}</code> })}
                    </Text>
                    <BlurbLLMEditor
                      llmProviders={llmProviders}
                      value={blurb}
                      onChange={setBlurb}
                    />
                  </Stack>
                </Card>
              </Stepper.Step>
              )}

              <Stepper.Completed>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Title order={4}>{t('readyTitle')}</Title>
                    <Text><strong>{t('summaryName')}</strong> {name}</Text>
                    <Text><strong>{t('summaryDomain')}</strong> {domain} / {category}</Text>
                    <Text><strong>{t('summaryWarehouse')}</strong> {selectedWarehouse?.name} / {warehouse.config['dataset']}</Text>
                    {aiConfigManaged ? (
                      <Text c="dimmed"><strong>{t('summaryAi')}</strong> {t('summaryAiManaged')}</Text>
                    ) : (
                      <>
                        <Text><strong>{t('summaryLlm')}</strong> {selectedLLM?.name} / {llm.config['endpoint_id']?.trim() ? t('summaryEndpoint', { id: llm.config['endpoint_id'] }) : llm.config['model']}</Text>
                        <Text>
                          <strong>{t('summaryEmbedding')}</strong>{' '}
                          {embProviderMeta?.name || embedding.provider} / {embedding.model}
                        </Text>
                        <Text>
                          <strong>{t('summaryBlurb')}</strong>{' '}
                          {blurb.enabled && blurb.model
                            ? `${llmProviders.find((p) => p.id === blurb.provider)?.name || blurb.provider} / ${blurb.model}`
                            : t('summaryBlurbSameAsAnalysis')}
                        </Text>
                      </>
                    )}
                    <Button onClick={handleCreate} loading={loading} fullWidth mt="md">{t('createButton')}</Button>
                  </Stack>
                </Card>
              </Stepper.Completed>
            </Stepper>

            <Group justify="flex-end">
              {active > 0 && <Button variant="default" onClick={() => setActive((c) => c - 1)}>{t('back')}</Button>}
              {active < stepIds.length && <Button onClick={() => setActive((c) => c + 1)} disabled={!canProceedById[stepIds[active]]?.()}>{t('next')}</Button>}
            </Group>
          </>
        )}
      </Stack>
    </Shell>
  );
}
