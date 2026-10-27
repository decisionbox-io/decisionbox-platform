/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { LayerModelsPanel, DEFAULT_LAYER } from '@/components/projects/LayerModelsPanel';
import type { LLMProviderEntry, LayerModel } from '@/lib/api';

// The per-layer model controls replaced two project-wide switches in
// Settings → Advanced (reasoning and prompt caching). Both are per layer now
// because a mixed pipeline makes one project-wide toggle span providers where
// it is inert on some and meaningful on others.

jest.mock('@/components/projects/WarehouseConfigPanel', () => ({
  TestConnectionButton: ({ label }: { label?: string }) => <button type="button">{label || 'Test'}</button>,
}));
jest.mock('@/components/common/LLMModelField', () => ({
  LiveModelCombobox: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <input aria-label="Model" value={value} onChange={(e) => onChange(e.currentTarget.value)} />
  ),
}));
// Spread the real module: the panel reads DEFAULT_LLM_PROVIDER_ID from it,
// and a mock that defines only `api` leaves that undefined — which is a
// different component than the one that ships.
jest.mock('@/lib/api', () => ({
  ...jest.requireActual('@/lib/api'),
  api: {
    listLLMLayers: jest.fn(),
    listLiveLLMModelsForProvider: jest.fn().mockResolvedValue({ models: [] }),
  },
}));

import { api } from '@/lib/api';

const LAYERS = [
  { key: 'exploration', label: 'Exploration', description: 'Explores the warehouse.', group: 'main', refuses_reasoning_models: false },
  { key: 'analysis', label: 'Analysis', description: 'Turns evidence into insights.', group: 'main', refuses_reasoning_models: false },
  { key: 'sql_repair', label: 'SQL repair', description: 'Repairs a rejected query.', group: 'small', refuses_reasoning_models: false },
  { key: 'table_descriptions', label: 'Table descriptions', description: 'Describes each table.', group: 'small', refuses_reasoning_models: true },
];

const ENTRIES: LLMProviderEntry[] = [
  { id: 'default', label: 'Bedrock — Opus', provider: 'bedrock' },
  { id: 'cheap', label: 'Bedrock — Haiku', provider: 'bedrock' },
];

const BEDROCK_META = {
  id: 'bedrock', name: 'Bedrock', description: '', auth_methods: [],
  // Bedrock really does declare wire_override: a customer-named deployment's
  // wire cannot be inferred from the model id.
  config_fields: [
    { key: 'wire_override', label: 'Wire override', required: false, type: 'string', placeholder: '', description: 'Override wire dispatch', default: '', options: [] },
  ],
};

function renderPanel(value: Record<string, LayerModel>, onChange = jest.fn()) {
  render(
    <MantineProvider>
      <LayerModelsPanel
        projectId="proj-1"
        entries={ENTRIES}
        providers={[BEDROCK_META as never]}
        value={value}
        onChange={onChange}
      />
    </MantineProvider>,
  );
  return onChange;
}

beforeEach(() => {
  jest.clearAllMocks();
  (api.listLLMLayers as jest.Mock).mockResolvedValue({
    version: 1, layers: LAYERS, default_layer_key: 'default',
  });
});

