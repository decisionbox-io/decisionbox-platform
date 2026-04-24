'use client';

/**
 * Editor for the per-project Blurb LLM (PLAN-SCHEMA-RETRIEVAL.md §6.2).
 * Wraps the shared <ProviderCredentialsPhase> with the LLM-specific
 * model picker (LiveModelCombobox with wire/price/lifecycle metadata).
 *
 * A "Use analysis LLM" switch gates the whole thing — when off, we
 * send no `blurb_llm` on the project and the agent falls back to the
 * analysis LLM + its key, which is the common case for users who
 * don't care about blurb-cost optimisation yet.
 */

import { useState } from 'react';
import { Stack, Switch } from '@mantine/core';
import { api, LiveModel, ProviderMeta } from '@/lib/api';
import { LiveModelCombobox } from '@/components/common/LLMModelField';
import { ProviderCredentialsPhase, CredentialsPhaseValue, emptyCredentials } from './ProviderCredentialsPhase';

export interface BlurbLLMState {
  /** false → fall back to analysis LLM (no blurb_llm sent to server). */
  enabled: boolean;
  provider: string;
  model: string;
  config: Record<string, string>;
  apiKey: string;
}

export function emptyBlurbLLMState(): BlurbLLMState {
  return { enabled: false, provider: '', model: '', config: {}, apiKey: '' };
}

interface Props {
  llmProviders: ProviderMeta[];
  value: BlurbLLMState;
  onChange: (next: BlurbLLMState) => void;
  footer?: React.ReactNode;
  /**
   * Settings page ships the switch already-on (project already has a
   * blurb_llm) and skips the Load-models click by jumping straight to
   * the model phase.
   */
  startInModelPhase?: boolean;
}

export function BlurbLLMEditor({ llmProviders, value, onChange, footer, startInModelPhase }: Props) {
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);

  const credentials: CredentialsPhaseValue = value.enabled
    ? { provider: value.provider, config: value.config, apiKey: value.apiKey }
    : emptyCredentials();

  const selected = llmProviders.find((p) => p.id === value.provider) || null;

  const setEnabled = (en: boolean) => {
    if (!en) {
      onChange(emptyBlurbLLMState());
      setLiveModels(null);
      return;
    }
    // Turning the switch on preselects the first provider so the user
    // sees the credentials phase populated instead of a blank form.
    const first = llmProviders.find((p) => p.id === 'bedrock') || llmProviders[0];
    if (!first) return;
    onChange({
      enabled: true,
      provider: first.id,
      model: '',
      config: buildDefaults(first),
      apiKey: '',
    });
  };

  const applyCredentials = (next: CredentialsPhaseValue) => {
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
    <Stack gap="sm">
      <Switch
        label="Use a separate model for schema-index blurbs"
        description="When off, indexing reuses the analysis LLM + its API key. Turn on to pick a cheaper/faster model for the per-table descriptions the retriever indexes (e.g. Bedrock Qwen3-32B or gpt-4.1-nano)."
        checked={value.enabled}
        onChange={(e) => setEnabled(e.currentTarget.checked)}
      />

      {value.enabled && (
        <ProviderCredentialsPhase<ProviderMeta>
          providers={llmProviders}
          label="Blurb LLM Provider"
          value={credentials}
          onChange={applyCredentials}
          phaseOverride={startInModelPhase ? 'model' : undefined}
          onLoad={async (cfg) => {
            try {
              const resp = await api.listLiveLLMModels(value.provider, cfg);
              setLiveModels(resp.models);
              return { ok: true, liveError: resp.live_error };
            } catch (e: unknown) {
              setLiveModels(null);
              return { ok: true, liveError: e instanceof Error ? e.message : String(e) };
            }
          }}
        >
          <LiveModelCombobox
            providerMeta={selected}
            liveModels={liveModels}
            value={value.model}
            onChange={(val) => onChange({ ...value, model: val })}
          />
          {footer}
        </ProviderCredentialsPhase>
      )}
    </Stack>
  );
}

function buildDefaults(provider: ProviderMeta): Record<string, string> {
  const defaults: Record<string, string> = {};
  for (const f of provider.config_fields) {
    if (f.default) defaults[f.key] = f.default;
  }
  return defaults;
}
