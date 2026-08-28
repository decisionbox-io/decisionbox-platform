'use client';

import { Alert, Button, Collapse, Group, Select, Stack, Text, Textarea, TextInput } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { useRef, useState } from 'react';
import { DynamicField as CatalogAwareField, LiveModelCombobox, modelWireIsKnown } from '@/components/common/LLMModelField';
import { LiveModel, ProviderMeta } from '@/lib/api';
import { buildDefaults } from './WarehouseFormFields';

export interface LLMFormState {
  provider: string;
  /** Selected auth method ID. Empty when the provider declares no auth
   *  methods (Ollama) or when none is selected yet. */
  authMethod: string;
  config: Record<string, string>;
  /** Credential value entered for the selected auth method's credential
   *  field (api_key for direct providers; AKID:secret for AWS access
   *  keys; SA JSON for GCP sa_key). The name is kept as "apiKey" for
   *  state-shape stability across callers. */
  apiKey: string;
}

export function emptyLLMFormState(): LLMFormState {
  return { provider: '', authMethod: '', config: {}, apiKey: '' };
}

export type AIPhase = 'credentials' | 'model';

interface Props {
  providers: ProviderMeta[];
  value: LLMFormState;
  onChange: (next: LLMFormState) => void;
  /** Phase controls whether the credential fields or the model picker is
   *  shown. The parent owns this state because the Next-button gating
   *  needs to read it. */
  phase: AIPhase;
  onPhaseChange: (next: AIPhase) => void;
  liveModels: LiveModel[] | null;
  liveError: string | null;
  loading: boolean;
  /** Triggers a live-model fetch using the current `value.provider` +
   *  `value.config` + `value.apiKey`. The parent implements the actual
   *  api.listLiveLLMModels call so it can pass projectId or other context
   *  that this component does not need to know about. */
  onLoadModels: () => void;
  /** Settings/wizard variants pass `true` when an API key has already
   *  been persisted; the input then asks for a new key (optional) instead
   *  of treating it as required. */
  hasSavedApiKey?: boolean;
}

