'use client';

import { Alert, Button, Collapse, Group, Select, Stack, Text, TextInput } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { useState } from 'react';
import { DynamicField as CatalogAwareField, LiveModelCombobox, modelWireIsKnown } from '@/components/common/LLMModelField';
import { LiveModel, ProviderMeta } from '@/lib/api';
import { buildDefaults } from './WarehouseFormFields';

export interface LLMFormState {
  provider: string;
  config: Record<string, string>;
  apiKey: string;
}

export function emptyLLMFormState(): LLMFormState {
  return { provider: '', config: {}, apiKey: '' };
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
  const needsApiKey = selected?.config_fields.some((f) => f.key === 'api_key') ?? false;
  const [showAdvanced, setShowAdvanced] = useState(false);

  const setProvider = (id: string) => {
    const prov = providers.find((p) => p.id === id);
    onChange({
      provider: id,
      config: prov ? buildDefaults(prov.config_fields) : {},
      apiKey: '',
    });
    onPhaseChange('credentials');
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
            .filter((f) => f.key !== 'api_key' && f.key !== 'model' && f.key !== 'wire_override')
            .map((field) => (
              <CatalogAwareField
                key={field.key}
                field={field}
                providerMeta={selected}
                value={value.config[field.key] || ''}
                onChange={(val) => setConfigField(field.key, val)}
              />
            ))}

          {needsApiKey && (
            <TextInput
              label={hasSavedApiKey ? 'Update API Key' : 'API Key'}
              required={!hasSavedApiKey}
              type="password"
              placeholder={selected?.config_fields.find((f) => f.key === 'api_key')?.placeholder || 'Enter API key'}
              value={value.apiKey}
              onChange={(e) => onChange({ ...value, apiKey: e.target.value })}
              description={hasSavedApiKey
                ? 'Stored encrypted. Leave empty to keep current. Used now only to refresh the model list.'
                : 'Stored encrypted. Used now only to load the model list.'}
            />
          )}

          {!needsApiKey && (
            <Text size="xs" c="dimmed">
              This provider uses cloud credentials (IAM / ADC). No API key needed.
            </Text>
          )}

          <Button
            onClick={onLoadModels}
            loading={loading}
            disabled={!value.provider || (needsApiKey && !value.apiKey && !hasSavedApiKey)}
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
