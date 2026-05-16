'use client';

/**
 * Embedding provider + model picker.
 *
 * Composes <ProviderCredentialsPhase> (shared with the LLM editors)
 * and renders an <EmbeddingModelCombobox> inside the phase's render
 * slot once credentials are loaded. Keeps the top half of the UI
 * pixel-identical to the LLM picker, while the model row stays
 * dimension-aware (the one embedding-specific bit that matters).
 */

import { useState } from 'react';
import { api, EmbeddingLiveModel, EmbeddingProviderMeta } from '@/lib/api';
import { ProviderCredentialsPhase, CredentialsPhaseValue, emptyCredentials } from './ProviderCredentialsPhase';
import { EmbeddingModelCombobox } from './EmbeddingModelCombobox';

export interface EmbeddingState {
  provider: string;
  authMethod: string;
  model: string;
  config: Record<string, string>;
  apiKey: string;
}

export function emptyEmbeddingState(): EmbeddingState {
  return { provider: '', authMethod: '', model: '', config: {}, apiKey: '' };
}

interface Props {
  providers: EmbeddingProviderMeta[];
  value: EmbeddingState;
  onChange: (next: EmbeddingState) => void;
  required?: boolean;
  /**
   * When true, the shared phase starts already on "model" so a settings
   * page editing a saved project doesn't force the user through a
   * Load-models click just to see the current selection.
   */
  startInModelPhase?: boolean;
  /**
   * When set, the Load-models click prefers the per-project endpoint so
   * the user doesn't have to re-enter the embedding API key on Settings.
   * Falls back to the in-flight credentials endpoint when the user has
   * just typed a new key, switched provider, or there's no projectId
   * (new-project flow).
   */
  projectId?: string;
  /** Provider the project currently has saved. The per-project endpoint
   * is only safe when the form provider still matches what's persisted —
   * otherwise the server reads the wrong credential. */
  savedProvider?: string;
}

export function EmbeddingEditor({ providers, value, onChange, required, startInModelPhase, projectId, savedProvider }: Props) {
  const [liveModels, setLiveModels] = useState<EmbeddingLiveModel[] | null>(null);

  const selectedProvider = providers.find((p) => p.id === value.provider) || null;

  // Derived credentials view over the parent-owned state. Keeps the
  // parent as the single source of truth while letting the shared
  // phase treat its own value as self-contained.
  const credentials: CredentialsPhaseValue = value.provider
    ? { provider: value.provider, authMethod: value.authMethod, config: value.config, apiKey: value.apiKey }
    : emptyCredentials();

  const applyCredentials = (next: CredentialsPhaseValue) => {
    // Reset the model when the provider flips — different providers
    // never share model IDs in a meaningful way, and keeping a stale
    // `text-embedding-3-large` around when the user switched to
    // Bedrock would be a foot-gun.
    const providerChanged = next.provider !== value.provider;
    onChange({
      ...value,
      provider: next.provider,
      authMethod: next.authMethod,
      config: next.config,
      apiKey: next.apiKey,
      ...(providerChanged ? { model: '' } : {}),
    });
    if (providerChanged) setLiveModels(null);
  };

  return (
    <ProviderCredentialsPhase<EmbeddingProviderMeta>
      providers={providers}
      label="Embedding Provider"
      required={required}
      value={credentials}
      onChange={applyCredentials}
      phaseOverride={startInModelPhase ? 'model' : undefined}
      onLoad={async (cfg) => {
        try {
          // Prefer the per-project endpoint when:
          //   - we have a projectId (Settings, not new-project),
          //   - the form still points at the same provider the project
          //     has saved (otherwise the server reads the wrong secret),
          //   - the user has NOT just typed a new key (in that case we
          //     want to validate THAT key, not the persisted one).
          // The server reads project.embedding.config + the stored
          // embedding-credentials secret. There's only one embedding
          // slot per project so no slot parameter is needed.
          const useProjectEndpoint =
            !!projectId &&
            !cfg.credentials_json &&
            savedProvider === value.provider;
          const resp = useProjectEndpoint
            ? await api.listLiveEmbeddingModelsForProject(projectId!)
            : await api.listLiveEmbeddingModels(value.provider, cfg);
          setLiveModels(resp.models);
          return { ok: true, liveError: resp.live_error };
        } catch (e: unknown) {
          // Fall through to catalog view — the shared phase still
          // advances so users can pick from the shipped models.
          setLiveModels(null);
          return { ok: true, liveError: e instanceof Error ? e.message : String(e) };
        }
      }}
    >
      <EmbeddingModelCombobox
        providerMeta={selectedProvider}
        liveModels={liveModels}
        value={value.model}
        onChange={(val) => onChange({ ...value, model: val })}
      />
    </ProviderCredentialsPhase>
  );
}