// LLMFormFields renders the LLM provider selector and the credentials →
// model two-phase form. It is fully controlled — the parent owns
// `value`, `phase`, `liveModels`, and `loading`. The phase split mirrors
// the new-project wizard's stable UX:
//   - phase 'credentials': pick provider, fill cloud-specific config
//     fields, enter API key; "Load models" advances to phase 'model'.
//   - phase 'model': render LiveModelCombobox + optional wire_override
//     (advanced) + Refresh button.
export function LLMFormFields({
  providers, value, onChange, phase, onPhaseChange,
  liveModels, liveError, loading, onLoadModels, hasSavedApiKey,
}: Props) {
  const selected = providers.find((p) => p.id === value.provider) || null;
  const authMethods = selected?.auth_methods ?? [];
  const selectedMethod = authMethods.find((m) => m.id === value.authMethod);
  const credentialField = (selectedMethod?.fields ?? []).find((f) => f.type === 'credential');
  const nonCredentialAuthFields = (selectedMethod?.fields ?? []).filter((f) => f.type !== 'credential');
  const needsCredential = Boolean(credentialField);
  const [showAdvanced, setShowAdvanced] = useState(false);

  // A user-deployed endpoint (e.g. vertex-ai endpoint_id) serves its own
  // model, so the model picker and the "Load models" step are skipped —
  // the provider forwards an empty model and the endpoint resolves it.
  // The credential fields stay visible so the endpoint ID is editable.
  const usingEndpoint = Boolean(value.config['endpoint_id']?.trim());

  const setProvider = (id: string) => {
    const prov = providers.find((p) => p.id === id);
    const methods = prov?.auth_methods ?? [];
    onChange({
      provider: id,
      authMethod: methods.length === 1 ? methods[0].id : '',
      config: prov ? buildDefaults(prov.config_fields) : {},
      apiKey: '',
    });
    onPhaseChange('credentials');
  };

  const setAuthMethod = (id: string) => {
    onChange({ ...value, authMethod: id, apiKey: '' });
  };

  const setConfigField = (key: string, val: string) => {
    const config = { ...value.config, [key]: val };
    // Entering an endpoint ID drops any wire_override: an endpoint always
    // uses the OpenAI chat-completions wire, and the field is hidden in
    // endpoint mode, so a stale value would otherwise be persisted and
    // rejected by the provider at construction time.
    if (key === 'endpoint_id' && val.trim()) {
      delete config['wire_override'];
    }
    onChange({ ...value, config });
  };

  // autofilledRef remembers the exact values handleModelSelect last prefilled,
  // so switching models refreshes a still-auto-filled field with the new
  // model's detected value while preserving a value the operator actually typed.
  const autofilledRef = useRef<{ max_input_tokens?: string; max_output_tokens?: string }>({});

  // liveDetectedLimit returns a model's token limit ONLY when it was genuinely
  // reported by live detection (source live/both), never a catalog-only value.
  // Writing a catalog number into the override field would pin it as a
  // top-priority manual override that outranks live detection + self-calibration.
  const liveDetectedLimit = (m: LiveModel | undefined, key: 'max_input_tokens' | 'max_output_tokens'): number | undefined => {
    if (!m || (m.source !== 'live' && m.source !== 'both')) return undefined;
    const v = key === 'max_input_tokens' ? m.max_input_tokens : m.max_output_tokens;
    return (v ?? 0) > 0 ? v : undefined;
  };

  // handleModelSelect sets the model and prefills the context-window / output
  // fields from the selected model's *live-detected* values (LiteLLM
  // /model/info, Ollama /api/show, vLLM max_model_len) so the operator sees real
  // numbers instead of typing a window they may not know. A field is refreshed
  // when it is empty or still holds an auto-filled value — recognised either via
  // autofilledRef (same session) or by matching the previously-selected model's
  // live-detected value (robust across a form remount, where the ref is empty).
  // A value the operator typed themselves is preserved. Fields stay editable.
  const handleModelSelect = (val: string) => {
    const config = { ...value.config, model: val };
    const prevLive = (liveModels ?? []).find((m) => m.id === value.config.model);
    const live = (liveModels ?? []).find((m) => m.id === val);
    const next: typeof autofilledRef.current = {};
    (['max_input_tokens', 'max_output_tokens'] as const).forEach((key) => {
      // Only touch fields the provider actually declares. Providers that budget
      // differently (Ollama uses num_ctx, not max_input_tokens) don't render
      // these inputs, and the save path persists every config key — writing an
      // auto-filled value here would silently become a top-priority override
      // the operator can neither see nor clear.
      if (!selected?.config_fields.some((f) => f.key === key)) {
        return;
      }
      const detected = liveDetectedLimit(live, key);
      const prevDetected = liveDetectedLimit(prevLive, key);
      const current = config[key]?.trim() ?? '';
      const wasAutofilled =
        current !== '' &&
        (current === autofilledRef.current[key] ||
          (prevDetected !== undefined && current === String(prevDetected)));
      // Only overwrite an empty field or one that was auto-filled — never a
      // value the operator typed.
      if (current === '' || wasAutofilled) {
        if (detected !== undefined) {
          config[key] = String(detected);
          next[key] = String(detected);
        } else if (wasAutofilled) {
          // New model reports nothing — clear the stale auto-filled value rather
          // than persist the prior model's window as an override for this one.
          delete config[key];
        }
      }
    });
    autofilledRef.current = next;
    onChange({ ...value, config });
  };

  return (
    <Stack>
      <Select
        label="LLM Provider"
        required
        placeholder="Select LLM provider"
        data={providers.map((p) => ({ value: p.id, label: p.name }))}
        value={value.provider}
        onChange={(v) => setProvider(v || '')}
      />
      {selected?.description && <Text size="xs" c="dimmed">{selected.description}</Text>}

      {(phase === 'credentials' || usingEndpoint) && (
        <>
          {selected?.config_fields
            .filter((f) => f.key !== 'model' && f.key !== 'wire_override' && f.key !== 'max_input_tokens' && f.key !== 'max_output_tokens')
            .map((field) => (
              <CatalogAwareField
                key={field.key}
                field={field}
                providerMeta={selected}
                value={value.config[field.key] || ''}
                onChange={(val) => setConfigField(field.key, val)}
              />
            ))}

          {authMethods.length > 1 && (
            <Select
              label="Authentication method"
              required
              data={authMethods.map((m) => ({ value: m.id, label: m.name }))}
              value={value.authMethod || null}
              onChange={(v) => v && setAuthMethod(v)}
            />
          )}
          {selectedMethod?.description && (
            <Text size="xs" c="dimmed">{selectedMethod.description}</Text>
          )}

          {nonCredentialAuthFields.map((field) => (
            <CatalogAwareField
              key={field.key}
              field={field}
              providerMeta={selected}
              value={value.config[field.key] || ''}
              onChange={(val) => setConfigField(field.key, val)}
            />
          ))}

          {credentialField && (
            <Textarea
              label={hasSavedApiKey ? 'Update credentials' : credentialField.label || 'Credentials'}
              required={!hasSavedApiKey}
              placeholder={credentialField.placeholder || `Enter ${(credentialField.label || 'credentials').toLowerCase()}`}
              value={value.apiKey}
              onChange={(e) => onChange({ ...value, apiKey: e.target.value })}
              description={hasSavedApiKey
                ? 'Stored encrypted. Leave empty to keep current. Used now only to refresh the model list.'
                : 'Stored encrypted. Used now only to load the model list.'}
              minRows={3}
              autosize
              styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }}
            />
          )}

          {value.authMethod && !credentialField && (
            <Text size="xs" c="dimmed">
              This auth method uses ambient cloud credentials (IAM role / ADC) — no credentials needed in the dashboard.
            </Text>
          )}

          {authMethods.length === 0 && (
            <Text size="xs" c="dimmed">
              This provider requires no credentials.
            </Text>
          )}

          {usingEndpoint ? (
            <Text size="xs" c="dimmed">
              This endpoint serves its own deployed model — no model selection needed.
            </Text>
          ) : (
            <Button
              onClick={onLoadModels}
              loading={loading}
              disabled={
                !value.provider ||
                (authMethods.length > 0 && !value.authMethod) ||
                (needsCredential && !value.apiKey && !hasSavedApiKey)
              }
            >
              Load models
            </Button>
          )}
        </>
      )}

      {phase === 'model' && !usingEndpoint && (
        <>
          {liveError && (
            <Alert color="orange" icon={<IconAlertCircle size={16} />} title="Could not fetch live model list">
              {liveError} — showing catalog models instead.
            </Alert>
          )}

          <LiveModelCombobox
            providerMeta={selected}
            liveModels={liveModels}
            value={value.config['model'] || ''}
            onChange={handleModelSelect}
          />

          {/* Manual token overrides (context window + max output). Prefilled
              from the selected model's auto-detected values by handleModelSelect;
              the effective value is also shown as a placeholder when a field is
              left blank. */}
          {(['max_input_tokens', 'max_output_tokens'] as const).map((key) => {
            const f = selected?.config_fields.find((cf) => cf.key === key);
            if (!f) return null;
            const model = value.config['model'] || '';
            const live = (liveModels ?? []).find((m) => m.id === model);
            const def = key === 'max_input_tokens' ? 131072 : 64000;
            const known = (key === 'max_input_tokens' ? live?.max_input_tokens : live?.max_output_tokens) ?? 0;
            const effective = known > 0 ? known : def;
            return (
              <TextInput
                key={key}
                label={f.label}
                description={f.description}
                placeholder={`${effective.toLocaleString()} (current default — leave blank to use it)`}
                value={value.config[key] || ''}
                onChange={(e) => setConfigField(key, e.currentTarget.value)}
              />
            );
          })}

          {(() => {
            const wireField = selected?.config_fields.find((f) => f.key === 'wire_override');
            if (!wireField) return null;
            const wireKnown = modelWireIsKnown(liveModels, selected, value.config['model'] || '');
            const renderField = (
              <CatalogAwareField
                field={wireField}
                providerMeta={selected}
                value={value.config[wireField.key] || ''}
                onChange={(val) => setConfigField(wireField.key, val)}
              />
            );
            if (!wireKnown) return renderField;
            return (
              <>
                <Button
                  variant="subtle"
                  size="xs"
                  onClick={() => setShowAdvanced((v) => !v)}
                  style={{ alignSelf: 'flex-start' }}
                >
                  {showAdvanced ? 'Hide advanced settings' : 'Advanced settings'}
                </Button>
                <Collapse in={showAdvanced}>{renderField}</Collapse>
              </>
            );
          })()}

          <Group>
            <Button variant="default" onClick={() => onPhaseChange('credentials')}>Back to credentials</Button>
            <Button variant="subtle" onClick={onLoadModels} loading={loading}>Refresh model list</Button>
          </Group>
        </>
      )}
    </Stack>
  );
}
