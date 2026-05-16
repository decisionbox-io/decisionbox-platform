'use client';

/**
 * Shared credentials phase for any pick-a-provider+pick-a-model flow:
 * analysis LLM, blurb LLM, and embedding. Owns the 80% that all three
 * have in common so the kind-specific editors only need to plug their
 * own model-picker render into the `children` slot.
 *
 * What this component does:
 *   1. Renders a provider <Select>.
 *   2. Renders each provider's `config_fields` via the CatalogAwareField
 *      component (covers `model`-catalog field and `wire_override` as
 *      needed — but the kind-specific parent filters `model` /
 *      `wire_override` out of its config map since those live in the
 *      model picker itself).
 *   3. Renders a clear-text monospace <Textarea> for the selected auth
 *      method's credential field. Matches the warehouse credential UI
 *      (clear text, monospace, autosize) so JSON service-account keys
 *      and AWS access-key strings are readable while editing. The
 *      saved value is encrypted in the secret store — the form input
 *      itself is just a multi-line editor.
 *   4. Renders a "Load models" button that invokes the caller-supplied
 *      `onLoad` handler. The handler is responsible for doing the
 *      network call (since the endpoint + response type differ per
 *      kind); this component just manages the loading/error UI around
 *      that call.
 *
 * What it deliberately does NOT do:
 *   - The model picker itself. ModelInfo (LLM) and EmbeddingModelMeta
 *     have genuinely different shapes — LLM carries wire + pricing +
 *     lifecycle, embedding carries dimensions — so each kind ships its
 *     own picker component that receives the live list and renders
 *     appropriately. The `children` prop is called once the credentials
 *     phase advances so the parent's picker can render inline.
 *
 * Why composition instead of one big `<ProviderEditor kind="...">`?
 * Two reasons: (1) TypeScript can type-check each kind's model-picker
 * with its concrete list type, no unions or discriminators; (2) LLM-
 * only concerns (wire_override, tool-use, reasoning-class rejection)
 * don't leak into embedding code paths.
 */

import { useRef, useState } from 'react';
import { Alert, Button, Card, Group, Select, Stack, Text, Textarea } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { DynamicField as CatalogAwareField } from '@/components/common/LLMModelField';
import { AuthMethod, ConfigField } from '@/lib/api';

/**
 * Minimal provider shape this component operates on. Both ProviderMeta
 * (LLM) and EmbeddingProviderMeta satisfy it, so we don't need generics
 * or a discriminator — duck typing through TS structural types is
 * enough.
 *
 * auth_methods is optional: providers that need no credentials (Ollama)
 * leave it absent; api-key providers declare a single "api_key" method;
 * cloud providers declare the full credential strategy menu (iam_role /
 * access_keys / assume_role for AWS, adc / sa_key for GCP).
 */
export interface ProviderLike {
  id: string;
  name: string;
  description: string;
  config_fields: ConfigField[];
  auth_methods?: AuthMethod[];
}

export interface CredentialsPhaseValue {
  provider: string;
  /** Selected auth method ID. Empty when the provider declares no auth methods. */
  authMethod: string;
  config: Record<string, string>;
  /**
   * Credential value entered for the selected auth method's credential
   * field. The name "apiKey" is kept for backwards-compat with editor
   * state types; semantically this is "the credential blob" (API key,
   * AWS access-keys string, GCP service-account JSON, …).
   */
  apiKey: string;
}

export function emptyCredentials(): CredentialsPhaseValue {
  return { provider: '', authMethod: '', config: {}, apiKey: '' };
}

