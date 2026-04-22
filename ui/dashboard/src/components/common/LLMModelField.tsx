'use client';

import { useState } from 'react';
import { Autocomplete, Badge, Group, Select, Stack, Switch, Text, TextInput, Textarea } from '@mantine/core';
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
      const opts = field.options.map((o: ConfigOption) => ({ value: o.value, label: o.label || o.value }));
      const handle = (next: string) => {
        // Normalise the label Mantine Autocomplete writes on selection
        // back to the ConfigOption.value the backend expects.
        const hit = opts.find((o) => o.label === next);
        onChange(hit ? hit.value : next);
      };
      return (
        <Autocomplete
          label={field.label}
          required={field.required}
          description={field.description}
          placeholder={field.placeholder || field.default}
          value={value}
          onChange={handle}
          data={opts}
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

  const data = models.map((m) => ({ value: m.id, label: `${m.display_name} — ${m.id}` }));
  // See comment in LiveModelCombobox — normalise the Autocomplete's
  // label-valued input back to the raw model id.
  const handleChange = (next: string) => {
    const hit = data.find((o) => o.label === next);
    onChange(hit ? hit.value : next);
  };

  return (
    <Stack gap={4}>
      <Autocomplete
        label={field.label}
        required={field.required}
        description={field.description}
        placeholder={field.placeholder || field.default}
        value={value}
        onChange={handleChange}
        limit={50}
        data={data}
      />
      <ModelDetailsPanel match={match ?? null} typedValue={value} />
    </Stack>
  );
}

// LiveModelCombobox is the model picker shown after a live model list
// has been loaded from the upstream (phase 2 of project-create AI step;
// settings AI tab after auto-refresh or manual refresh).
//
// Design:
//   - liveModels === null → hasn't been loaded yet. Render a stub
//     TextInput that accepts free text but prompts the user to load
//     the live list first. This is the pre-load state in settings
//     before auto-refresh kicks in.
//   - liveModels !== null → render the picker from the upstream rows
//     only. Rows that exist only in our shipped catalog (i.e. no live
//     match) are filtered out to keep the UX simple — the catalog
//     still drives wire dispatch and pricing enrichment, but we do
//     not show models the provider didn't advertise to the user.
//
// Free text is always accepted so users can type an ID the upstream
// didn't return (new model, preview access, typo tolerance).
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
  // Default view hides models the agent can't dispatch today — the
  // upstream advertises them but we have no wire implementation that
  // speaks their schema (Nova / Titan on Bedrock, Cohere Command, AI21,
  // etc.). Users can flip the switch to see them anyway; picking one
  // still works if they set wire_override manually.
  // Hook must come before any early returns.
  const [showAll, setShowAll] = useState(false);

  // Not loaded yet → free-text input with a hint.
  if (liveModels === null) {
    return (
      <TextInput
        label="Model"
        required
        placeholder="Load models to pick from the list"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        description="Enter credentials and load the model list to see everything available to this key. Free text also works."
      />
    );
  }

  // Show only rows that came from the upstream — 'live' or 'both'.
  // Catalog-only entries are dropped from the picker.
  const allUpstreamRows = liveModels.filter((m) => m.source === 'live' || m.source === 'both');
  const rows = showAll ? allUpstreamRows : allUpstreamRows.filter((m) => m.dispatchable);
  const hiddenCount = allUpstreamRows.length - rows.length;
  const match = allUpstreamRows.find((m) => m.id === value) ?? null;

  // Enrichment (wire, max tokens, pricing) for the currently-typed
  // value that isn't in the live list — pull from the shipped catalog
  // when possible. This still matters because a user could paste a
  // valid model ID that the upstream's list endpoint doesn't return
  // (e.g. because it's an inference-profile ID).
  let enrichmentOnly: LiveModel | null = null;
  if (!match && value) {
    const cat = providerMeta?.models?.find((m) => m.id === value);
    if (cat) enrichmentOnly = { ...cat, source: 'catalog', dispatchable: !!cat.wire };
  }

  if (rows.length === 0) {
    return (
      <TextInput
        label="Model"
        required
        placeholder="Enter model ID"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        description="Upstream returned no models — type any model ID the provider accepts."
      />
    );
  }

  const data = rows.map((m) => ({ value: m.id, label: formatLiveRowLabel(m) }));

  // Mantine Autocomplete quirk: when the user picks an option whose
  // data shape is {value, label}, the input *text* becomes the label
  // ("Claude Sonnet 4.6 — anthropic.claude-sonnet-4-6"), not the
  // value. We normalise back to the model id in onChange so the
  // rest of the app — state, API payload, match lookup — always
  // sees the raw model id. Free text still flows through as-is.
  const handleChange = (next: string) => {
    const hit = data.find((o) => o.label === next);
    onChange(hit ? hit.value : next);
  };

  return (
    <Stack gap={4}>
      <Autocomplete
        label="Model"
        required
        description={
          hiddenCount > 0 && !showAll
            ? `${rows.length} dispatchable model${rows.length === 1 ? '' : 's'} loaded · ${hiddenCount} hidden (unsupported wire). Type to filter; free text also works.`
            : `${rows.length} model${rows.length === 1 ? '' : 's'} loaded — clear the box to browse, or type to filter. Free text also works.`
        }
        placeholder="e.g. claude-opus-4-6, gpt-4o, gemini-2.5-pro"
        value={value}
        onChange={handleChange}
        limit={100}
        data={data}
        // Custom filter: when the current value exactly matches one of
        // the options (by id), show the whole list (the user already
        // picked something and likely wants to browse alternatives);
        // otherwise case-insensitive substring match on id + label.
        filter={({ options, search }) => {
          const exact = data.some((o) => o.value === search || o.label === search);
          if (exact) return options;
          if (!search) return options;
          const s = search.toLowerCase();
          return (options as { value: string; label: string }[]).filter((o) =>
            o.value.toLowerCase().includes(s) || o.label.toLowerCase().includes(s)
          );
        }}
      />
      {hiddenCount > 0 && (
        <Switch
          size="xs"
          checked={showAll}
          onChange={(e) => setShowAll(e.currentTarget.checked)}
          label={`Show ${hiddenCount} unsupported model${hiddenCount === 1 ? '' : 's'} (Nova, Titan, Cohere, …)`}
        />
      )}
      <LiveModelDetails match={match ?? enrichmentOnly} typedValue={value} matched={!!match} />
    </Stack>
  );
}

