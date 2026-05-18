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
import { TestConnectionButton } from './WarehouseConfigPanel';

// autoSingleAuthMethod returns the only auth method when a provider has
// exactly one, or empty string otherwise. Used to pre-select api_key
// for direct providers (Claude/OpenAI/Voyage) so the user doesn't have
// to click a dropdown with a single option.
function autoSingleAuthMethod(meta?: { auth_methods?: { id: string }[] }): string {
  const methods = meta?.auth_methods ?? [];
  return methods.length === 1 ? methods[0].id : '';
}

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
// in one click via PUT /projects/{id}; secret keys (llm-credentials,
// embedding-credentials) rotate only when the user enters a new value.
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
          authMethod: (proj.llm.config?.['auth_method'] as string) || autoSingleAuthMethod(provMeta),
          config: {
            ...fieldDefaults,
            ...(proj.llm.config || {}),
            ...(proj.llm.model ? { model: proj.llm.model } : {}),
          },
          apiKey: '',
        });
        // Use the freshly-fetched embProvs (local var) — embeddingProviders
        // state was just set on line 93 but the value here is still the
        // initial empty array until React re-renders.
        const embProvMeta = (embProvs || []).find((p) => p.id === proj.embedding?.provider);
        setEmbedding({
          provider: proj.embedding?.provider || '',
          authMethod: (proj.embedding?.config?.['auth_method'] as string) || autoSingleAuthMethod(embProvMeta),
          model: proj.embedding?.model || '',
          config: proj.embedding?.config || {},
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
  const llmAuthMethod = (selectedLlm?.auth_methods ?? []).find((m) => m.id === llm.authMethod);
  const llmNeedsCredential = (llmAuthMethod?.fields ?? []).some((f) => f.type === 'credential');
  const hasSavedLLMKey = secretsList.some((s) => s.key === 'llm-credentials');
  const hasSavedEmbeddingKey = secretsList.some((s) => s.key === 'embedding-credentials');
  const embProviderMeta = embeddingProviders.find((p) => p.id === embedding.provider);
  const embAuthMethod = (embProviderMeta?.auth_methods ?? []).find((m) => m.id === embedding.authMethod);
  const embNeedsCredential = (embAuthMethod?.fields ?? []).some((f) => f.type === 'credential');

  const isValid = Boolean(
    llm.provider && llm.config['model'] &&
    (!llmNeedsCredential || llm.apiKey || hasSavedLLMKey) &&
    embedding.provider && embedding.model &&
    (!embNeedsCredential || embedding.apiKey || hasSavedEmbeddingKey),
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
        project?.llm?.provider === llm.provider && (hasSavedLLMKey || !llmNeedsCredential);
      const inflightCfg: Record<string, string> = { ...llm.config };
      if (llm.authMethod) inflightCfg.auth_method = llm.authMethod;
      // Derive the credential field key from the selected auth method
      // instead of hardcoding "credentials_json". Every provider today
      // happens to use that key, but a future auth method that declares
      // a different credential field key (e.g. "token") would silently
      // drop the in-flight credential here. Mirrors ProviderCredentialsPhase.
      if (llm.apiKey) {
        const credField = (llmAuthMethod?.fields ?? []).find((f) => f.type === 'credential');
        if (credField) inflightCfg[credField.key] = llm.apiKey;
      }
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
      const llmConfig: Record<string, string> = Object.fromEntries(
        Object.entries(llm.config).filter(([k]) => k !== 'model'),
      );
      if (llm.authMethod) llmConfig.auth_method = llm.authMethod;
      // Carry every form-state config field for embedding (project_id,
      // location, region, …). The form holds them under
      // embedding.config[key], which is what the agent reads from
      // project.Embedding.Config at init time. Drop "model" because
      // it lives in the top-level Model field, not Config.
      const embConfig: Record<string, string> = Object.fromEntries(
        Object.entries(embedding.config).filter(([k]) => k !== 'model'),
      );
      if (embedding.authMethod) embConfig.auth_method = embedding.authMethod;
      const saved = await api.updateProject(projectId, {
        llm: {
          provider: llm.provider,
          model: llm.config['model'] || '',
          config: llmConfig,
        },
        embedding: { provider: embedding.provider, model: embedding.model, config: embConfig },
      });
      // Parallel writes for symmetry with new-project page; the settings
      // path doesn't auto-trigger indexing so the race is less acute,
      // but a sequential await per secret still wastes ~250ms per
      // round-trip.
      const writes: Promise<unknown>[] = [];
      if (llm.apiKey) writes.push(api.setSecret(projectId, 'llm-credentials', llm.apiKey));
      if (embedding.apiKey) writes.push(api.setSecret(projectId, 'embedding-credentials', embedding.apiKey));
      await Promise.all(writes);
      if (llm.apiKey) setLlm((prev) => ({ ...prev, apiKey: '' }));
      if (embedding.apiKey) setEmbedding((prev) => ({ ...prev, apiKey: '' }));
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

      {hasSavedLLMKey && llmNeedsCredential && (
        <div style={{ borderRadius: 'var(--db-radius)', background: 'var(--db-bg-muted)', padding: 8 }}>
          <Group gap="xs">
            <IconShieldCheck size={14} color="var(--db-green-text)" />
            <Text size="xs" fw={500}>Credentials saved</Text>
            <Text size="xs" c="dimmed" style={{ fontFamily: 'monospace' }}>
              {secretsList.find((s) => s.key === 'llm-credentials')?.masked}
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

      {/* Test button is gated on saved credentials — testing reads the
          stored secret + project config, not in-flight form state. */}
      {hasSavedLLMKey && project.llm?.provider && (
        <TestConnectionButton projectId={projectId} target="llm" />
      )}

      <Title order={5} mt="md">Embedding Provider</Title>
      <Text size="xs" c="dimmed" mb="xs">
        Required for schema indexing and semantic search.
        {hasSavedEmbeddingKey ? (
          <> Current credentials: <b>{secretsList.find(s => s.key === 'embedding-credentials')?.masked}</b>. Leave the credentials field blank to keep them.</>
        ) : null}
      </Text>
      <EmbeddingEditor
        providers={embeddingProviders}
        value={embedding}
        onChange={(next) => setEmbedding(next)}
        startInModelPhase={!!project.embedding?.provider}
        projectId={projectId}
        savedProvider={project.embedding?.provider}
      />

      {hasSavedEmbeddingKey && project.embedding?.provider && (
        <TestConnectionButton projectId={projectId} target="embedding" />
      )}

      <Group justify="flex-end" mt="sm">
        <Button onClick={handleSave} loading={saving} disabled={!isValid}>
          {variant === 'wizard' ? 'Save and continue' : 'Save providers'}
        </Button>
      </Group>
    </PanelSection>
  );
}
