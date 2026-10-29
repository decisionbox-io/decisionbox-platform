import {
  MAX_LLM_PROVIDER_ID_LEN, isValidLLMProviderId, llmEntryDisplayName, llmProviderIdFromName,
  llmProviderSecretKey,
} from '@/lib/api';

// The dashboard writes a provider entry's credential directly, so this
// derivation has to agree with llmlayer.CredentialsKey on the Go side. A
// drift would show up as a credential the agent cannot find — silently, on
// the next run — so the same values are pinned on both sides.
describe('llmProviderSecretKey', () => {
  it('keeps the reserved entries on their legacy keys', () => {
    expect(llmProviderSecretKey('default')).toBe('llm-credentials');
    expect(llmProviderSecretKey('')).toBe('llm-credentials');
    expect(llmProviderSecretKey('blurb')).toBe('blurb-llm-credentials');
  });

  it('hex-encodes any other id', () => {
    // "x" is 0x78; the cloud backends reject a raw underscore, which is why
    // the id is encoded rather than appended.
    expect(llmProviderSecretKey('x')).toBe('llm-credentials-78');
    expect(llmProviderSecretKey('fast_small')).toBe('llm-credentials-666173745f736d616c6c');
  });

  it('produces a key the strictest backend accepts', () => {
    // Azure Key Vault allows only [A-Za-z0-9-] in a secret name.
    for (const id of ['fast_small', 'Mixed-Case', 'deepinfra']) {
      expect(llmProviderSecretKey(id)).toMatch(/^[a-z0-9-]+$/);
    }
  });
});

// The id is derived from the name and never shown, so the derivation has to
// hold the invariants the field used to enforce by refusing input: the
// charset the secret backends accept, the length the composed Azure name
// caps, and no collision — with a sibling, or with a reserved slot.
describe('llmProviderIdFromName', () => {
  it('slugifies what a person would actually type', () => {
    expect(llmProviderIdFromName('DeepInfra — open weights', [])).toBe('deepinfra-open-weights');
    expect(llmProviderIdFromName('vLLM (A100)', [])).toBe('vllm-a100');
    expect(llmProviderIdFromName('  Azure  Foundry  ', [])).toBe('azure-foundry');
  });

  it('produces only what the secret backends can store', () => {
    const id = llmProviderIdFromName('Ünïcôde ✨ and symbols!!', []);
    expect(isValidLLMProviderId(id)).toBe(true);
  });

  it('never exceeds the length the composed Azure secret name caps', () => {
    const id = llmProviderIdFromName('a'.repeat(120), []);
    expect(id.length).toBeLessThanOrEqual(MAX_LLM_PROVIDER_ID_LEN);
    expect(isValidLLMProviderId(id)).toBe(true);
  });

  it('does not collide with a sibling', () => {
    expect(llmProviderIdFromName('Cheap', ['cheap'])).toBe('cheap-2');
    expect(llmProviderIdFromName('Cheap', ['cheap', 'cheap-2'])).toBe('cheap-3');
  });

  it('keeps clear of the reserved slots', () => {
    // These two key credentials an older agent image reads by a fixed name.
    expect(llmProviderIdFromName('default', [])).toBe('default-2');
    expect(llmProviderIdFromName('Blurb', [])).toBe('blurb-2');
  });

  it('yields nothing for a name with nothing to slugify', () => {
    expect(llmProviderIdFromName('—', [])).toBe('');
    expect(llmProviderIdFromName('   ', [])).toBe('');
    // Which the form reports as "name this connection" rather than saving
    // an entry whose credential could not be filed anywhere.
    expect(isValidLLMProviderId('')).toBe(false);
  });

  it('still fits the secret key the backend composes', () => {
    const id = llmProviderIdFromName('A very long connection name for a gateway', []);
    expect(llmProviderSecretKey(id)).toMatch(/^llm-credentials-[0-9a-f]+$/);
  });
});

// A connection is named by what its owner chose: the name they typed, else
// the provider they picked. Never by the id — the fallback used to read
// "default — bedrock" for an entry whose owner had typed neither word.
describe('llmEntryDisplayName', () => {
  const PROVIDERS = [
    { id: 'bedrock', name: 'Bedrock' },
    { id: 'vertex-ai', name: 'Vertex AI' },
  ];

  it('uses the name when there is one', () => {
    expect(llmEntryDisplayName({ id: 'cheap', label: 'DeepInfra', provider: 'bedrock' }, PROVIDERS))
      .toBe('DeepInfra');
  });

  it('names the reserved slots by the job they do, with the chosen provider', () => {
    expect(llmEntryDisplayName({ id: 'default', provider: 'bedrock' }, PROVIDERS))
      .toBe('Default · Bedrock');
    expect(llmEntryDisplayName({ id: 'blurb', provider: 'vertex-ai' }, PROVIDERS))
      .toBe('Table descriptions · Vertex AI');
  });

  it('uses the provider display name, not its registry slug', () => {
    expect(llmEntryDisplayName({ id: 'x', provider: 'vertex-ai' }, PROVIDERS)).toBe('Vertex AI');
  });

  it('never falls back to the id while there is a provider to name', () => {
    for (const id of ['default', 'blurb', 'some-entry']) {
      expect(llmEntryDisplayName({ id, provider: 'bedrock' }, PROVIDERS)).not.toContain(id);
    }
  });

  it('still says something for an entry with no provider yet', () => {
    expect(llmEntryDisplayName({ id: 'default' }, PROVIDERS)).toBe('Default');
    expect(llmEntryDisplayName({ id: 'half-made' }, PROVIDERS)).toBe('half-made');
  });
});
