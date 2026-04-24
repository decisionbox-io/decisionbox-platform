'use client';

/**
 * EmbeddingModelCombobox is the embedding-specific counterpart to the
 * LLM LiveModelCombobox. Renders a searchable dropdown of embedding
 * models with their dimensions visible (dimensions matter for Qdrant:
 * the collection is bound to a fixed vector size at first index and a
 * model swap forces a rebuild).
 *
 * Shows ONLY the models the provider reports via ListModels. The
 * shipped catalog (providerMeta.models) is used solely to enrich live
 * rows that arrive without a dimensions field — we never display
 * catalog-only rows. The UI is paired with `<ProviderCredentialsPhase>`,
 * which guarantees `liveModels` is set before this component renders.
 */

import {
  Combobox, InputBase, Stack, Text, useCombobox,
} from '@mantine/core';
import { EmbeddingLiveModel, EmbeddingProviderMeta } from '@/lib/api';

interface Props {
  providerMeta: EmbeddingProviderMeta | null;
  liveModels: EmbeddingLiveModel[] | null;
  value: string;
  onChange: (v: string) => void;
}

export function EmbeddingModelCombobox({ providerMeta, liveModels, value, onChange }: Props) {
  const combobox = useCombobox();

  // Merge catalog + live rows. When both sources have the same ID we
  // prefer the live row's dimensions + lifecycle but fall back to the
  // catalog's name for display stability.
  const rows = buildRows(providerMeta, liveModels);

  const selected = rows.find((r) => r.id === value);
  const description = (() => {
    if (selected) {
      const dim = selected.dimensions > 0 ? `${selected.dimensions}-dim` : 'dimensions unknown';
      if (selected.lifecycle) return `${selected.displayName} · ${dim} · ${selected.lifecycle}`;
      return `${selected.displayName} · ${dim} vectors`;
    }
    return 'Pick a shipped model or type any model ID the provider supports.';
  })();

  const options = rows.map((r) => (
    <Combobox.Option key={r.id} value={r.id}>
      <Stack gap={2}>
        <Text size="sm">{r.displayName}</Text>
        <Text size="xs" c="dimmed">
          {r.id}
          {r.dimensions > 0 && ` · ${r.dimensions}d`}
          {r.lifecycle && ` · ${r.lifecycle}`}
        </Text>
      </Stack>
    </Combobox.Option>
  ));

  return (
    <Combobox
      store={combobox}
      onOptionSubmit={(val) => {
        onChange(val);
        combobox.closeDropdown();
      }}
    >
      <Combobox.Target>
        <InputBase
          label="Embedding Model"
          placeholder="text-embedding-3-large (or type a custom model ID)"
          value={value}
          onChange={(e) => {
            onChange(e.currentTarget.value);
            combobox.openDropdown();
          }}
          onFocus={() => combobox.openDropdown()}
          onBlur={() => combobox.closeDropdown()}
          description={description}
        />
      </Combobox.Target>
      <Combobox.Dropdown>
        <Combobox.Options>
          {options.length > 0 ? options : <Combobox.Empty>No models — type a model ID.</Combobox.Empty>}
        </Combobox.Options>
      </Combobox.Dropdown>
    </Combobox>
  );
}

interface Row {
  id: string;
  displayName: string;
  dimensions: number;
  lifecycle?: string;
}

// Builds rows from the live list only. We pass providerMeta in purely
// to backfill the displayName when the provider's upstream list is
// name-less, so users see "Embedding 3 Large" instead of the bare ID.
function buildRows(
  providerMeta: EmbeddingProviderMeta | null,
  liveModels: EmbeddingLiveModel[] | null
): Row[] {
  if (!liveModels) return [];

  const catalogNames = new Map<string, { name: string; dimensions: number }>();
  if (providerMeta) {
    for (const m of providerMeta.models) {
      catalogNames.set(m.id, { name: m.name || '', dimensions: m.dimensions });
    }
  }

  const rows: Row[] = liveModels.map((lm) => {
    const cat = catalogNames.get(lm.id);
    return {
      id: lm.id,
      displayName: lm.display_name || cat?.name || lm.id,
      dimensions: lm.dimensions > 0 ? lm.dimensions : (cat?.dimensions ?? 0),
      lifecycle: lm.lifecycle,
    };
  });
  return rows.sort((a, b) => a.id.localeCompare(b.id));
}
