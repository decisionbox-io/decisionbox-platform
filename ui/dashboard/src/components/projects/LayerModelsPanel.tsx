'use client';

/**
 * Which provider entry and model each LLM layer uses.
 *
 * The shape follows what people actually do. One default pair applies
 * everywhere; a layer that wants something else is switched off "uses the
 * default" and gets its own row. So the common case is three controls —
 * provider, model, reasoning — and the seventeen-layer case is there when a
 * project needs it.
 *
 * The layer list is DATA, fetched from GET /api/v1/llm/layers. This component
 * holds no copy of it, so a layer added server-side shows up here without a
 * front-end release, and the per-layer model rules arrive with it.
 */

import { useEffect, useMemo, useState } from 'react';
import {
  Accordion, ActionIcon, Alert, Badge, Button, Card, Group, Loader, Select,
  Stack, Switch, Text, TextInput, Tooltip,
} from '@mantine/core';
import { IconAlertCircle, IconRotateClockwise } from '@tabler/icons-react';
import {
  LLMLayerDescriptor, LLMProviderEntry, LayerModel, LiveModel, ProviderMeta, api,
} from '@/lib/api';
import { LiveModelCombobox } from '@/components/common/LLMModelField';
import { TestConnectionButton } from './WarehouseConfigPanel';

/** The reserved row every layer without one of its own inherits. */
export const DEFAULT_LAYER = 'default';

const REASONING_KEY = 'reasoning_enabled';
const PROMPT_CACHE_KEY = 'prompt_cache';

export interface LayerModelsPanelProps {
  projectId: string;
  /** The project's provider entries, as currently edited. */
  entries: LLMProviderEntry[];
  providers: ProviderMeta[];
  value: Record<string, LayerModel>;
  onChange: (next: Record<string, LayerModel>) => void;
}

function boolFrom(config: Record<string, string> | undefined, key: string, fallback: boolean): boolean {
  const raw = (config?.[key] ?? '').trim().toLowerCase();
  if (raw === 'true' || raw === '1' || raw === 'on' || raw === 'yes') return true;
  if (raw === 'false' || raw === '0' || raw === 'off' || raw === 'no') return false;
  return fallback;
}

