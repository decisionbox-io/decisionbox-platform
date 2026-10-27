'use client';

import { useEffect, useRef, useState } from 'react';
import {
  Alert, Button, Group, Loader, Stack, Text, Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle } from '@tabler/icons-react';
import { EmbeddingEditor, EmbeddingState, emptyEmbeddingState } from '@/components/EmbeddingEditor';
import {
  api, EmbeddingProviderMeta, Project, SecretEntryResponse,
} from '@/lib/api';
import { TestConnectionButton } from './WarehouseConfigPanel';
import { LLMProvidersPanel } from './LLMProvidersPanel';
import { LayerModelsPanel } from './LayerModelsPanel';
import { UseLLMConfig, useLLMConfig } from './useLLMConfig';

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
  /**
   * The per-layer LLM configuration lifecycle, when the HOST owns it.
   *
   * A host that also renders <LayerModelsPanel> — a wizard whose next step is
   * the models — must pass its own instance. Two instances of the hook load
   * independently and neither reloads after the other saves, so the entries
   * written in this step are invisible to the next one, and that step's save
   * writes its stale copy back over them.
   */
  llmConfig?: UseLLMConfig;
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

// ProvidersPanel manages the load + save lifecycle for a project's AI
// providers.
//
// Since per-layer models (#407) the LLM half is two panels — the named
// provider entries and the model each layer uses — driven by the shared
// useLLMConfig hook, which is also what the settings tabs use. The embedding
// half is unchanged: a different registry, a different provider set, and
// dimension/reindex semantics a shared list would get wrong.
//
// The two halves save separately because they ARE separate: embedding writes
// project.embedding, the LLM half writes llm_providers + layer_models and the
// per-entry credentials. One button would hide which of the two failed.
//
// Used by:
//   - settings/page.tsx        (variant="page" — embedding only; the LLM half
//                               has its own tabs there)
//   - the creation wizard and
//     plugin-overlaid wizards  (variant="wizard" — both halves, in order)
export default function ProvidersPanel({
  projectId, variant, onSaved, llmConfig: injectedLLMConfig,
}: ProvidersPanelProps) {
  const [project, setProject] = useState<Project | null>(null);
  const [embeddingProviders, setEmbeddingProviders] = useState<EmbeddingProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [secretsList, setSecretsList] = useState<SecretEntryResponse[]>([]);
  const [embedding, setEmbedding] = useState<EmbeddingState>(emptyEmbeddingState);

  // Created here only when the host does not own one. The hook treats an
  // empty projectId as "nothing to load", so the unused instance costs no
  // request — and calling it unconditionally keeps the hook order stable.
  const ownLLMConfig = useLLMConfig(injectedLLMConfig ? '' : projectId, (saved) => {
    setProject(saved);
    onSaved?.(saved);
  });
  const llmConfig = injectedLLMConfig ?? ownLLMConfig;

  const loadOnce = useRef(false);
  useEffect(() => {
    if (loadOnce.current) return;
    loadOnce.current = true;
    Promise.all([
      api.getProject(projectId),
      api.listEmbeddingProviders(),
    ])
      .then(([proj, embProvs]) => {
        setProject(proj);
        setEmbeddingProviders(embProvs || []);
        // Use the freshly-fetched embProvs (local var) — embeddingProviders
        // state was just set above but the value here is still the initial
        // empty array until React re-renders.
        const embProvMeta = (embProvs || []).find((p) => p.id === proj.embedding?.provider);
        setEmbedding({
          provider: proj.embedding?.provider || '',
          authMethod: (proj.embedding?.config?.['auth_method'] as string) || autoSingleAuthMethod(embProvMeta),
          model: proj.embedding?.model || '',
          config: proj.embedding?.config || {},
          apiKey: '',
        });
        api.listSecrets(projectId).then((s) => setSecretsList(s || [])).catch(() => {});
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [projectId]);

  const hasSavedEmbeddingKey = secretsList.some((s) => s.key === 'embedding-credentials');
  const embProviderMeta = embeddingProviders.find((p) => p.id === embedding.provider);
  const embAuthMethod = (embProviderMeta?.auth_methods ?? []).find((m) => m.id === embedding.authMethod);
  const embNeedsCredential = (embAuthMethod?.fields ?? []).some((f) => f.type === 'credential');

  const embeddingValid = Boolean(
    embedding.provider && embedding.model &&
    (!embNeedsCredential || embedding.apiKey || hasSavedEmbeddingKey),
  );

  const saveEmbedding = async () => {
    if (!project) return;
    setSaving(true);
    try {
      // Carry every form-state config field (project_id, location, region,
      // …). The form holds them under embedding.config[key], which is what
      // the agent reads from project.Embedding.Config at init time. Drop
      // "model" because it lives in the top-level Model field, not Config.
      const embConfig: Record<string, string> = Object.fromEntries(
        Object.entries(embedding.config).filter(([k]) => k !== 'model'),
      );
      if (embedding.authMethod) embConfig.auth_method = embedding.authMethod;
      const saved = await api.updateProject(projectId, {
        embedding: { provider: embedding.provider, model: embedding.model, config: embConfig },
      });
      if (embedding.apiKey) {
        await api.setSecret(projectId, 'embedding-credentials', embedding.apiKey);
        setEmbedding((prev) => ({ ...prev, apiKey: '' }));
      }
      const updated = await api.listSecrets(projectId);
      setSecretsList(updated || []);
      notifications.show({ title: 'Saved', message: 'Embedding configuration updated', color: 'green' });
      setProject(saved);
      onSaved?.(saved);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  if (loading || llmConfig.loading) return <Loader />;
  if (error) return <Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert>;
  if (llmConfig.error) return <Alert color="red" icon={<IconAlertCircle size={16} />}>{llmConfig.error}</Alert>;
  if (!project) return <Text>Project not found</Text>;

  const header = variant === 'page' ? (
    <Stack gap={2} mb="sm">
      <Title order={4}>Embedding Provider</Title>
      <Text size="xs" c="dimmed">The embedding model that backs schema indexing and semantic search.</Text>
    </Stack>
  ) : null;

  return (
    <Stack gap="md">
      {variant === 'wizard' && (
        <PanelSection>
          <LLMProvidersPanel
            projectId={projectId}
            providers={llmConfig.providers}
            value={llmConfig.entryForms}
            onChange={llmConfig.setEntryForms}
            secrets={llmConfig.secrets}
            layersByProviderId={llmConfig.layersByProviderId}
            savedIds={llmConfig.savedIds}
            onModelsDiscovered={llmConfig.reportDiscoveredModels}
          />
          {/*
            * savedEntries/savedGeneration are not optional extras: without
            * them this panel believes every connection is stored, so an
            * edited entry gets the SERVER's answer about the connection it
            * replaced — the previous provider's models, offered against the
            * new one. The host's own models step passes them; so must this.
            */}
          <LayerModelsPanel
            projectId={projectId}
            entries={llmConfig.entries}
            savedEntries={llmConfig.savedEntries}
            savedGeneration={llmConfig.savedGeneration}
            providers={llmConfig.providers}
            value={llmConfig.layerModels}
            onChange={llmConfig.setLayerModels}
            discoveredModels={llmConfig.discoveredModels}
          />
          {llmConfig.validationError && (
            <Alert color="yellow" icon={<IconAlertCircle size={16} />}>{llmConfig.validationError}</Alert>
          )}
          <Group justify="flex-end">
            <Button
              onClick={() => { void llmConfig.save(); }}
              loading={llmConfig.saving}
              disabled={!!llmConfig.validationError}
            >
              Save LLM configuration
            </Button>
          </Group>
        </PanelSection>
      )}

      <PanelSection>
        {header}
        <Text size="xs" c="dimmed" mb="xs">
          Required for schema indexing and semantic search.
          {hasSavedEmbeddingKey ? (
            <> Current credentials: <b>{secretsList.find((s) => s.key === 'embedding-credentials')?.masked}</b>. Leave the credentials field blank to keep them.</>
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
          <Button onClick={saveEmbedding} loading={saving} disabled={!embeddingValid}>
            {variant === 'wizard' ? 'Save and continue' : 'Save embedding'}
          </Button>
        </Group>
      </PanelSection>
    </Stack>
  );
}
