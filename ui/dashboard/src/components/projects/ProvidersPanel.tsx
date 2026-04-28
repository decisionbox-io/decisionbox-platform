'use client';

import { useEffect, useRef, useState } from 'react';
import {
  Alert, Button, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconShieldCheck } from '@tabler/icons-react';
import { EmbeddingEditor, EmbeddingState, emptyEmbeddingState } from '@/components/EmbeddingEditor';
import {
  api, EmbeddingProviderMeta, LiveModel, Project, ProviderMeta, SecretEntryResponse,
} from '@/lib/api';
import {
  AIPhase, LLMFormFields, LLMFormState, emptyLLMFormState,
} from './LLMFormFields';
import { buildDefaults } from './WarehouseFormFields';

type Variant = 'page' | 'wizard';

export interface ProvidersPanelProps {
  projectId: string;
  variant: Variant;
  onSaved?: (project: Project) => void;
}

function PanelSection({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: '20px',
      maxWidth: 640,
    }}>
      <Stack gap="md">{children}</Stack>
    </div>
  );
}

// ProvidersPanel manages the load + save lifecycle for a project's LLM
// + embedding configuration. Form rendering is delegated to
// `LLMFormFields` (LLM provider + credentials/model phase) and
// `EmbeddingEditor` (embedding provider + model + key) so this panel
// shares its rendering with the new-project wizard. Both providers save
// in one click via PUT /projects/{id}; secret keys (llm-api-key,
// embedding-api-key) rotate only when the user enters a new value.
// Used by:
//   - settings/page.tsx          (variant="page")
//   - pack-gen wizard            (variant="wizard")
export default function ProvidersPanel({ projectId, variant, onSaved }: ProvidersPanelProps) {
  const [project, setProject] = useState<Project | null>(null);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [embeddingProviders, setEmbeddingProviders] = useState<EmbeddingProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [secretsList, setSecretsList] = useState<SecretEntryResponse[]>([]);

  const [llm, setLlm] = useState<LLMFormState>(emptyLLMFormState);
  // The settings page lands users straight into "model" phase when a
  // provider is already configured (no need to re-enter credentials);
  // wizard mode for fresh projects starts in "credentials".
  const [aiPhase, setAiPhase] = useState<AIPhase>('credentials');
  const [aiLoading, setAiLoading] = useState(false);
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);
  const [liveError, setLiveError] = useState<string | null>(null);
  const liveReqIdRef = useRef(0);

  const [embedding, setEmbedding] = useState<EmbeddingState>(emptyEmbeddingState);

  const loadOnce = useRef(false);
  useEffect(() => {
    if (loadOnce.current) return;
    loadOnce.current = true;
    Promise.all([
      api.getProject(projectId),
      api.listLLMProviders(),
      api.listEmbeddingProviders(),
    ])
      .then(([proj, llmProvs, embProvs]) => {
        setProject(proj);
        setLlmProviders(llmProvs);
        setEmbeddingProviders(embProvs || []);
        const provMeta = llmProvs.find((p) => p.id === proj.llm.provider);
        const fieldDefaults = provMeta ? buildDefaults(provMeta.config_fields) : {};
        setLlm({
          provider: proj.llm.provider || '',
          config: {
            ...fieldDefaults,
            ...(proj.llm.config || {}),
            ...(proj.llm.model ? { model: proj.llm.model } : {}),
          },
          apiKey: '',
        });
        setEmbedding({
          provider: proj.embedding?.provider || '',
          model: proj.embedding?.model || '',
          config: {},
          apiKey: '',
        });
        if (proj.llm.provider) {
          // Existing project — jump straight to model phase and
          // pre-load the live model list so the user does not have to
          // click Load models for an unchanged provider.
          setAiPhase('model');
          const reqId = ++liveReqIdRef.current;
          api.listLiveLLMModelsForProject(proj.id)
            .then((resp) => {
              if (reqId !== liveReqIdRef.current) return;
              setLiveModels(resp.models);
              if (resp.live_error) setLiveError(resp.live_error);
            })
            .catch((e) => {
              if (reqId !== liveReqIdRef.current) return;
              setLiveError((e as Error).message);
            });
        }
        api.listSecrets(projectId).then((s) => setSecretsList(s || [])).catch(() => {});
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [projectId]);

  const selectedLlm = llmProviders.find((p) => p.id === llm.provider);
  const llmNeedsApiKey = selectedLlm?.config_fields.some((f) => f.key === 'api_key') ?? false;
  const hasSavedLLMKey = secretsList.some((s) => s.key === 'llm-api-key');
  const hasSavedEmbeddingKey = secretsList.some((s) => s.key === 'embedding-api-key');
  const embProviderMeta = embeddingProviders.find((p) => p.id === embedding.provider);
  const embNeedsKey = embProviderMeta?.config_fields.some(
    (f) => f.type === 'credential' || f.key === 'api_key',
  ) ?? false;

  const isValid = Boolean(
    llm.provider && llm.config['model'] &&
    (!llmNeedsApiKey || llm.apiKey || hasSavedLLMKey) &&
    embedding.provider && embedding.model &&
    (!embNeedsKey || embedding.apiKey || hasSavedEmbeddingKey),
  );

  const refreshLiveModels = async () => {
    if (!llm.provider) return;
    const reqId = ++liveReqIdRef.current;
    setAiLoading(true);
    setLiveError(null);
    try {
      // Prefer the in-flight body-config endpoint when the user has
      // typed a key but not yet saved, or has changed the provider away
      // from what's persisted. The project endpoint reads only saved
      // state and would 400 with "no llm provider configured" in that
      // window. Fall back to the project endpoint once provider + key
      // are persisted (so cloud providers using ambient credentials —
      // bedrock, vertex — work without a fake key in the form).
      const persistedMatch =
        project?.llm?.provider === llm.provider && (hasSavedLLMKey || !llmNeedsApiKey);
      const inflightCfg: Record<string, string> = { ...llm.config };
      if (llm.apiKey) inflightCfg.api_key = llm.apiKey;
      const resp = persistedMatch && !llm.apiKey
        ? await api.listLiveLLMModelsForProject(projectId)
        : await api.listLiveLLMModels(llm.provider, inflightCfg);
      if (reqId !== liveReqIdRef.current) return;
      setLiveModels(resp.models);
      if (resp.live_error) setLiveError(resp.live_error);
      // Always advance to model phase after a successful (or partially
      // successful) refresh — the user may have come from the
      // credentials phase via Load models.
      setAiPhase('model');
    } catch (e: unknown) {
      if (reqId !== liveReqIdRef.current) return;
      setLiveError((e as Error).message);
      setAiPhase('model');
    } finally {
      if (reqId === liveReqIdRef.current) setAiLoading(false);
    }
  };

  const handleSave = async () => {
    if (!project) return;
    setSaving(true);
    try {
      const saved = await api.updateProject(projectId, {
        llm: {
          provider: llm.provider,
          model: llm.config['model'] || '',
          config: Object.fromEntries(
            Object.entries(llm.config).filter(([k]) => k !== 'model' && k !== 'api_key'),
          ),
        },
        embedding: { provider: embedding.provider, model: embedding.model },
      });
      if (llm.apiKey) {
        await api.setSecret(projectId, 'llm-api-key', llm.apiKey);
        setLlm((prev) => ({ ...prev, apiKey: '' }));
      }
      if (embedding.apiKey) {
        await api.setSecret(projectId, 'embedding-api-key', embedding.apiKey);
        setEmbedding((prev) => ({ ...prev, apiKey: '' }));
      }
      const updated = await api.listSecrets(projectId);
      setSecretsList(updated || []);
      notifications.show({ title: 'Saved', message: 'Provider configuration updated', color: 'green' });
      setProject(saved);
      onSaved?.(saved);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <Loader />;
  if (error) return <Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert>;
  if (!project) return <Text>Project not found</Text>;

  const header = variant === 'page' ? (
    <Stack gap={2} mb="sm">
      <Title order={4}>AI &amp; Embedding Providers</Title>
      <Text size="xs" c="dimmed">The LLM the agent calls during analysis and the embedding model that backs schema indexing.</Text>
    </Stack>
  ) : null;

  return (
    <PanelSection>
      {header}

      <Title order={5}>AI Provider</Title>

      {hasSavedLLMKey && llmNeedsApiKey && (
        <div style={{ borderRadius: 'var(--db-radius)', background: 'var(--db-bg-muted)', padding: 8 }}>
          <Group gap="xs">
            <IconShieldCheck size={14} color="var(--db-green-text)" />
            <Text size="xs" fw={500}>API Key saved</Text>
            <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>
              {secretsList.find((s) => s.key === 'llm-api-key')?.masked}
            </Text>
          </Group>
        </div>
      )}

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
        onLoadModels={refreshLiveModels}
        hasSavedApiKey={hasSavedLLMKey}
      />

      <Title order={5} mt="md">Embedding Provider</Title>
      <Text size="xs" c="dimmed" mb="xs">
        Required for schema indexing and semantic search.
        {hasSavedEmbeddingKey ? (
          <> Current key: <b>{secretsList.find(s => s.key === 'embedding-api-key')?.masked}</b>. Leave the key field blank to keep it.</>
        ) : null}
      </Text>
      <EmbeddingEditor
        providers={embeddingProviders}
        value={embedding}
        onChange={(next) => setEmbedding(next)}
        startInModelPhase={!!project.embedding?.provider}
      />

      <Group justify="flex-end" mt="sm">
        <Button onClick={handleSave} loading={saving} disabled={!isValid}>
          {variant === 'wizard' ? 'Save and continue' : 'Save providers'}
        </Button>
      </Group>
    </PanelSection>
  );
}
