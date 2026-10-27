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

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Accordion, ActionIcon, Alert, Badge, Button, Card, Group, Loader, Select,
  Stack, Switch, Text, TextInput, Tooltip,
} from '@mantine/core';
import { IconAlertCircle, IconRotateClockwise } from '@tabler/icons-react';
import {
  DEFAULT_LLM_PROVIDER_ID as DEFAULT_PROVIDER_ID,
  LLMLayerDescriptor, LLMProviderEntry, LayerModel, LiveModel, ProviderMeta, api,
} from '@/lib/api';
import { LiveModelCombobox } from '@/components/common/LLMModelField';
import { TestConnectionButton } from './WarehouseConfigPanel';
import { connectionKeyFor } from './LLMProvidersPanel';

/** The reserved row every layer without one of its own inherits. */
export const DEFAULT_LAYER = 'default';

/**
 * The one layer whose CREDENTIAL chain differs from its entry's: it tries
 * the blurb secret first. Its model list therefore has to be fetched for
 * the layer, not the entry. The key matches the server's vocabulary.
 */
const LAYER_TABLE_DESCRIPTIONS = 'table_descriptions';

const REASONING_KEY = 'reasoning_enabled';
const PROMPT_CACHE_KEY = 'prompt_cache';

export interface LayerModelsPanelProps {
  projectId: string;
  /** The project's provider entries, as currently edited. */
  entries: LLMProviderEntry[];
  /**
   * The entries AS STORED. The live-model endpoint answers about the
   * persisted connection, so this is what a fetched list describes — and
   * what its cache key has to be. Defaults to `entries` for a caller with
   * nothing unsaved.
   */
  savedEntries?: LLMProviderEntry[];
  /**
   * Incremented by the host on every successful save. Any cached model list
   * is dropped when it moves, because a credential-only save changes
   * nothing else that this panel can see.
   */
  savedGeneration?: number;
  /**
   * Models the Providers tab discovered with the credential in its form,
   * keyed by entry id. Used for a connection the server has not been given
   * yet — the only answer available for one, and what keeps "change a
   * provider, pick its model, save" possible in a single pass.
   */
  /** Discovered model lists, keyed by connection — see connectionKeyFor. */
  discoveredModels?: Record<string, LiveModel[]>;
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
  projectId, entries, savedEntries, savedGeneration = 0, discoveredModels,
  providers, value, onChange,
}: LayerModelsPanelProps) {
  const stored = savedEntries ?? entries;
  const [layers, setLayers] = useState<LLMLayerDescriptor[] | null>(null);
  const [layersError, setLayersError] = useState<string | null>(null);
  const [liveByEntry, setLiveByEntry] = useState<Record<string, LiveModel[] | null>>({});
  // Entries whose model list has been requested. A ref, not the state map:
  // several rows can ask for the same entry in one render pass, and reading
  // the state would see the value from before the batch.
  const requestedRef = useRef<Set<string>>(new Set());
  // The live_error the API returns with a 200 and catalogue-only rows when
  // upstream discovery failed. Kept so the picker can say so and offer a
  // retry rather than looking deliberately empty.
  const [liveErrors, setLiveErrors] = useState<Record<string, string | null>>({});

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
  // connectionKey identifies the CONNECTION an entry currently describes, not
  // just its id. An entry keeps its id when its provider, endpoint or
  // credentials change — that is what an id is for — so caching the model
  // list against the id alone meant switching the default connection from
  // OpenAI to Claude kept offering OpenAI's models, even after saving.
  const connectionKey = (entryId: string) => {
    const e = stored.find((x) => x.id === entryId);
    return e ? connectionKeyFor(e) : entryId;
  };

  // The identity of what the FORM now describes, which is what a discovered
  // list is filed under. Not the same thing as connectionKey above: that one
  // identifies the stored connection, which is what the server answers about.
  const editedConnectionKey = (entryId: string) => {
    const e = entries.find((x) => x.id === entryId);
    return e ? connectionKeyFor(e) : entryId;
  };

  // True when the form has moved away from what the server has. A list
  // fetched now would describe the STORED connection, so there is nothing
  // honest to show for an edited one until it is saved.
  const connectionUnsaved = (entryId: string) => {
    const edited = entries.find((x) => x.id === entryId);
    const persisted = stored.find((x) => x.id === entryId);
    if (!edited) return false;
    if (!persisted) return true;
    return connectionKeyFor(edited) !== connectionKeyFor(persisted);
  };

  /**
   * Table descriptions resolve a different CREDENTIAL from every other
   * layer on the same entry: blurb-llm-credentials (and BLURB_LLM_API_KEY)
   * before the entry's own. So the models reachable for that layer are not
   * necessarily the entry's, and listing by entry alone could offer models
   * indexing cannot reach, or hide ones it can. The endpoint takes a layer
   * for exactly this; every other layer still shares one entry-wide list.
   */
  const listsByLayer = (layer: string) => layer === LAYER_TABLE_DESCRIPTIONS;

  const cacheKeyFor = (entryId: string, layer: string) =>
    (listsByLayer(layer) ? `${layer}\u0002` : '') + connectionKey(entryId);

  const loadModelsFor = async (entryId: string, layer = DEFAULT_LAYER) => {
    // The server can only answer about a connection it has. Asking about an
    // edited one returns the stored entry's models, which would then be
    // shown against the new provider.
    if (connectionUnsaved(entryId)) return;
    const key = cacheKeyFor(entryId, layer);
    if (requestedRef.current.has(key)) return;
    requestedRef.current.add(key);
    try {
      // Both, for the table-description layer: provider_id names the entry
      // the row points at NOW (the server would otherwise resolve the
      // persisted assignment and answer about the previous one), while
      // layer keeps the blurb-first credential chain that makes this
      // listing different from the entry's.
      const body = listsByLayer(layer) ? { layer, provider_id: entryId } : { provider_id: entryId };
      const resp = await api.listLiveLLMModelsForProvider(projectId, body);
      setLiveByEntry((prev) => ({ ...prev, [key]: resp.models }));
      setLiveErrors((prev) => ({ ...prev, [key]: resp.live_error ?? null }));
    } catch {
      // A failure is not a verdict for the session. An entry the user has
      // just added is not on the server yet, so this 404s — and leaving it
      // marked as requested meant the picker stayed empty even after the
      // connection was saved and working. Forgetting it lets the next
      // attempt (a save, a re-pick, or Reload) try again.
      requestedRef.current.delete(key);
      setLiveByEntry((prev) => ({ ...prev, [key]: [] }));
    }
  };

  /** Drops what was loaded for a layer's connection and fetches it again. */
  const reloadModelsFor = async (entryId: string, layer = DEFAULT_LAYER) => {
    requestedRef.current.delete(cacheKeyFor(entryId, layer));
    await loadModelsFor(entryId, layer);
  };

  /**
   * The models known for a layer's connection.
   *
   * For an EDITED connection the server has nothing to say — it would
   * answer about the stored one — so the only honest source is what the
   * Providers tab discovered with the credential in its own form. Without
   * that, changing a provider cleared the model (it belongs to the old
   * one), left no list to pick from, and blocked the save that needs a
   * model: a dead end reachable from a two-click edit.
   *
   * Looked up by the connection the form now describes, so an entry edited
   * AGAIN since the check misses rather than being offered the previous
   * connection's models.
   */
  const modelsFor = (entryId: string, layer = DEFAULT_LAYER) => {
    if (connectionUnsaved(entryId)) return discoveredModels?.[editedConnectionKey(entryId)] ?? null;
    return liveByEntry[cacheKeyFor(entryId, layer)] ?? null;
  };

  /**
   * Whether a reload is worth offering: an empty picker, OR a list the
   * server could only fill from the catalogue because live discovery
   * failed. The combobox filters catalogue-only rows out, so without this
   * a transient upstream failure left an empty picker that looked
   * deliberately empty.
   */
  const needsReload = (entryId: string, layer = DEFAULT_LAYER) =>
    (modelsFor(entryId, layer)?.length ?? 0) === 0
    || Boolean(liveErrors[cacheKeyFor(entryId, layer)]);

  // The model-scoped settings, rendered identically for the default row and
  // for a layer's own row. One block on purpose: a control offered on an
  // override but not on the default is a control a project that runs one
  // model everywhere cannot reach at all — which is how the token caps and
  // wire_override went missing from settings.
  // What a toggle this row does not set will actually RESOLVE to.
  //
  // The backend falls back to the default row (through the project-level
  // mirror the default row is dual-written to), not to the switch's own
  // default — so a seeded table-description row with no entries of its own
  // showed caching on while the saved default had it off, and reasoning off
  // while it actually ran on. A control that reports the opposite of what
  // runs is worse than no control.
  const inheritedToggle = (row: LayerModel, key: string, fallback: boolean): boolean => {
    if (row.config?.[key] !== undefined) return boolFrom(row.config, key, fallback);
    return boolFrom(value[DEFAULT_LAYER]?.config, key, fallback);
  };

  /** Whether a row's connection is served by a user-deployed endpoint. */
  const usingEndpoint = (row: LayerModel) => {
    const entry = entries.find((e) => e.id === (row.provider_id || DEFAULT_PROVIDER_ID));
    return Boolean((row.config?.['endpoint_id'] || entry?.config?.['endpoint_id'] || '').trim());
  };

  const renderModelSettings = (
    row: LayerModel,
    meta: ProviderMeta | null,
    setCfg: (key: string, raw: string | null) => void,
  ) => {
    // Needed for a customer-named Bedrock / Vertex / Azure deployment whose
    // wire cannot be inferred from the model id. Offered only where the
    // provider declares it, so it is not a dead box on Claude or OpenAI —
    // and never on an endpoint, which serves its own model over its own
    // wire. vertex-ai refuses to build at all when both are set, so a box
    // that can only produce a broken layer should not be there.
    const wireField = usingEndpoint(row)
      ? undefined
      : meta?.config_fields?.find((f) => f.key === 'wire_override');
    return (
      <>
        <Group grow align="flex-start">
          <TextInput
            label="Max input tokens"
            description="Blank uses the model's own window."
            value={row.config?.['max_input_tokens'] || ''}
            onChange={(e) => setCfg('max_input_tokens', e.currentTarget.value || null)}
          />
          <TextInput
            label="Max output tokens"
            description="Blank uses the model's own ceiling."
            value={row.config?.['max_output_tokens'] || ''}
            onChange={(e) => setCfg('max_output_tokens', e.currentTarget.value || null)}
          />
        </Group>
        {wireField && (
          <TextInput
            label={wireField.label || 'Wire override'}
            description={wireField.description || 'Only for a deployment whose wire cannot be inferred from the model id.'}
            placeholder={wireField.placeholder || ''}
            value={row.config?.['wire_override'] || ''}
            onChange={(e) => setCfg('wire_override', e.currentTarget.value || null)}
          />
        )}
        <Group gap="lg" wrap="wrap">
          <Switch
            size="xs"
            label="Reasoning"
            checked={inheritedToggle(row, REASONING_KEY, false)}
            onChange={(e) => setCfg(REASONING_KEY, e.currentTarget.checked ? 'true' : 'false')}
          />
          <Switch
            size="xs"
            label="Prompt caching"
            description="Turning this off for one layer alone opens a second connection to that provider — caching is applied when the connection is built, not per request."
            checked={inheritedToggle(row, PROMPT_CACHE_KEY, true)}
            onChange={(e) => setCfg(PROMPT_CACHE_KEY, e.currentTarget.checked ? 'true' : 'false')}
          />
        </Group>
      </>
    );
  };

  // The entries the rows already name — what has to be loaded when the tab
  // OPENS, not only when a selection changes. Without this the live picker on
  // a saved project stayed empty: nothing had changed, so nothing loaded, and
  // a project on the default entry alone had no other selection to make.
  const selectedEntryIds = useMemo(() => {
    const fallback = entryOptions[0]?.value;
    const ids = new Set<string>();
    if (fallback) ids.add(fallback);
    for (const row of Object.values(value)) {
      if (row.provider_id) ids.add(row.provider_id);
    }
    return Array.from(ids).sort();
  }, [value, entryOptions]);

  // Depends on the CONNECTIONS, so editing one re-loads its models rather
  // than leaving the previous provider's list on screen.
  const selectedConnections = useMemo(
    () => selectedEntryIds.map(connectionKey).join('\u0001'),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selectedEntryIds, stored],
  );

  // A save is the one event that can change what the server would answer
  // without changing the connection key — a credential-only save leaves
  // provider and config untouched, so a list discovered with the old key
  // (or a live_error from a bad one) would otherwise survive the fix with
  // no way back but a page reload. savedEntries is replaced on every save,
  // so its identity is the signal.
  // A save is the one event that can change what the server would answer
  // without changing the connection key: a credential-only save leaves the
  // provider and config untouched, so a list discovered with the old key —
  // or the live_error from a bad one — would survive the fix with no way
  // back but a page reload.
  //
  // The host tells us a save happened. Sniffing the saved entries for a
  // change cannot: equal content is exactly what a credential-only save
  // produces, and a new array identity is what any parent re-render
  // produces.
  const savedGenerationRef = useRef(savedGeneration);
  if (savedGenerationRef.current !== savedGeneration) {
    savedGenerationRef.current = savedGeneration;
    requestedRef.current.clear();
  }

  useEffect(() => {
    for (const id of selectedEntryIds) {
      if (id) void loadModelsFor(id);
    }
    // Table descriptions resolve their own credential, so their list is
    // fetched separately — and only when that layer has a row to show.
    const td = value[LAYER_TABLE_DESCRIPTIONS];
    if (td) {
      const id = td.provider_id || DEFAULT_PROVIDER_ID;
      void loadModelsFor(id, LAYER_TABLE_DESCRIPTIONS);
    }
    // loadModelsFor guards against repeats through requestedRef, so the only
    // dependencies are which entries are selected and what they point at.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedConnections, projectId, savedGeneration]);

  // What this panel auto-filled, per layer, so switching models refreshes a
  // value it put there and never one the operator typed.
  const autofilledRef = useRef<Record<string, { max_input_tokens?: string; max_output_tokens?: string }>>({});

  /**
   * selectModel sets a layer's model and prefills the token limits from what
   * the upstream endpoint actually REPORTED for it (never a catalogue
   * estimate).
   *
   * Without it an uncatalogued gateway model's real window is recorded
   * nowhere: the row carries no override, and everything downstream budgets
   * against the global fallback — so a model advertising 8K is handed
   * prompts sized for a much larger one. The single-model editor has always
   * done this; the per-layer rows have to as well.
   */
  const selectModel = (layerKey: string, row: LayerModel, model: string, live: LiveModel[] | null, apply: (next: LayerModel) => void) => {
    const config: Record<string, string> = { ...(row.config || {}) };
    const chosen = (live ?? []).find((m) => m.id === model);
    const filled = autofilledRef.current[layerKey] ?? {};
    const next: { max_input_tokens?: string; max_output_tokens?: string } = {};

    const reported = (m: LiveModel | undefined, key: 'max_input_tokens' | 'max_output_tokens') => {
      const v = key === 'max_input_tokens' ? m?.live_max_input_tokens : m?.live_max_output_tokens;
      return (v ?? 0) > 0 ? String(v) : undefined;
    };

    (['max_input_tokens', 'max_output_tokens'] as const).forEach((key) => {
      const current = (config[key] ?? '').trim();
      // Only what this panel RECORDED filling counts as ours. Treating "it
      // equals what the current model reports" as ours too looked like a
      // way to recognise our own value across a reload, but it cannot tell
      // that apart from an operator who pinned the limit to the number the
      // model reports — which is the obvious thing to type. They then
      // switched model and watched their 8192 become 200000, or vanish.
      // A stale limit left behind is visible in the field and editable; a
      // budget silently rewritten is not.
      const ours = current !== '' && current === filled[key];
      if (current !== '' && !ours) return; // typed by the operator — left alone
      const detected = reported(chosen, key);
      if (detected !== undefined) {
        config[key] = detected;
        next[key] = detected;
      } else if (ours) {
        // The new model reports nothing: clear OUR stale value rather than
        // leave the previous model's window as this one's override.
        delete config[key];
      }
    });

    autofilledRef.current[layerKey] = next;
    apply({ ...row, model, config: Object.keys(config).length > 0 ? config : undefined });
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
                    void loadModelsFor(val, layer.key);
                    // The model belongs to the entry it was picked from, so
                    // switching entries clears it rather than carrying a
                    // model the new connection may not serve.
                    setRow(layer.key, { ...effective, provider_id: val, model: '' });
                  }}
                />
                <Stack gap={4}>
                  <LiveModelCombobox
                    providerMeta={meta}
                    liveModels={modelsFor(entryId, layer.key)}
                    value={effective.model || ''}
                    onChange={(val) => selectModel(
                      layer.key, effective, val, modelsFor(entryId, layer.key),
                      (next) => setRow(layer.key, next),
                    )}
                  />
                  {needsReload(entryId, layer.key) && entryId && (
                    <Button
                      variant="subtle"
                      size="compact-xs"
                      onClick={() => { void reloadModelsFor(entryId, layer.key); }}
                    >
                      Reload models
                    </Button>
                  )}
                </Stack>
              </Group>
              {renderModelSettings(effective, meta, setConfig)}
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

  // Written explicitly rather than left to whatever was stored: the reserved
  // entry is what the API requires on this row.
  const writeDefaultRow = (next: LayerModel) => {
    onChange({ ...value, [DEFAULT_LAYER]: { ...next, provider_id: DEFAULT_PROVIDER_ID } });
  };

  const setDefaultConfig = (key: string, raw: string | null) => {
    const config = { ...(defaultRow.config || {}) };
    if (raw === null) delete config[key];
    else config[key] = raw;
    writeDefaultRow({ ...defaultRow, config });
  };

  // The reserved entry, always: see the note on the Provider field below.
  const defaultEntryId = DEFAULT_PROVIDER_ID;
  const defaultEntry = entries.find((e) => e.id === defaultEntryId);
  const defaultMeta = providers.find((p) => p.id === defaultEntry?.provider) || null;

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
            {/*
              * The default row always uses the reserved entry, so this is a
              * label rather than a picker. The entry is a SLOT, not a
              * pointer: the legacy mirror an older agent image reads copies
              * this row's endpoint into project.llm while the credential it
              * reads for that is fixed at llm-credentials, so a default row
              * pointing elsewhere would send one entry's credential to
              * another's endpoint — which is why the API refuses it. Offering
              * the choice here only produced a selection that could not be
              * saved.
              */}
            <TextInput
              label="Provider"
              description={entryOptions.length > 1
                ? 'Every layer inherits this connection. Edit it under Providers, or give a layer its own row below.'
                : 'Edit this connection under Providers.'}
              value={defaultEntry?.label || defaultEntry?.provider || DEFAULT_PROVIDER_ID}
              readOnly
            />
            <Stack gap={4}>
              <LiveModelCombobox
                providerMeta={defaultMeta}
                liveModels={modelsFor(defaultEntryId)}
                value={defaultRow.model || ''}
                onChange={(val) => selectModel(
                  DEFAULT_LAYER, defaultRow, val, modelsFor(defaultEntryId),
                  writeDefaultRow,
                )}
              />
              {needsReload(defaultEntryId) && defaultEntryId && (
                <Button
                  variant="subtle"
                  size="compact-xs"
                  onClick={() => { void reloadModelsFor(defaultEntryId); }}
                >
                  Reload models
                </Button>
              )}
            </Stack>
          </Group>
          {renderModelSettings(defaultRow, defaultMeta, setDefaultConfig)}
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