describe('LayerModelsPanel', () => {
  it('takes the layer list from the API rather than a copy of its own', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    // A layer added server-side has to appear here with no front-end release,
    // which is only true while this component holds no list.
    expect(await screen.findByText('Exploration')).toBeInTheDocument();
    expect(api.listLLMLayers).toHaveBeenCalled();
  });

  it('groups main and small layers', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    expect(await screen.findByText('Main layers')).toBeInTheDocument();
    expect(screen.getByText('Small layers')).toBeInTheDocument();
  });

  it('marks a layer that refuses reasoning models, so the user is warned before a save the API rejects', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    await screen.findByText('Main layers');
    fireEvent.click(screen.getByText('Small layers'));
    expect(await screen.findByText('no reasoning models')).toBeInTheDocument();
  });

  it('shows every layer as using the default until it has a row', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    await screen.findByText('Exploration');
    const switches = screen.getAllByRole('switch', { name: /uses the default/i });
    expect(switches.length).toBeGreaterThan(0);
    switches.forEach((s) => expect(s).toBeChecked());
  });

  it('turning off "uses the default" seeds the row from the default pair', async () => {
    const onChange = renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus', config: { prompt_cache: 'false' } } });
    await screen.findByText('Exploration');
    fireEvent.click(screen.getAllByRole('switch', { name: /uses the default/i })[0]);
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    const next = onChange.mock.calls[0][0];
    // Seeded, not blank: an override starts from what the layer was already
    // doing, so the user changes one thing rather than re-entering a pair.
    expect(next.exploration).toEqual({ provider_id: 'default', model: 'opus', config: { prompt_cache: 'false' } });
  });

  it('resetting an override removes its row so the layer inherits again', async () => {
    const onChange = renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'cheap', model: 'haiku' },
    });
    await screen.findByText('Exploration');
    fireEvent.click(screen.getByRole('button', { name: /Reset Exploration/i }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(onChange.mock.calls[0][0].exploration).toBeUndefined();
  });

  it('Apply to all clears every override, which is what the words mean', async () => {
    const onChange = renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'cheap', model: 'haiku' },
      sql_repair: { provider_id: 'cheap', model: 'haiku' },
    });
    await screen.findByText('Exploration');
    fireEvent.click(screen.getByRole('button', { name: /Apply to all/i }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(onChange.mock.calls[0][0]).toEqual({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
  });

  // Caching has always been opt-out, so a layer that says nothing caches.
  it('shows prompt caching on when a layer says nothing about it', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'cheap', model: 'haiku' },
    });
    await screen.findByText('Exploration');
    const caching = screen.getAllByRole('switch', { name: /prompt caching/i });
    caching.forEach((s) => expect(s).toBeChecked());
  });

  it('reads a layer-level prompt-cache override back', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'cheap', model: 'haiku', config: { prompt_cache: 'false' } },
    });
    await screen.findByText('Exploration');
    // Two switches: the default row's (on) and the override's (off).
    const caching = screen.getAllByRole('switch', { name: /prompt caching/i });
    expect(caching.some((s) => !(s as HTMLInputElement).checked)).toBe(true);
  });

  // Prompt caching is applied when the connection is built, not per request,
  // so turning it off for one layer buys a second connection. The UI says so
  // where the toggle lives rather than leaving it as a surprise on the bill.
  it('says that turning caching off for one layer opens a second connection', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'cheap', model: 'haiku' },
    });
    await screen.findByText('Exploration');
    expect(screen.getAllByText(/opens a second connection/i).length).toBeGreaterThan(0);
  });

  it('switching a layer to another provider entry clears its model', async () => {
    const onChange = renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'default', model: 'opus' },
    });
    await screen.findByText('Exploration');
    // The model belongs to the connection it was picked from; carrying it to
    // another entry would send a model that entry may not serve.
    const onChangeCalls = () => onChange.mock.calls.map((c) => c[0]);
    fireEvent.change(screen.getAllByLabelText('Model')[1], { target: { value: 'haiku' } });
    await waitFor(() => expect(onChangeCalls().length).toBeGreaterThan(0));
    expect(onChangeCalls()[0].exploration.model).toBe('haiku');
  });
});

// The live picker is the point of the panel, and it was unreachable: the
// model list loaded only when a provider SELECTION CHANGED, so opening a
// saved project's Models tab showed none — and a project on the default
// entry alone has no other selection to make.
describe('LayerModelsPanel — live models for the entries already selected', () => {
  it('loads them when the tab opens', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      sql_repair: { provider_id: 'cheap', model: 'haiku' },
    });
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalledWith('proj-1', { provider_id: 'default' }));
    expect(api.listLiveLLMModelsForProvider).toHaveBeenCalledWith('proj-1', { provider_id: 'cheap' });
  });

  it('asks for each entry once, however many layers share it', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      exploration: { provider_id: 'default', model: 'opus' },
      analysis: { provider_id: 'default', model: 'opus' },
    });
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalled());
    const forDefault = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls
      .filter(([, body]) => body.provider_id === 'default');
    expect(forDefault).toHaveLength(1);
  });
});

