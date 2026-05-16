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
  authMethod: string;
  model: string;
  config: Record<string, string>;
  apiKey: string;
}

export function emptyBlurbLLMState(): BlurbLLMState {
  return { enabled: false, provider: '', authMethod: '', model: '', config: {}, apiKey: '' };
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
  /**
   * When set, the Load-models click prefers the per-project endpoint
   * (which reads the stored blurb-llm-credentials secret server-side)
   * so the user doesn't have to re-enter the API key on Settings.
   * Falls back to the in-flight credentials endpoint when the user
   * has just typed a new key, switched provider, or there's no
   * projectId (new-project / packgen flows).
   */
  projectId?: string;
  /** Provider the project currently has saved for the blurb slot. The
   * per-project endpoint is only safe when the form provider still
   * matches what's persisted — otherwise the server reads the wrong
   * credential. */
  savedProvider?: string;
}

export function BlurbLLMEditor({ llmProviders, value, onChange, footer, startInModelPhase, projectId, savedProvider }: Props) {
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);

  const credentials: CredentialsPhaseValue = value.enabled
    ? { provider: value.provider, authMethod: value.authMethod, config: value.config, apiKey: value.apiKey }
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
    const methods = first.auth_methods ?? [];
    onChange({
      enabled: true,
      provider: first.id,
      authMethod: methods.length === 1 ? methods[0].id : '',
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
      authMethod: next.authMethod,
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
              // Prefer the per-project endpoint when:
              //   - we have a projectId (we're on Settings, not new-project),
              //   - the form still points at the same provider the project
              //     has saved (otherwise the server reads the wrong secret),
              //   - the user has NOT just typed a new key in the form
              //     (in that case we want to validate THAT key, not the
              //     persisted one).
              // The server-side handler will read project.blurb_llm + the
              // blurb-llm-credentials secret, falling back to the analysis
              // LLM slot if no blurb override is configured.
              const useProjectEndpoint =
                !!projectId &&
                !cfg.credentials_json &&
                savedProvider === value.provider;
              const resp = useProjectEndpoint
                ? await api.listLiveLLMModelsForProject(projectId!, 'blurb_llm')
                : await api.listLiveLLMModels(value.provider, cfg);
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