export interface ProviderCredentialsPhaseProps<P extends ProviderLike> {
  /** All registered providers of this kind (LLM or embedding). */
  providers: P[];
  /** Label the <Select> uses. "LLM Provider" / "Embedding Provider" / "Blurb LLM Provider". */
  label: string;
  /** Required flag for the provider Select + API key. */
  required?: boolean;
  /** Controlled state. */
  value: CredentialsPhaseValue;
  onChange: (next: CredentialsPhaseValue) => void;
  /**
   * "Load models" click handler. The parent makes the network call
   * (its response type differs per kind) and is responsible for
   * rendering the resulting picker in the `children` slot once phase
   * flips to "model". The return tuple tells this component whether
   * to advance the phase and what error — if any — to surface.
   */
  onLoad: (cfg: Record<string, string>) => Promise<{ ok: boolean; liveError?: string }>;
  /**
   * Render-prop: called once the parent's load finishes. The caller
   * renders the model picker here with full type-safety against its
   * own per-kind model list.
   */
  children?: React.ReactNode;
  /** Copy override for the "Load models" button. Default "Load models". */
  loadButtonLabel?: string;
  /** Message shown when the provider doesn't need an API key. Default wording covers cloud-creds flows. */
  noKeyHint?: string;
  /** Whether to show the full "Back to credentials" button once in model phase. Default true. */
  backable?: boolean;
  /** Set by the parent when it externally resets to credentials phase. */
  phaseOverride?: 'credentials' | 'model';
  /** Fires when phase transitions. */
  onPhaseChange?: (phase: 'credentials' | 'model') => void;
}