export function LayerModelsPanel({
  projectId, entries, providers, value, onChange,
}: LayerModelsPanelProps) {
  const [layers, setLayers] = useState<LLMLayerDescriptor[] | null>(null);
  const [layersError, setLayersError] = useState<string | null>(null);
  const [liveByEntry, setLiveByEntry] = useState<Record<string, LiveModel[] | null>>({});

  useEffect(() => {
    api.listLLMLayers()
      .then((resp) => setLayers(resp.layers))
      .catch((e) => setLayersError((e as Error).message));
  }, []);

  const entryOptions = useMemo(
    () => entries
      .filter((e) => e.provider)
      .map((e) => ({ value: e.id, label: e.label || `${e.id} — ${e.provider}` })),
    [entries],
  );

  const defaultRow: LayerModel = value[DEFAULT_LAYER] || {};

  // Models for an entry are loaded lazily and shared by every layer on it —
  // the same reason the agent builds one provider per entry rather than one
  // per layer.
  const loadModelsFor = async (entryId: string) => {
    if (liveByEntry[entryId] !== undefined) return;
    setLiveByEntry((prev) => ({ ...prev, [entryId]: null }));
    try {
      const resp = await api.listLiveLLMModelsForProvider(projectId, { provider_id: entryId });
      setLiveByEntry((prev) => ({ ...prev, [entryId]: resp.models }));
    } catch {
      setLiveByEntry((prev) => ({ ...prev, [entryId]: [] }));
    }
  };

  const setRow = (layer: string, next: LayerModel | null) => {
    const copy = { ...value };
    if (next === null) delete copy[layer];
    else copy[layer] = next;
    onChange(copy);
  };

  const applyToAll = () => {
    // "Apply to all" writes the default row and DROPS every override, which
    // is what the words mean. Keeping the overrides would leave a user who
    // clicked it still on several models.
    onChange({ [DEFAULT_LAYER]: defaultRow });
  };

  if (layersError) {
    return <Alert color="red" icon={<IconAlertCircle size={16} />}>Could not load the layer list: {layersError}</Alert>;
  }
  if (!layers) return <Loader size="sm" />;

  const overrideCount = Object.keys(value).filter((k) => k !== DEFAULT_LAYER).length;
  const groups: { key: 'main' | 'small'; title: string; blurb: string }[] = [
    { key: 'main', title: 'Main layers', blurb: 'Where the model choice changes what people see.' },
    { key: 'small', title: 'Small layers', blurb: 'Cheap, high-volume or mechanical. A fast model does these well.' },
  ];

  const renderRow = (layer: LLMLayerDescriptor) => {
    const row = value[layer.key];
    const usesDefault = row === undefined;
    const effective = row ?? defaultRow;
    const entryId = effective.provider_id || (entryOptions[0]?.value ?? '');
    const entry = entries.find((e) => e.id === entryId);
    const meta = providers.find((p) => p.id === entry?.provider) || null;
    const reasoningOn = boolFrom(effective.config, REASONING_KEY, false);
    // Caching has always been opt-out, so absence means on.
    const cacheOn = boolFrom(effective.config, PROMPT_CACHE_KEY, true);

    const setConfig = (key: string, raw: string | null) => {
      const config = { ...(row?.config || {}) };
      if (raw === null) delete config[key];
      else config[key] = raw;
      setRow(layer.key, { ...effective, config });
    };

    return (
      <Card key={layer.key} withBorder padding="sm" radius="md">
        <Stack gap="xs">
          <Group justify="space-between" align="flex-start" wrap="nowrap">
            <Stack gap={0}>
              <Group gap="xs">
                <Text fw={500} size="sm">{layer.label}</Text>
                {layer.refuses_reasoning_models && (
                  <Tooltip label="A model that thinks on every request returns an empty description inside this layer's small budget, so reasoning models are refused here.">
                    <Badge size="xs" variant="light" color="orange">no reasoning models</Badge>
                  </Tooltip>
                )}
              </Group>
              <Text size="xs" c="dimmed">{layer.description}</Text>
            </Stack>
            <Group gap="xs" wrap="nowrap">
              {!usesDefault && (
                <Tooltip label="Back to the default model">
                  <ActionIcon
                    variant="subtle"
                    aria-label={`Reset ${layer.label}`}
                    onClick={() => setRow(layer.key, null)}
                  >
                    <IconRotateClockwise size={16} />
                  </ActionIcon>
                </Tooltip>
              )}
              <Switch
                size="xs"
                label="uses the default"
                checked={usesDefault}
                onChange={(e) => {
                  if (e.currentTarget.checked) setRow(layer.key, null);
                  else setRow(layer.key, { ...defaultRow, config: { ...(defaultRow.config || {}) } });
                }}
              />
            </Group>
          </Group>

          {!usesDefault && (
            <Stack gap="xs">
              <Group grow align="flex-start">
                <Select
                  label="Provider"
                  data={entryOptions}
                  value={effective.provider_id || null}
                  onChange={(val) => {
                    if (!val) return;
                    void loadModelsFor(val);
                    // The model belongs to the entry it was picked from, so
                    // switching entries clears it rather than carrying a
                    // model the new connection may not serve.
                    setRow(layer.key, { ...effective, provider_id: val, model: '' });
                  }}
                />
                <LiveModelCombobox
                  providerMeta={meta}
                  liveModels={liveByEntry[entryId] ?? null}
                  value={effective.model || ''}
                  onChange={(val) => setRow(layer.key, { ...effective, model: val })}
                />
              </Group>
              <Group grow align="flex-start">
                <TextInput
                  label="Max input tokens"
                  description="Blank uses the model's own window."
                  value={effective.config?.['max_input_tokens'] || ''}
                  onChange={(e) => setConfig('max_input_tokens', e.currentTarget.value || null)}
                />
                <TextInput
                  label="Max output tokens"
                  description="Blank uses the model's own ceiling."
                  value={effective.config?.['max_output_tokens'] || ''}
                  onChange={(e) => setConfig('max_output_tokens', e.currentTarget.value || null)}
                />
              </Group>
              <Group gap="lg" wrap="wrap">
                <Switch
                  size="xs"
                  label="Reasoning"
                  checked={reasoningOn}
                  onChange={(e) => setConfig(REASONING_KEY, e.currentTarget.checked ? 'true' : 'false')}
                />
                <Switch
                  size="xs"
                  label="Prompt caching"
                  description="Turning this off for one layer alone opens a second connection to that provider — caching is applied when the connection is built, not per request."
                  checked={cacheOn}
                  onChange={(e) => setConfig(PROMPT_CACHE_KEY, e.currentTarget.checked ? 'true' : 'false')}
                />
              </Group>
              <TestConnectionButton
                projectId={projectId}
                target="llm"
                layer={layer.key}
                label={`Test ${layer.label}`}
              />
            </Stack>
          )}
        </Stack>
      </Card>
    );
  };

  const defaultEntry = entries.find((e) => e.id === (defaultRow.provider_id || entryOptions[0]?.value));
  const defaultMeta = providers.find((p) => p.id === defaultEntry?.provider) || null;
  const defaultEntryId = defaultRow.provider_id || (entryOptions[0]?.value ?? '');

  return (
    <Stack gap="md">
      <Stack gap={2}>
        <Text fw={600}>Models</Text>
        <Text size="xs" c="dimmed">
          One model for everything, or a different one for any layer. Budgets,
          reasoning and prompt caching follow the model you choose for that
          layer.
        </Text>
      </Stack>

      <Card withBorder padding="md" radius="md">
        <Stack gap="sm">
          <Group justify="space-between">
            <Text fw={500} size="sm">Default — used by every layer without its own</Text>
            <Button
              size="xs"
              variant="light"
              onClick={applyToAll}
              disabled={overrideCount === 0}
            >
              Apply to all {overrideCount > 0 ? `(clears ${overrideCount})` : ''}
            </Button>
          </Group>
          <Group grow align="flex-start">
            <Select
              label="Provider"
              data={entryOptions}
              value={defaultRow.provider_id || null}
              onChange={(val) => {
                if (!val) return;
                void loadModelsFor(val);
                onChange({ ...value, [DEFAULT_LAYER]: { ...defaultRow, provider_id: val, model: '' } });
              }}
            />
            <LiveModelCombobox
              providerMeta={defaultMeta}
              liveModels={liveByEntry[defaultEntryId] ?? null}
              value={defaultRow.model || ''}
              onChange={(val) => onChange({ ...value, [DEFAULT_LAYER]: { ...defaultRow, model: val } })}
            />
          </Group>
          <Group gap="lg" wrap="wrap">
            <Switch
              size="xs"
              label="Reasoning"
              checked={boolFrom(defaultRow.config, REASONING_KEY, false)}
              onChange={(e) => onChange({
                ...value,
                [DEFAULT_LAYER]: {
                  ...defaultRow,
                  config: { ...(defaultRow.config || {}), [REASONING_KEY]: e.currentTarget.checked ? 'true' : 'false' },
                },
              })}
            />
            <Switch
              size="xs"
              label="Prompt caching"
              checked={boolFrom(defaultRow.config, PROMPT_CACHE_KEY, true)}
              onChange={(e) => onChange({
                ...value,
                [DEFAULT_LAYER]: {
                  ...defaultRow,
                  config: { ...(defaultRow.config || {}), [PROMPT_CACHE_KEY]: e.currentTarget.checked ? 'true' : 'false' },
                },
              })}
            />
          </Group>
          <TestConnectionButton projectId={projectId} target="llm" label="Test default model" />
        </Stack>
      </Card>

      <Accordion multiple defaultValue={['main']} variant="separated">
        {groups.map((group) => {
          const items = layers.filter((l) => l.group === group.key);
          if (items.length === 0) return null;
          const overridden = items.filter((l) => value[l.key] !== undefined).length;
          return (
            <Accordion.Item key={group.key} value={group.key}>
              <Accordion.Control>
                <Group gap="xs">
                  <Text fw={500} size="sm">{group.title}</Text>
                  <Text size="xs" c="dimmed">{group.blurb}</Text>
                  {overridden > 0 && <Badge size="xs" variant="light">{overridden} overridden</Badge>}
                </Group>
              </Accordion.Control>
              <Accordion.Panel>
                <Stack gap="sm">{items.map(renderRow)}</Stack>
              </Accordion.Panel>
            </Accordion.Item>
          );
        })}
      </Accordion>
    </Stack>
  );
}

export default LayerModelsPanel;