// Every model-scoped control has to exist on the DEFAULT row too. A project
// that runs one model everywhere never opens an override, so a control
// offered only there is a control it cannot reach — which is how the token
// caps and wire_override disappeared from settings for exactly the
// customer-named Bedrock / Vertex / Azure deployments that need them.
describe('LayerModelsPanel — the default row carries the model settings', () => {
  it('offers the token caps and the wire override', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    await screen.findByText('Main layers');
    // One per row rendered; the default card is the first.
    expect(screen.getAllByLabelText('Max input tokens').length).toBeGreaterThan(0);
    expect(screen.getAllByLabelText('Max output tokens').length).toBeGreaterThan(0);
    expect(screen.getAllByLabelText('Wire override').length).toBeGreaterThan(0);
  });

  it('writes a wire override onto the default row', async () => {
    const onChange = renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    await screen.findByText('Main layers');
    fireEvent.change(screen.getAllByLabelText('Wire override')[0], { target: { value: 'anthropic-messages' } });
    expect(onChange).toHaveBeenCalled();
    const next = onChange.mock.calls[onChange.mock.calls.length - 1][0];
    expect(next[DEFAULT_LAYER].config.wire_override).toBe('anthropic-messages');
  });

  it('hides the wire override on a provider that does not declare it', async () => {
    render(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1"
          entries={[{ id: 'default', label: 'Claude', provider: 'claude' }]}
          providers={[{ id: 'claude', name: 'Claude', description: '', config_fields: [], auth_methods: [] } as never]}
          value={{ [DEFAULT_LAYER]: { provider_id: 'default', model: 'sonnet' } }}
          onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await screen.findByText('Main layers');
    expect(screen.queryByLabelText('Wire override')).not.toBeInTheDocument();
  });
});

// The default row always uses the reserved entry — the API refuses anything
// else, because the legacy mirror an older agent image reads resolves its
// credential from a fixed key. A picker here produced a selection that could
// never be saved.
describe('LayerModelsPanel — the default row is pinned to the reserved entry', () => {
  it('shows the connection rather than offering a choice', async () => {
    renderPanel({ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } });
    await screen.findByText('Main layers');

    // The default card's Provider control is read-only and names the entry.
    const provider = screen.getAllByLabelText('Provider')[0] as HTMLInputElement;
    expect(provider).toHaveAttribute('readonly');
    expect(provider.value).toBe('Bedrock — Opus');
  });

  it('keeps writing the reserved entry onto the row', async () => {
    const onChange = renderPanel({ [DEFAULT_LAYER]: { model: 'opus' } });
    await screen.findByText('Main layers');
    fireEvent.change(screen.getAllByLabelText('Model')[0], { target: { value: 'sonnet' } });
    const next = onChange.mock.calls[onChange.mock.calls.length - 1][0];
    expect(next[DEFAULT_LAYER].provider_id).toBe('default');
    expect(next[DEFAULT_LAYER].model).toBe('sonnet');
  });

  // An override row DOES choose, which is the whole point of a second entry.
  it('still offers the choice on a layer of its own', async () => {
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      analysis: { provider_id: 'cheap', model: 'haiku' },
    });
    await screen.findByText('Main layers');
    const pickers = screen.getAllByLabelText('Provider').filter((el) => !el.hasAttribute('readonly'));
    expect(pickers.length).toBeGreaterThan(0);
  });
});

