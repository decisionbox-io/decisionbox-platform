/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import {
  LLMProvidersPanel, ProviderEntryForm, entriesFrom, entryFormsFrom, pendingCredentialWrites,
} from '@/components/projects/LLMProvidersPanel';

jest.mock('@/components/projects/WarehouseConfigPanel', () => ({
  TestConnectionButton: () => <button type="button">Test LLM Connection</button>,
}));
jest.mock('@/components/ProviderCredentialsPhase', () => ({
  ProviderCredentialsPhase: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
  emptyCredentials: () => ({ provider: '', authMethod: '', config: {}, apiKey: '' }),
}));
jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: { listLiveLLMModelsForProvider: jest.fn().mockResolvedValue({ models: [] }) },
  };
});

const PROVIDERS = [
  { id: 'bedrock', name: 'Bedrock', description: '', config_fields: [], auth_methods: [] },
] as never[];

function form(partial: Partial<ProviderEntryForm> & { id: string }): ProviderEntryForm {
  return { label: '', provider: 'bedrock', authMethod: '', config: {}, apiKey: '', isNew: false, ...partial };
}

function renderPanel(
  value: ProviderEntryForm[],
  layersByProviderId: Record<string, string[]> = {},
  onChange = jest.fn(),
) {
  render(
    <MantineProvider>
      <LLMProvidersPanel
        projectId="proj-1"
        providers={PROVIDERS}
        value={value}
        onChange={onChange}
        secrets={[]}
        layersByProviderId={layersByProviderId}
        savedIds={value.map((f) => f.id)}
      />
    </MantineProvider>,
  );
  return onChange;
}

describe('LLMProvidersPanel', () => {
  // The default entry is a SLOT, not a pointer. An older agent image reads
  // its credential from the fixed llm-credentials key, so it cannot be
  // removed or renamed.
  it('does not offer to remove the default entry', () => {
    renderPanel([form({ id: 'default' }), form({ id: 'cheap' })]);
    expect(screen.queryByRole('button', { name: /Remove default/i })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Remove cheap/i })).toBeInTheDocument();
  });

  it('blocks removing an entry a layer still uses, and names the layers', () => {
    renderPanel(
      [form({ id: 'default' }), form({ id: 'cheap' })],
      { cheap: ['exploration', 'sql_repair'] },
    );
    expect(screen.getByRole('button', { name: /Remove cheap/i })).toBeDisabled();
    expect(screen.getByText(/used by 2 layers/i)).toBeInTheDocument();
  });

  it('allows removing an entry no layer uses', async () => {
    const onChange = renderPanel([form({ id: 'default' }), form({ id: 'cheap' })]);
    fireEvent.click(screen.getByRole('button', { name: /Remove cheap/i }));
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(onChange.mock.calls[0][0].map((f: ProviderEntryForm) => f.id)).toEqual(['default']);
  });

  it('freezes a saved entry id, because it keys the stored credential', () => {
    renderPanel([form({ id: 'default' }), form({ id: 'cheap', isNew: false })]);
    expect(screen.getByLabelText('Id')).toBeDisabled();
  });

  it('refuses an id the secret backends could not store', () => {
    renderPanel([form({ id: 'default' }), form({ id: 'bad id', isNew: true })]);
    expect(screen.getByText(/letters, digits, underscores or hyphens/i)).toBeInTheDocument();
  });

  it('reports a duplicate id rather than letting two entries collide', () => {
    renderPanel([form({ id: 'default' }), form({ id: 'dup' }), form({ id: 'dup' })]);
    expect(screen.getByText(/Duplicate provider id: dup/i)).toBeInTheDocument();
  });
});

describe('entry form conversion', () => {
  it('synthesises a default entry for a project that has none', () => {
    const forms = entryFormsFrom([{ id: 'cheap', provider: 'bedrock' }]);
    expect(forms[0].id).toBe('default');
    expect(forms[0].isNew).toBe(true);
  });

  it('puts the default entry first — it is what every layer inherits', () => {
    const forms = entryFormsFrom([
      { id: 'cheap', provider: 'bedrock' },
      { id: 'default', provider: 'bedrock' },
    ]);
    expect(forms[0].id).toBe('default');
  });

  it('reads an id-less stored entry as the default', () => {
    const forms = entryFormsFrom([{ id: '', provider: 'bedrock' }]);
    expect(forms.filter((f) => f.id === 'default')).toHaveLength(1);
  });

  // A credential never belongs in the project document, and a model is the
  // layer's choice — not the connection's.
  it('strips the credential and the model from a saved entry config', () => {
    const entries = entriesFrom([
      form({ id: 'default', config: { region: 'us-east-1', model: 'opus', credentials_json: 'sk-leak' }, authMethod: 'iam_role' }),
    ]);
    expect(entries[0].config).toEqual({ region: 'us-east-1', auth_method: 'iam_role' });
  });

  it('drops an entry with no provider chosen rather than saving a blank one', () => {
    expect(entriesFrom([form({ id: 'default' }), form({ id: 'half', provider: '' })])).toHaveLength(1);
  });

  it('writes each typed credential under its own entry key', () => {
    const writes = pendingCredentialWrites([
      form({ id: 'default', apiKey: 'sk-a' }),
      form({ id: 'cheap', apiKey: 'sk-b' }),
      form({ id: 'untouched' }),
    ]);
    expect(writes).toEqual([
      { key: 'llm-credentials', value: 'sk-a' },
      { key: 'llm-credentials-6368656170', value: 'sk-b' },
    ]);
  });
});