export function ProviderCredentialsPhase<P extends ProviderLike>({
  providers,
  label,
  required,
  value,
  onChange,
  onLoad,
  children,
  loadButtonLabel = 'Load models',
  noKeyHint = 'This provider uses cloud credentials (IAM / ADC). No API key needed.',
  backable = true,
  phaseOverride,
  onPhaseChange,
}: ProviderCredentialsPhaseProps<P>) {
  const [phase, setPhase] = useState<'credentials' | 'model'>(() => phaseOverride ?? 'credentials');
  const [loading, setLoading] = useState(false);
  const [liveError, setLiveError] = useState<string | null>(null);
  const loadReqIdRef = useRef(0);

  const selected = providers.find((p) => p.id === value.provider);
  const authMethods = selected?.auth_methods ?? [];
  const selectedMethod = authMethods.find((m) => m.id === value.authMethod);
  // Each auth method declares at most one credential field today
  // (api_key for direct providers, credentials_json for cloud blobs).
  const credentialField = (selectedMethod?.fields ?? []).find((f) => f.type === 'credential');
  // Non-credential auth-method fields (role_arn, external_id, …) render
  // alongside the credential input.
  const nonCredentialAuthFields = (selectedMethod?.fields ?? []).filter((f) => f.type !== 'credential');
  const needsCredential = Boolean(credentialField);

  // Monotonic request id so a stale response can't overwrite newer state.
  const handleLoad = async () => {
    if (!value.provider) return;
    const reqId = ++loadReqIdRef.current;
    setLoading(true);
    setLiveError(null);
    try {
      const cfg: Record<string, string> = { ...value.config };
      if (value.authMethod) cfg['auth_method'] = value.authMethod;
      if (value.apiKey && credentialField) cfg[credentialField.key] = value.apiKey;
      const result = await onLoad(cfg);
      if (reqId !== loadReqIdRef.current) return;
      if (result.liveError) setLiveError(result.liveError);
      if (result.ok) {
        setPhase('model');
        onPhaseChange?.('model');
      }
    } catch (e: unknown) {
      if (reqId !== loadReqIdRef.current) return;
      setLiveError(e instanceof Error ? e.message : String(e));
      setPhase('model');
      onPhaseChange?.('model');
    } finally {
      if (reqId === loadReqIdRef.current) setLoading(false);
    }
  };

  const setProvider = (providerID: string) => {
    const prov = providers.find((p) => p.id === providerID);
    // Auto-pick the single auth method when there's only one — no
    // point making the user click "API Key" for Claude/OpenAI/Voyage.
    const methods = prov?.auth_methods ?? [];
    const defaultMethod = methods.length === 1 ? methods[0].id : '';
    onChange({
      provider: providerID,
      authMethod: defaultMethod,
      config: prov ? buildDefaults(prov.config_fields) : {},
      apiKey: '',
    });
    setPhase('credentials');
    onPhaseChange?.('credentials');
    setLiveError(null);
  };

  const setAuthMethod = (methodID: string) => {
    onChange({ ...value, authMethod: methodID, apiKey: '' });
    setLiveError(null);
  };

  return (
    <Stack gap="sm">
      <Select
        label={label}
        required={required}
        placeholder={`Select ${label.toLowerCase()}`}
        data={providers.map((p) => ({ value: p.id, label: p.name }))}
        value={value.provider || null}
        onChange={(v) => v && setProvider(v)}
      />
      {selected?.description && <Text size="xs" c="dimmed">{selected.description}</Text>}

      {selected && (
        <Card withBorder p="md">
          <Stack gap="sm">
            {phase === 'credentials' && (
              <>
                {/* Non-credential, non-model, non-wire top-level provider config fields. */}
                {selected.config_fields
                  .filter(
                    (f) =>
                      f.type !== 'credential' &&
                      f.key !== 'model' &&
                      f.key !== 'wire_override'
                  )
                  .map((field) => (
                    <CatalogAwareField
                      key={field.key}
                      field={field}
                      providerMeta={null}
                      value={value.config[field.key] || ''}
                      onChange={(val) =>
                        onChange({ ...value, config: { ...value.config, [field.key]: val } })
                      }
                    />
                  ))}

                {/* Auth method selector — shown when the provider declares 2+ methods.
                    Single-method providers (Claude/OpenAI/Voyage api_key, …) auto-select. */}
                {authMethods.length > 1 && (
                  <Select
                    label="Authentication method"
                    required={required}
                    data={authMethods.map((m) => ({ value: m.id, label: m.name }))}
                    value={value.authMethod || null}
                    onChange={(v) => v && setAuthMethod(v)}
                  />
                )}
                {selectedMethod?.description && (
                  <Text size="xs" c="dimmed">{selectedMethod.description}</Text>
                )}

                {/* Per-method non-credential fields (e.g. role_arn for assume_role). */}
                {nonCredentialAuthFields.map((field) => (
                  <CatalogAwareField
                    key={field.key}
                    field={field}
                    providerMeta={null}
                    value={value.config[field.key] || ''}
                    onChange={(val) =>
                      onChange({ ...value, config: { ...value.config, [field.key]: val } })
                    }
                  />
                ))}

                {credentialField && (
                  <Textarea
                    label={credentialField.label || 'Credentials'}
                    required={required}
                    placeholder={credentialField.placeholder || `Enter ${(credentialField.label || 'credentials').toLowerCase()}`}
                    value={value.apiKey}
                    onChange={(e) => onChange({ ...value, apiKey: e.target.value })}
                    description="Used now only to load the model list; stored encrypted when the project is saved."
                    minRows={3}
                    autosize
                    styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }}
                  />
                )}

                {value.authMethod && !credentialField && (
                  <Text size="xs" c="dimmed">{noKeyHint}</Text>
                )}

                {authMethods.length === 0 && (
                  <Text size="xs" c="dimmed">This provider requires no credentials.</Text>
                )}

                <Button
                  size="xs"
                  onClick={handleLoad}
                  loading={loading}
                  disabled={
                    !value.provider ||
                    (authMethods.length > 0 && !value.authMethod) ||
                    (needsCredential && !value.apiKey)
                  }
                  style={{ alignSelf: 'flex-start' }}
                >
                  {loadButtonLabel}
                </Button>
              </>
            )}

            {phase === 'model' && (
              <>
                {liveError && (
                  <Alert color="orange" icon={<IconAlertCircle size={16} />} title="Could not fetch live model list">
                    {liveError} — showing catalog models instead.
                  </Alert>
                )}

                {children}

                <Group>
                  {backable && (
                    <Button
                      variant="default"
                      size="xs"
                      onClick={() => {
                        setPhase('credentials');
                        onPhaseChange?.('credentials');
                      }}
                    >
                      Back to credentials
                    </Button>
                  )}
                  <Button variant="subtle" size="xs" onClick={handleLoad} loading={loading}>
                    Refresh model list
                  </Button>
                </Group>
              </>
            )}
          </Stack>
        </Card>
      )}
    </Stack>
  );
}

function buildDefaults(fields: ConfigField[]): Record<string, string> {
  const defaults: Record<string, string> = {};
  for (const f of fields) {
    if (f.default) defaults[f.key] = f.default;
  }
  return defaults;
}
