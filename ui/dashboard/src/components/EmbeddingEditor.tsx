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
  model: string;
  config: Record<string, string>;
  apiKey: string;
}

export function emptyEmbeddingState(): EmbeddingState {
  return { provider: '', model: '', config: {}, apiKey: '' };
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
}

export function EmbeddingEditor({ providers, value, onChange, required, startInModelPhase }: Props) {
  const [liveModels, setLiveModels] = useState<EmbeddingLiveModel[] | null>(null);

  const selectedProvider = providers.find((p) => p.id === value.provider) || null;

  // Derived credentials view over the parent-owned state. Keeps the
  // parent as the single source of truth while letting the shared
  // phase treat its own value as self-contained.
  const credentials: CredentialsPhaseValue = value.provider
    ? { provider: value.provider, config: value.config, apiKey: value.apiKey }
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
          const resp = await api.listLiveEmbeddingModels(value.provider, cfg);
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
