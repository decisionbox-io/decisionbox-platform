'use client';

import { Alert, Button, Collapse, Group, Select, Stack, Text, Textarea } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { useState } from 'react';
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
    onChange({ ...value, config: { ...value.config, [key]: val } });
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

      {phase === 'credentials' && (
        <>
          {selected?.config_fields
            .filter((f) => f.key !== 'model' && f.key !== 'wire_override')
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
        </>
      )}

      {phase === 'model' && (
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
            onChange={(val) => setConfigField('model', val)}
          />

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
