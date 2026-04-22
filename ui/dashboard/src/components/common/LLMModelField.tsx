'use client';

import { Autocomplete, Badge, Group, Select, Stack, Text, TextInput, Textarea } from '@mantine/core';
import type { ConfigField, ConfigOption, LiveModel, ModelInfo, ProviderMeta } from '@/lib/api';

// DynamicField renders one ConfigField from the backend provider meta.
//
// Behaviour:
//   - Key === "model": always a combobox. If the selected provider has a
//     catalog (meta.models), the dropdown lists catalogued IDs with their
//     display name; the user can still type any string for uncatalogued
//     models. Below the input we show a details panel (wire, max tokens,
//     pricing) for the currently selected/typed model when catalogued, or
//     a hint to set Wire override when not.
//   - field.options is non-empty + field.free_text is true: combobox over
//     the provided options.
//   - field.options is non-empty + !field.free_text: strict dropdown.
//   - field.type === "textarea": textarea (monospace, autosize).
//   - otherwise: plain text input.
export function DynamicField({
  field,
  value,
  onChange,
  providerMeta,
}: {
  field: ConfigField;
  value: string;
  onChange: (v: string) => void;
  providerMeta?: ProviderMeta | null;
}) {
  // Model field gets the catalog-backed combobox.
  if (field.key === 'model') {
    return <ModelCombobox field={field} value={value} onChange={onChange} meta={providerMeta ?? null} />;
  }

  // Generic dropdown / combobox from ConfigField.options.
  if (field.options && field.options.length > 0) {
    if (field.free_text) {
      return (
        <Autocomplete
          label={field.label}
          required={field.required}
          description={field.description}
          placeholder={field.placeholder || field.default}
          value={value}
          onChange={onChange}
          data={field.options.map((o: ConfigOption) => ({ value: o.value, label: o.label || o.value }))}
        />
      );
    }
    return (
      <Select
        label={field.label}
        required={field.required}
        description={field.description}
        placeholder={field.placeholder}
        value={value}
        onChange={(v) => onChange(v || '')}
        data={field.options.map((o: ConfigOption) => ({ value: o.value, label: o.label || o.value }))}
        allowDeselect={!field.required}
      />
    );
  }

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

// ModelCombobox renders the "model" ConfigField as a combobox over the
// provider's catalog. Shows catalog details for the selected model or an
// inline hint prompting wire_override if the current value is uncatalogued.
function ModelCombobox({
  field,
  value,
  onChange,
  meta,
}: {
  field: ConfigField;
  value: string;
  onChange: (v: string) => void;
  meta: ProviderMeta | null;
}) {
  const models = meta?.models ?? [];
  const match = models.find((m) => m.id === value);

  if (models.length === 0) {
    // No catalog for this provider — plain text input.
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

  return (
    <Stack gap={4}>
      <Autocomplete
        label={field.label}
        required={field.required}
        description={field.description}
        placeholder={field.placeholder || field.default}
        value={value}
        onChange={onChange}
        limit={50}
        data={models.map((m) => ({ value: m.id, label: `${m.display_name} — ${m.id}` }))}
      />
      <ModelDetailsPanel match={match ?? null} typedValue={value} />
    </Stack>
  );
}

// LiveModelCombobox is the model picker shown in phase 2 of the
// project-create AI step and in the settings AI tab after a refresh.
// When liveModels is non-null we render the merged live + catalog list
// (each row tagged by source). When it's null we fall back to the
// catalog-only list from ProviderMeta.models. Free text is always
// accepted so users can type an uncatalogued / unlisted model ID.
export function LiveModelCombobox({
  providerMeta,
  liveModels,
  value,
  onChange,
}: {
  providerMeta: ProviderMeta | null;
  liveModels: LiveModel[] | null;
  value: string;
  onChange: (v: string) => void;
}) {
  // Build the picker rows from either the live list (which already
  // includes catalog enrichment via the backend merge) or the
  // provider's shipped catalog.
  const rows: LiveModel[] =
    liveModels !== null
      ? liveModels
      : (providerMeta?.models ?? []).map((m) => ({ ...m, source: 'catalog' as const }));

  const match = rows.find((m) => m.id === value) ?? null;

  if (rows.length === 0) {
    return (
      <TextInput
        label="Model"
        required
        placeholder="Enter model ID"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        description="No catalog entries for this provider — type any model ID the upstream accepts."
      />
    );
  }

  return (
    <Stack gap={4}>
      <Autocomplete
        label="Model"
        required
        description="Pick a model from the list or type any model ID."
        placeholder="e.g. claude-opus-4-6, gpt-4o, gemini-2.5-pro"
        value={value}
        onChange={onChange}
        limit={50}
        data={rows.map((m) => ({
          value: m.id,
          label: formatRowLabel(m),
        }))}
      />
      <LiveModelDetails match={match} typedValue={value} />
    </Stack>
  );
}

function formatRowLabel(m: LiveModel): string {
  const display = m.display_name && m.display_name !== m.id ? `${m.display_name} — ` : '';
  const sourceTag = m.source === 'live' ? '  [live-only]' : m.source === 'catalog' ? '  [catalog-only]' : '';
  return `${display}${m.id}${sourceTag}`;
}

function LiveModelDetails({ match, typedValue }: { match: LiveModel | null; typedValue: string }) {
  if (!typedValue) return null;
  if (!match) {
    return (
      <Text size="xs" c="dimmed">
        <Text span fw={500} c="orange">Not in catalog or live list.</Text>{' '}
        DecisionBox will try to dispatch but you may need to set <Text span fw={500}>Wire override</Text>.
      </Text>
    );
  }
  const pricing =
    match.input_price_per_million || match.output_price_per_million
      ? `$${match.input_price_per_million ?? 0}/M in · $${match.output_price_per_million ?? 0}/M out`
      : null;
  return (
    <Group gap="xs" wrap="wrap">
      <Badge size="xs" variant="light" color={match.source === 'live' ? 'green' : match.source === 'both' ? 'blue' : 'gray'}>
        {match.source}
      </Badge>
      {match.wire ? (
        <Badge size="xs" variant="light" color="blue">
          wire: {match.wire}
        </Badge>
      ) : (
        <Badge size="xs" variant="light" color="orange">
          wire: unknown — set Wire override
        </Badge>
      )}
      {match.max_output_tokens ? (
        <Badge size="xs" variant="light" color="gray">
          max out: {match.max_output_tokens.toLocaleString()}
        </Badge>
      ) : null}
      {pricing ? (
        <Badge size="xs" variant="light" color="gray">{pricing}</Badge>
      ) : null}
    </Group>
  );
}

function ModelDetailsPanel({ match, typedValue }: { match: ModelInfo | null; typedValue: string }) {
  if (!typedValue) return null;

  if (!match) {
    return (
      <Text size="xs" c="dimmed">
        <Text span fw={500} c="orange">Not in catalog.</Text>{' '}
        Set <Text span fw={500}>Wire override</Text> below to tell DecisionBox which schema this model uses.
      </Text>
    );
  }

  const pricing =
    match.input_price_per_million || match.output_price_per_million
      ? `$${match.input_price_per_million ?? 0}/M in · $${match.output_price_per_million ?? 0}/M out`
      : null;

  return (
    <Group gap="xs" wrap="wrap">
      <Badge size="xs" variant="light" color="blue">
        wire: {match.wire}
      </Badge>
      {match.max_output_tokens ? (
        <Badge size="xs" variant="light" color="gray">
          max out: {match.max_output_tokens.toLocaleString()}
        </Badge>
      ) : null}
      {pricing ? (
        <Badge size="xs" variant="light" color="gray">
          {pricing}
        </Badge>
      ) : null}
    </Group>
  );
}