// A model list that failed to load must not stay empty for the session. An
// entry the user has just added is not on the server yet, so the lookup 404s
// — and the picker stayed empty even after the connection was saved and
// working.
describe('LayerModelsPanel — a failed model load can be retried', () => {
  it('offers a reload and asks again', async () => {
    (api.listLiveLLMModelsForProvider as jest.Mock).mockRejectedValueOnce(new Error('not found'));
    // An override on a MAIN layer, which is the group the accordion opens
    // with — a collapsed panel's controls are not reachable.
    renderPanel({
      [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' },
      analysis: { provider_id: 'cheap', model: '' },
    });
    await screen.findByText('Main layers');
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalled());

    const before = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length;
    const reload = screen.getAllByRole('button', { name: 'Reload models' })[0];
    fireEvent.click(reload);
    await waitFor(() => {
      expect((api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length).toBeGreaterThan(before);
    });
  });
});

// An entry keeps its id when its provider, endpoint or credentials change —
// that is what an id is for. Caching the model list against the id alone
// meant switching the default connection from Bedrock to Claude kept
// offering Bedrock's models, even after saving.
describe('LayerModelsPanel — a changed connection invalidates its model list', () => {
  it('asks again when the entry behind an id changes provider', async () => {
    (api.listLiveLLMModelsForProvider as jest.Mock).mockResolvedValue({
      models: [{ id: 'anthropic.claude-opus', name: 'Opus' }],
    });
    const value = { [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } };
    const { rerender } = render(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={ENTRIES} providers={[BEDROCK_META as never]}
          value={value} onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalled());
    const before = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length;

    // The same id, a different connection behind it.
    const edited = [{ id: 'default', label: 'Claude', provider: 'claude' }, ENTRIES[1]];
    rerender(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={edited} providers={[BEDROCK_META as never]}
          value={value} onChange={jest.fn()}
        />
      </MantineProvider>,
    );

    await waitFor(() => {
      expect((api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length).toBeGreaterThan(before);
    });
  });

  it('does not ask again when nothing about the connection changed', async () => {
    const value = { [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } };
    const { rerender } = render(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={ENTRIES} providers={[BEDROCK_META as never]}
          value={value} onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalled());
    const before = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length;

    rerender(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={[...ENTRIES]} providers={[BEDROCK_META as never]}
          value={{ ...value }} onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await waitFor(() => expect(api.listLiveLLMModelsForProvider).toHaveBeenCalled());
    expect((api.listLiveLLMModelsForProvider as jest.Mock).mock.calls.length).toBe(before);
  });
});

// The live-model endpoint answers about the STORED connection. A list
// fetched while the form says something else describes the stored entry —
// so caching it against the edited values left a repointed entry offering
// its old provider's models, and saving did not clear them because the key
// was already the new one.
describe('LayerModelsPanel — an edited connection has no model list yet', () => {
  const EDITED: LLMProviderEntry[] = [
    { id: 'default', label: 'Claude', provider: 'claude' },
    ENTRIES[1],
  ];

  it('does not ask the server about a connection it has not got', async () => {
    render(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={EDITED} savedEntries={ENTRIES}
          providers={[BEDROCK_META as never]}
          value={{ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } }}
          onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await screen.findByText('Main layers');
    const askedForDefault = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls
      .filter(([, body]) => body.provider_id === 'default');
    expect(askedForDefault).toHaveLength(0);
  });

  it('asks once the form matches what was saved', async () => {
    render(
      <MantineProvider>
        <LayerModelsPanel
          projectId="proj-1" entries={ENTRIES} savedEntries={ENTRIES}
          providers={[BEDROCK_META as never]}
          value={{ [DEFAULT_LAYER]: { provider_id: 'default', model: 'opus' } }}
          onChange={jest.fn()}
        />
      </MantineProvider>,
    );
    await waitFor(() => {
      const asked = (api.listLiveLLMModelsForProvider as jest.Mock).mock.calls
        .filter(([, body]) => body.provider_id === 'default');
      expect(asked.length).toBeGreaterThan(0);
    });
  });
});
