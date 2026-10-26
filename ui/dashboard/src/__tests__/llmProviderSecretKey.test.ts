import { llmProviderSecretKey } from '@/lib/api';

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