function formatLiveRowLabel(m: LiveModel): string {
  if (m.display_name && m.display_name !== m.id) {
    return `${m.display_name} — ${m.id}`;
  }
  return m.id;
}

function LiveModelDetails({
  match,
  typedValue,
  matched,
}: {
  match: LiveModel | null;
  typedValue: string;
  matched: boolean;
}) {
  if (!typedValue) return null;
  if (!match) {
    return (
      <Text size="xs" c="dimmed">
        <Text span fw={500} c="orange">Not in the loaded list.</Text>{' '}
        DecisionBox will still try to dispatch — you may need to set <Text span fw={500}>Wire override</Text>.
      </Text>
    );
  }
  const pricing =
    match.input_price_per_million || match.output_price_per_million
      ? `$${match.input_price_per_million ?? 0}/M in · $${match.output_price_per_million ?? 0}/M out`
      : null;
  return (
    <Stack gap={4}>
      {!match.dispatchable && (
        <Text size="xs" c="orange" fw={500}>
          Not supported yet: DecisionBox doesn&apos;t have a wire implementation for this model&apos;s family.
          Pick another model, or set Wire override if you know a compatible wire.
        </Text>
      )}
      <Group gap="xs" wrap="wrap">
        {!matched && (
          <Badge size="xs" variant="light" color="gray">
            not in live list — using catalog enrichment
          </Badge>
        )}
        {match.wire ? (
          <Badge size="xs" variant="light" color={match.dispatchable ? 'blue' : 'orange'}>
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
    </Stack>
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
