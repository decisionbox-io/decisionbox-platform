'use client';

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Autocomplete, Badge, Button, FileButton, Group, Select, Stack, Switch, Text, TextInput, Textarea } from '@mantine/core';
import { IconAlertTriangle } from '@tabler/icons-react';
import type { ConfigField, ConfigOption, LiveModel, ProviderMeta } from '@/lib/api';

// DynamicField renders one ConfigField from the backend provider meta.
//
// Behaviour:
//   - Key === "model": always a plain free-text input. The dashboard
//     no longer surfaces the shipped catalog as a default selection
//     list — operators load the upstream's live list via
//     LiveModelCombobox (rendered by the caller) for the picker, and
//     type any ID for new / preview models. The catalog still
//     enriches typed values with wire / pricing metadata inside
//     LiveModelCombobox (the badges next to a chosen / typed row),
//     but is never offered as a starter list of selectable options.
//   - field.options is non-empty + field.free_text is true: combobox over
//     the provided options.
//   - field.options is non-empty + !field.free_text: strict dropdown.
//   - field.type === "textarea": textarea (monospace, autosize).
//   - otherwise: plain text input.
export function DynamicField({
  field,
  value,
  onChange,
}: {
  field: ConfigField;
  value: string;
  onChange: (v: string) => void;
  /** Reserved for future per-field provider metadata; intentionally
   * unused since the model picker no longer surfaces catalog rows. */
  providerMeta?: ProviderMeta | null;
}) {
  const t = useTranslations('llmModelField');
  // Model field falls through to the plain free-text branch below.
  // No catalog dropdown — see the component-level comment above for
  // why; live-list pickers live in LiveModelCombobox instead.

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

  // Boolean toggle (e.g. tls_skip_verify). Values are transported as the
  // strings "true" / "false" since config is Record<string,string>.
  if (field.type === 'boolean') {
    const checked = value === 'true';
    return (
      <Stack gap="xs">
        <Switch
          label={field.label}
          description={field.description}
          checked={checked}
          onChange={(e) => onChange(e.currentTarget.checked ? 'true' : 'false')}
        />
        {checked && field.key === 'tls_skip_verify' && (
          <Alert color="red" variant="light" icon={<IconAlertTriangle size={16} />} title={t('tlsDisabledTitle')}>
            {t('tlsDisabledBody')}
          </Alert>
        )}
      </Stack>
    );
  }

  // Textarea, plus an optional file picker for "file" fields (e.g. a CA
  // certificate PEM — paste directly or load from a .pem/.crt file).
  if (field.type === 'textarea' || field.type === 'file') {
    return (
      <Stack gap={4}>
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
        {field.type === 'file' && (
          <Group>
            <FileButton
              accept=".pem,.crt,.cer,.cert,.txt"
              onChange={(file) => {
                if (file) file.text().then(onChange);
              }}
            >
              {(props) => <Button {...props} size="xs" variant="light">{t('loadFromFile')}</Button>}
            </FileButton>
            {value && (
              <Button size="xs" variant="subtle" color="gray" onClick={() => onChange('')}>
                {t('clear')}
              </Button>
            )}
          </Group>
        )}
      </Stack>
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
  const t = useTranslations('llmModelField');
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
        label={t('modelLabel')}
        required
        placeholder={t('preloadPlaceholder')}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        description={t('preloadDescription')}
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
        label={t('modelLabel')}
        required
        placeholder={t('enterModelIdPlaceholder')}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        description={t('noModelsDescription')}
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
        label={t('modelLabel')}
        required
        description={
          hiddenCount > 0 && !showAll
            ? t('loadedWithHiddenDescription', { count: rows.length, hidden: hiddenCount })
            : t('loadedDescription', { count: rows.length })
        }
        placeholder={t('modelExamplesPlaceholder')}
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
          label={t('showUnsupportedToggle', { count: hiddenCount })}
        />
      )}
      <LiveModelDetails match={match ?? enrichmentOnly} typedValue={value} matched={!!match} />
    </Stack>
  );
}

// modelWireIsKnown returns true when DecisionBox knows how to dispatch
// the given model id — either because it appears in the live+catalog
// list with a non-empty wire, or because it is in the provider's
// shipped catalog with a wire. Used by the project-create and settings
// pages to decide whether wire_override belongs front-and-centre or
// tucked into an "Advanced" disclosure.
//
// Returns false for free-text model ids that match neither list (the
// user deserves to see the escape hatch) and for rows the upstream
// returned without a usable wire (dispatchable=false).
export function modelWireIsKnown(
  liveModels: LiveModel[] | null,
  providerMeta: ProviderMeta | null,
  modelID: string,
): boolean {
  if (!modelID) return false;
  if (liveModels) {
    const hit = liveModels.find((m) => m.id === modelID);
    if (hit) return !!hit.dispatchable && !!hit.wire;
  }
  const cat = providerMeta?.models?.find((m) => m.id === modelID);
  if (cat) return !!cat.wire;
  return false;
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
  const t = useTranslations('llmModelField');
  if (!typedValue) return null;
  if (!match) {
    return (
      <Text size="xs" c="dimmed">
        <Text span fw={500} c="orange">{t('notInListTitle')}</Text>{' '}
        {t.rich('notInListBody', {
          wire: (chunks) => <Text span fw={500}>{chunks}</Text>,
        })}
      </Text>
    );
  }
  const pricing =
    match.input_price_per_million || match.output_price_per_million
      ? t('pricingBadge', {
          in: match.input_price_per_million ?? 0,
          out: match.output_price_per_million ?? 0,
        })
      : null;
  return (
    <Stack gap={4}>
      {!match.dispatchable && (
        <Text size="xs" c="orange" fw={500}>
          {t('notSupportedYet')}
        </Text>
      )}
      <Group gap="xs" wrap="wrap">
        {!matched && (
          <Badge size="xs" variant="light" color="gray">
            {t('catalogEnrichmentBadge')}
          </Badge>
        )}
        {match.wire ? (
          <Badge size="xs" variant="light" color={match.dispatchable ? 'blue' : 'orange'}>
            {t('wireBadge', { wire: match.wire })}
          </Badge>
        ) : (
          <Badge size="xs" variant="light" color="orange">
            {t('wireUnknownBadge')}
          </Badge>
        )}
        {match.max_output_tokens ? (
          <Badge size="xs" variant="light" color="gray">
            {t('maxOutBadge', { tokens: match.max_output_tokens.toLocaleString() })}
          </Badge>
        ) : null}
        {pricing ? (
          <Badge size="xs" variant="light" color="gray">{pricing}</Badge>
        ) : null}
      </Group>
    </Stack>
  );
}

