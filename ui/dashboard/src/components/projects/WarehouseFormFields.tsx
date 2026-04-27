'use client';

import { Group, Select, Stack, Text, TextInput, Textarea } from '@mantine/core';
import { ConfigField, ProviderMeta } from '@/lib/api';

export interface WarehouseFormState {
  provider: string;
  config: Record<string, string>;
  authMethod: string;
  credential: string;
  filterField: string;
  filterValue: string;
}

export function emptyWarehouseFormState(): WarehouseFormState {
  return { provider: '', config: {}, authMethod: '', credential: '', filterField: '', filterValue: '' };
}

export function buildDefaults(fields: ConfigField[]): Record<string, string> {
  const defaults: Record<string, string> = {};
  for (const f of fields) {
    if (f.default) defaults[f.key] = f.default;
  }
  return defaults;
}

export function DynamicField({ field, value, onChange }: { field: ConfigField; value: string; onChange: (v: string) => void }) {
  if (field.type === 'textarea') {
    return (
      <Textarea
        label={field.label}
        required={field.required}
        placeholder={field.placeholder || field.default}
        description={field.description}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        minRows={6}
        autosize
        styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }}
      />
    );
  }
  return (
    <TextInput
      label={field.label}
      required={field.required}
      placeholder={field.placeholder || field.default}
      description={field.description}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

interface Props {
  providers: ProviderMeta[];
  value: WarehouseFormState;
  onChange: (next: WarehouseFormState) => void;
  /** When true, the credential textarea label says "Update <X>" and a hint
   *  about leaving the field empty to keep the saved value is appended.
   *  Used by the settings/wizard variants where a credential may already
   *  be persisted. */
  hasSavedCredential?: boolean;
}

// WarehouseFormFields renders the warehouse provider selector and all
// provider-driven fields (config, auth method, auth fields, credential,
// filter). It is fully controlled — the parent owns `value` and reacts
// to `onChange` events. Used by:
//   - projects/new/page.tsx       (Step 2 of the new-project wizard)
//   - WarehouseConfigPanel.tsx    (settings tab + pack-gen wizard wrapper)
export function WarehouseFormFields({ providers, value, onChange, hasSavedCredential }: Props) {
  const selected = providers.find((p) => p.id === value.provider);
  const authMethods = selected?.auth_methods || [];
  const selectedAuth = authMethods.find((m) => m.id === value.authMethod);
  const authFields = selectedAuth?.fields || [];
  const authCredField = authFields.find((f) => f.type === 'credential');
  const authConfigFields = authFields.filter((f) => f.type !== 'credential');

  const setProvider = (id: string) => {
    const prov = providers.find((p) => p.id === id);
    onChange({
      ...value,
      provider: id,
      config: prov ? buildDefaults(prov.config_fields) : {},
      authMethod: prov?.auth_methods?.length === 1 ? prov.auth_methods[0].id : '',
      credential: '',
    });
  };

  const setConfigField = (key: string, val: string) => {
    onChange({ ...value, config: { ...value.config, [key]: val } });
  };

  return (
    <Stack>
      <Select
        label="Warehouse Provider"
        required
        placeholder="Select warehouse"
        data={providers.map((p) => ({ value: p.id, label: p.name }))}
        value={value.provider}
        onChange={(v) => setProvider(v || '')}
      />
      {selected?.description && <Text size="xs" c="dimmed">{selected.description}</Text>}

      {selected?.config_fields.map((field) => (
        <DynamicField
          key={field.key}
          field={field}
          value={value.config[field.key] || ''}
          onChange={(val) => setConfigField(field.key, val)}
        />
      ))}

      {authMethods.length > 0 && (
        <Select
          key={`auth-${value.provider}`}
          label="Authentication"
          required
          placeholder="Select auth method"
          data={authMethods.map((m) => ({ value: m.id, label: m.name }))}
          value={value.authMethod}
          onChange={(v) => onChange({ ...value, authMethod: v || '', credential: '' })}
        />
      )}

      {selectedAuth?.description && <Text size="xs" c="dimmed">{selectedAuth.description}</Text>}

      {authConfigFields.map((field) => (
        <DynamicField
          key={field.key}
          field={field}
          value={value.config[field.key] || ''}
          onChange={(val) => setConfigField(field.key, val)}
        />
      ))}

      {authCredField && (
        <Textarea
          label={hasSavedCredential ? `Update ${authCredField.label}` : authCredField.label}
          required={authCredField.required && !hasSavedCredential}
          placeholder={authCredField.placeholder || `Enter ${authCredField.label.toLowerCase()}`}
          description={(authCredField.description || '') + (hasSavedCredential ? ' Stored encrypted. Leave empty to keep current.' : ' Stored encrypted.')}
          value={value.credential}
          onChange={(e) => onChange({ ...value, credential: e.target.value })}
          minRows={3}
          autosize
          styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }}
        />
      )}

      <Text size="sm" fw={600} mt="sm">Filter (optional)</Text>
      <Text size="xs" c="dimmed">For shared datasets. Leave empty if the entire dataset is yours.</Text>
      <Group grow>
        <TextInput
          label="Filter Field"
          placeholder="e.g. app_id"
          value={value.filterField}
          onChange={(e) => onChange({ ...value, filterField: e.target.value })}
        />
        <TextInput
          label="Filter Value"
          placeholder="e.g. my-app-123"
          value={value.filterValue}
          onChange={(e) => onChange({ ...value, filterValue: e.target.value })}
        />
      </Group>
    </Stack>
  );
}
