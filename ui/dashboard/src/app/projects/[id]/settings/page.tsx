'use client';

import { useCallback, useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';
import {
  ActionIcon, Alert, Button, Checkbox, CloseButton, Divider, Group, Loader, Modal, MultiSelect,
  NumberInput, Select, Stack, Switch, Tabs, Text, TextInput, Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle, IconPlus } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { BlurbLLMEditor, BlurbLLMState, emptyBlurbLLMState } from '@/components/BlurbLLMEditor';
import WarehouseConfigPanel from '@/components/projects/WarehouseConfigPanel';
import ProvidersPanel from '@/components/projects/ProvidersPanel';
import { useFormat } from '@/lib/format';
import { api, Project, ProviderMeta } from '@/lib/api';

// Output language choices rendered into prompt {{LANGUAGE}} substitutions
// on the agent + API side. The wire format is the human-readable name
// (the agent splices it directly into prompt text), so the value strings
// must match what the prompt directive expects.
const OUTPUT_LANGUAGE_OPTIONS = [
  { value: 'English',    label: 'English' },
  { value: 'Turkish',    label: 'Türkçe (Turkish)' },
  { value: 'German',     label: 'Deutsch (German)' },
  { value: 'Spanish',    label: 'Español (Spanish)' },
  { value: 'French',     label: 'Français (French)' },
  { value: 'Italian',    label: 'Italiano (Italian)' },
  { value: 'Portuguese', label: 'Português (Portuguese)' },
  { value: 'Dutch',      label: 'Nederlands (Dutch)' },
  { value: 'Polish',     label: 'Polski (Polish)' },
  { value: 'Russian',    label: 'Русский (Russian)' },
  { value: 'Arabic',     label: 'العربية (Arabic)' },
  { value: 'Hebrew',     label: 'עברית (Hebrew)' },
  { value: 'Japanese',   label: '日本語 (Japanese)' },
  { value: 'Korean',     label: '한국어 (Korean)' },
  { value: 'Chinese (Simplified)',   label: '简体中文 (Chinese — Simplified)' },
  { value: 'Chinese (Traditional)',  label: '繁體中文 (Chinese — Traditional)' },
  { value: 'Hindi',      label: 'हिन्दी (Hindi)' },
  { value: 'Indonesian', label: 'Bahasa Indonesia (Indonesian)' },
  { value: 'Vietnamese', label: 'Tiếng Việt (Vietnamese)' },
  { value: 'Thai',       label: 'ไทย (Thai)' },
];

// Validation verdicts that can qualify an insight for recommendation
// generation (Advanced → Recommendation eligibility). Values mirror the
// Go-side validation.Status per-claim taxonomy; the default is
// {confirmed, supported} = the historical recommender filter.
const RECOMMENDATION_VERDICT_DEFAULT = ['confirmed', 'supported'];
const RECOMMENDATION_VERDICT_VALUES = [
  'confirmed', 'supported', 'partial', 'unverifiable', 'rejected',
] as const;

export default function ProjectSettingsPage() {
  const t = useTranslations('settings');
  const { id } = useParams<{ id: string }>();
  const router = useRouter();

  const RECOMMENDATION_VERDICT_OPTIONS = RECOMMENDATION_VERDICT_VALUES.map((value) => ({
    value,
    label: t(`verdictOption_${value}`),
  }));

  const [project, setProject] = useState<Project | null>(null);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // When the deployment routes inference through a managed gateway, the
  // AI/Embedding + Blurb config is preset and immutable server-side, so we
  // hide those tabs. Default false (self-hosted / full provider choice);
  // the flag is fetched alongside the project and the whole Tabs block is
  // gated on `loading`, so there is no flash either way.
  const [aiConfigManaged, setAiConfigManaged] = useState(false);

  // General tab state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  // Output language for narrative fields (insight names/descriptions,
  // recommendation titles, /ask answers). Default "English" maps to the
  // legacy behavior — empty Project.Language on the wire.
  const [language, setLanguage] = useState<string>('English');
  const [savingGeneral, setSavingGeneral] = useState(false);

  // Profile tab state
  const [profile, setProfile] = useState<Record<string, Record<string, unknown>>>({});
  const [profileSchema, setProfileSchema] = useState<Record<string, unknown> | null>(null);
  const [savingProfile, setSavingProfile] = useState(false);

  // Blurb tab state
  const [blurb, setBlurb] = useState<BlurbLLMState>(emptyBlurbLLMState);
  const [savingBlurb, setSavingBlurb] = useState(false);

  // Advanced — local-only preference, not on the project document.
  const [debugLogsEnabled, setDebugLogsEnabled] = useState(false);

  // Advanced — Validation toggle (lives on the project document).
  // Defaults to true for legacy projects (validation_enabled === undefined).
  const [validationEnabled, setValidationEnabled] = useState(true);
  const [savingValidation, setSavingValidation] = useState(false);

  // Advanced — Smart overflow toggle (lives on the project document).
  // Defaults to true (smart_overflow_enabled === undefined).
  const [smartOverflowEnabled, setSmartOverflowEnabled] = useState(true);
  const [savingSmartOverflow, setSavingSmartOverflow] = useState(false);

  // Advanced — Clarifying questions toggle (lives on the project document).
  // Defaults to true (clarifying_questions_enabled === undefined) — opt-out.
  const [clarifyingQuestionsEnabled, setClarifyingQuestionsEnabled] = useState(true);
  const [savingClarifyingQuestions, setSavingClarifyingQuestions] = useState(false);

  // Advanced — Reflection / Discovery Ledger toggle (lives on the project
  // document). Defaults to true (reflection_enabled === undefined) — opt-out.
  const [reflectionEnabled, setReflectionEnabled] = useState(true);
  const [savingReflection, setSavingReflection] = useState(false);

  // Advanced — Enable reasoning toggle (model-agnostic, lives on the project
  // document). Defaults to false (reasoning_enabled === undefined) — opt-in.
  const [reasoningEnabled, setReasoningEnabled] = useState(false);
  const [savingReasoning, setSavingReasoning] = useState(false);

  // Advanced — Suggested questions toggle (lives on the project document).
  // Defaults to true (ask_suggestions_enabled === undefined) — opt-out; it
  // makes automatic LLM calls on insight / recommendation pages.
  const [askSuggestionsEnabled, setAskSuggestionsEnabled] = useState(true);
  const [savingAskSuggestions, setSavingAskSuggestions] = useState(false);

  // Advanced — Recommendation eligibility verdicts (lives on the project
  // document). Which validation verdicts qualify an insight for recommendation
  // generation. Empty / undefined → default {confirmed, supported}.
  const [recommendationVerdicts, setRecommendationVerdicts] = useState<string[]>(
    RECOMMENDATION_VERDICT_DEFAULT,
  );
  const [savingVerdicts, setSavingVerdicts] = useState(false);

  // Tab routing — honor `location.hash` so deep-links like
  // `/projects/:id/settings#advanced` open the right tab. The AI/Blurb
  // tabs drop out of the valid set in managed mode.
  const validTabs = [
    'general', 'warehouse',
    ...(aiConfigManaged ? [] : ['ai', 'blurb']),
    'profile', 'advanced',
  ];
  const [activeTab, setActiveTab] = useState<string>('general');
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const applyHash = () => {
      const h = window.location.hash.replace(/^#/, '');
      if (h && validTabs.includes(h)) setActiveTab(h);
    };
    applyHash();
    window.addEventListener('hashchange', applyHash);
    return () => window.removeEventListener('hashchange', applyHash);
    // validTabs is stable (literal); exhaustive-deps is noisy here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (typeof window === 'undefined' || !id) return;
    setDebugLogsEnabled(window.localStorage.getItem(`db:showDebugLogs:${id}`) === '1');
  }, [id]);

  useEffect(() => {
    Promise.all([
      api.getProject(id),
      api.listLLMProviders(),
      // Never let the capability probe break the settings page: managed
      // mode and this endpoint ship together, so a failure means an older
      // API without it — treat that as unmanaged (show config).
      api.getAppConfig().catch(() => ({ ai_config_managed: false })),
    ])
      .then(([proj, llmProvs, appConfig]) => {
        setProject(proj);
        setLlmProviders(llmProvs);
        setAiConfigManaged(appConfig.ai_config_managed);
        setName(proj.name);
        setDescription(proj.description || '');
        setLanguage(proj.language || 'English');
        setValidationEnabled(proj.validation_enabled !== false);
        setSmartOverflowEnabled(proj.smart_overflow_enabled !== false);
        setClarifyingQuestionsEnabled(proj.clarifying_questions_enabled !== false);
        setReflectionEnabled(proj.reflection_enabled !== false);
        setReasoningEnabled(proj.reasoning_enabled === true);
        setAskSuggestionsEnabled(proj.ask_suggestions_enabled !== false);
        setRecommendationVerdicts(
          proj.recommendation_verdicts && proj.recommendation_verdicts.length > 0
            ? proj.recommendation_verdicts
            : RECOMMENDATION_VERDICT_DEFAULT,
        );
        setProfile((proj.profile || {}) as Record<string, Record<string, unknown>>);
        if (proj.blurb_llm && proj.blurb_llm.provider) {
          const blurbProviderID = proj.blurb_llm.provider;
          const blurbMeta = llmProvs?.find((p) => p.id === blurbProviderID);
          const blurbMethods = blurbMeta?.auth_methods ?? [];
          setBlurb({
            enabled: true,
            provider: blurbProviderID,
            authMethod: blurbMethods.length === 1 ? blurbMethods[0].id : '',
            model: proj.blurb_llm.model || '',
            config: proj.blurb_llm.config || {},
            apiKey: '',
          });
        }
        if (proj.domain) {
          api.getProfileSchema(proj.domain, proj.category)
            .then(setProfileSchema)
            .catch(() => {});
        }
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [id]);

  const refreshProject = useCallback(async () => {
    try {
      const proj = await api.getProject(id);
      setProject(proj);
    } catch {
      // Refresh failures are non-fatal — the panel that just saved
      // already updated its own local state.
    }
  }, [id]);

  const breadcrumb = project
    ? [{ label: t('breadcrumbProjects'), href: '/' }, { label: project.name, href: `/projects/${id}` }, { label: t('breadcrumbSettings') }]
    : [{ label: t('breadcrumbSettings') }];

  if (loading) return <Shell><Loader /></Shell>;
  if (error) return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  if (!project) return <Shell><Text>{t('projectNotFound')}</Text></Shell>;

  const saveGeneral = async () => {
    setSavingGeneral(true);
    try {
      // Only send language when it actually differs from what's
      // currently stored. The server treats empty Language as "preserve
      // existing" (see projects.go merge), so omitting it leaves legacy
      // projects on their empty-Language => EffectiveLanguage()=English
      // path instead of rewriting them with an explicit value. We
      // compare against the displayed language (project.language ||
      // 'English') so a user reverting an explicit Turkish project back
      // to English does send 'English' and overwrites the stored value.
      const displayed = project.language || 'English';
      const payload: { name: string; description: string; language?: string } = { name, description };
      if (language !== displayed) {
        payload.language = language;
      }
      const saved = await api.updateProject(id, payload);
      setProject(saved);
      notifications.show({ title: t('toastSaved'), message: t('toastGeneralUpdated'), color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingGeneral(false);
    }
  };

  // Save the validation toggle. Optimistic update + rollback on failure
  // so the switch never lies about persisted state.
  const saveValidationEnabled = async (next: boolean) => {
    const prev = validationEnabled;
    setValidationEnabled(next);
    setSavingValidation(true);
    try {
      const saved = await api.updateProject(id, { validation_enabled: next });
      setProject(saved);
      setValidationEnabled(saved.validation_enabled !== false);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastValidationEnabled') : t('toastValidationDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setValidationEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingValidation(false);
    }
  };

  // Save the smart-overflow toggle. Optimistic update + rollback on failure.
  const saveSmartOverflowEnabled = async (next: boolean) => {
    const prev = smartOverflowEnabled;
    setSmartOverflowEnabled(next);
    setSavingSmartOverflow(true);
    try {
      const saved = await api.updateProject(id, { smart_overflow_enabled: next });
      setProject(saved);
      setSmartOverflowEnabled(saved.smart_overflow_enabled !== false);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastSmartOverflowEnabled') : t('toastSmartOverflowDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setSmartOverflowEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingSmartOverflow(false);
    }
  };

  // Save the clarifying-questions toggle. Optimistic update + rollback on failure.
  const saveClarifyingQuestionsEnabled = async (next: boolean) => {
    const prev = clarifyingQuestionsEnabled;
    setClarifyingQuestionsEnabled(next);
    setSavingClarifyingQuestions(true);
    try {
      const saved = await api.updateProject(id, { clarifying_questions_enabled: next });
      setProject(saved);
      setClarifyingQuestionsEnabled(saved.clarifying_questions_enabled !== false);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastClarifyingQuestionsEnabled') : t('toastClarifyingQuestionsDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setClarifyingQuestionsEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingClarifyingQuestions(false);
    }
  };

  // Save the reflection / Discovery Ledger toggle. Optimistic + rollback.
  const saveReflectionEnabled = async (next: boolean) => {
    const prev = reflectionEnabled;
    setReflectionEnabled(next);
    setSavingReflection(true);
    try {
      const saved = await api.updateProject(id, { reflection_enabled: next });
      setProject(saved);
      setReflectionEnabled(saved.reflection_enabled !== false);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastReflectionEnabled') : t('toastReflectionDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setReflectionEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingReflection(false);
    }
  };

  // Save the reasoning toggle. Optimistic update + rollback on failure.
  const saveReasoningEnabled = async (next: boolean) => {
    const prev = reasoningEnabled;
    setReasoningEnabled(next);
    setSavingReasoning(true);
    try {
      const saved = await api.updateProject(id, { reasoning_enabled: next });
      setProject(saved);
      setReasoningEnabled(saved.reasoning_enabled === true);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastReasoningEnabled') : t('toastReasoningDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setReasoningEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingReasoning(false);
    }
  };

  // Save the suggested-questions toggle. Optimistic update + rollback on failure.
  const saveAskSuggestionsEnabled = async (next: boolean) => {
    const prev = askSuggestionsEnabled;
    setAskSuggestionsEnabled(next);
    setSavingAskSuggestions(true);
    try {
      const saved = await api.updateProject(id, { ask_suggestions_enabled: next });
      setProject(saved);
      setAskSuggestionsEnabled(saved.ask_suggestions_enabled !== false);
      notifications.show({
        title: t('toastSaved'),
        message: next ? t('toastAskSuggestionsEnabled') : t('toastAskSuggestionsDisabled'),
        color: 'green',
      });
    } catch (e: unknown) {
      setAskSuggestionsEnabled(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingAskSuggestions(false);
    }
  };

  // Save the recommendation-eligibility verdicts. Optimistic update + rollback.
  // An empty selection is persisted as-is; the backend resolves empty back to
  // the default {confirmed, supported}, so deselecting all is not a footgun.
  const saveRecommendationVerdicts = async (next: string[]) => {
    const prev = recommendationVerdicts;
    setRecommendationVerdicts(next);
    setSavingVerdicts(true);
    try {
      const saved = await api.updateProject(id, { recommendation_verdicts: next });
      setProject(saved);
      setRecommendationVerdicts(
        saved.recommendation_verdicts && saved.recommendation_verdicts.length > 0
          ? saved.recommendation_verdicts
          : RECOMMENDATION_VERDICT_DEFAULT,
      );
      notifications.show({ title: t('toastSaved'), message: t('toastVerdictsUpdated'), color: 'green' });
    } catch (e: unknown) {
      setRecommendationVerdicts(prev);
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingVerdicts(false);
    }
  };

  const saveProfile = async () => {
    setSavingProfile(true);
    try {
      const saved = await api.updateProject(id, { profile });
      setProject(saved);
      notifications.show({ title: t('toastSaved'), message: t('toastProfileUpdated'), color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingProfile(false);
    }
  };

  const saveBlurb = async () => {
    setSavingBlurb(true);
    try {
      const saved = await api.updateProject(id, {
        blurb_llm:
          blurb.enabled && blurb.provider && blurb.model
            ? {
                provider: blurb.provider,
                model: blurb.model,
                config: {
                  ...Object.fromEntries(
                    Object.entries(blurb.config).filter(([k]) => k !== 'model' && k !== 'api_key'),
                  ),
                  ...(blurb.authMethod ? { auth_method: blurb.authMethod } : {}),
                },
              }
            : undefined,
      });
      if (blurb.enabled && blurb.apiKey) {
        await api.setSecret(id, 'blurb-llm-credentials', blurb.apiKey);
        setBlurb((prev) => ({ ...prev, apiKey: '' }));
      }
      setProject(saved);
      notifications.show({ title: t('toastSaved'), message: t('toastBlurbUpdated'), color: 'green' });
    } catch (e: unknown) {
      notifications.show({ title: t('toastError'), message: (e as Error).message, color: 'red' });
    } finally {
      setSavingBlurb(false);
    }
  };

  return (
    <Shell breadcrumb={breadcrumb}>
      <Tabs
        value={validTabs.includes(activeTab) ? activeTab : 'general'}
        onChange={(v) => { if (v) setActiveTab(v); }}
        styles={{
          tab: { fontSize: 13, fontWeight: 500, padding: '8px 16px' },
          panel: { paddingTop: 20 },
        }}
      >
        <Tabs.List>
          <Tabs.Tab value="general">{t('tabGeneral')}</Tabs.Tab>
          <Tabs.Tab value="warehouse">{t('tabWarehouse')}</Tabs.Tab>
          {!aiConfigManaged && <Tabs.Tab value="ai">{t('tabAi')}</Tabs.Tab>}
          {!aiConfigManaged && <Tabs.Tab value="blurb">{t('tabBlurb')}</Tabs.Tab>}
          {profileSchema && <Tabs.Tab value="profile">{t('tabProfile')}</Tabs.Tab>}
          <Tabs.Tab value="advanced">{t('tabAdvanced')}</Tabs.Tab>
        </Tabs.List>

        <Tabs.Panel value="general">
          <SettingsSection>
            <TextInput label={t('projectNameLabel')} required value={name} onChange={(e) => setName(e.target.value)} />
            <Textarea label={t('descriptionLabel')} value={description} onChange={(e) => setDescription(e.target.value)} />
            <Group>
              <TextInput label={t('domainLabel')} value={project.domain} disabled style={{ flex: 1 }} />
              <TextInput label={t('categoryLabel')} value={project.category} disabled style={{ flex: 1 }} />
            </Group>
            <Select
              label={t('outputLanguageLabel')}
              description={t('outputLanguageDescription')}
              value={language}
              onChange={(v) => setLanguage(v || 'English')}
              data={OUTPUT_LANGUAGE_OPTIONS}
              allowDeselect={false}
              searchable
            />
            <Group justify="flex-end">
              <Button onClick={saveGeneral} loading={savingGeneral}>{t('saveGeneralButton')}</Button>
            </Group>
          </SettingsSection>
        </Tabs.Panel>

        <Tabs.Panel value="warehouse">
          <WarehouseConfigPanel projectId={id} variant="page" onSaved={() => { void refreshProject(); }} />
        </Tabs.Panel>

        {!aiConfigManaged && (
        <Tabs.Panel value="ai">
          <ProvidersPanel projectId={id} variant="page" onSaved={() => { void refreshProject(); }} />
        </Tabs.Panel>
        )}

        {!aiConfigManaged && (
        <Tabs.Panel value="blurb">
          <SettingsSection>
            <Text size="sm" fw={500}>{t('blurbModelHeading')}</Text>
            <Text size="xs" c="dimmed" mb="sm">
              {t('blurbModelDescription')}
            </Text>
            <BlurbLLMEditor
              llmProviders={llmProviders}
              value={blurb}
              onChange={(next) => setBlurb(next)}
              startInModelPhase={!!project?.blurb_llm?.provider}
              projectId={id}
              savedProvider={project?.blurb_llm?.provider || project?.llm?.provider}
            />
            <Group justify="flex-end">
              <Button onClick={saveBlurb} loading={savingBlurb}>{t('saveBlurbButton')}</Button>
            </Group>
          </SettingsSection>
        </Tabs.Panel>
        )}

        {profileSchema && (
          <Tabs.Panel value="profile">
            <SettingsSection>
              <Text size="xs" c="dimmed" mb="md">
                {t('profileDescription')}
              </Text>
              <ProfileEditor schema={profileSchema} profile={profile} onChange={setProfile} />
              <Group justify="flex-end">
                <Button onClick={saveProfile} loading={savingProfile}>{t('saveProfileButton')}</Button>
              </Group>
            </SettingsSection>
          </Tabs.Panel>
        )}

        <Tabs.Panel value="advanced">
          <SettingsSection>
            <Stack gap="sm">
              <Text size="sm" fw={500}>{t('validationHeading')}</Text>
              <Switch
                label={t('validationSwitchLabel')}
                description={t('validationSwitchDescription')}
                checked={validationEnabled}
                disabled={savingValidation}
                onChange={(e) => saveValidationEnabled(e.currentTarget.checked)}
              />
              <Switch
                label={t('smartOverflowSwitchLabel')}
                description={t('smartOverflowSwitchDescription')}
                checked={smartOverflowEnabled}
                disabled={savingSmartOverflow}
                onChange={(e) => saveSmartOverflowEnabled(e.currentTarget.checked)}
              />
              <Switch
                label={t('clarifyingQuestionsSwitchLabel')}
                description={t('clarifyingQuestionsSwitchDescription')}
                checked={clarifyingQuestionsEnabled}
                disabled={savingClarifyingQuestions}
                onChange={(e) => saveClarifyingQuestionsEnabled(e.currentTarget.checked)}
              />
              <Switch
                label={t('reflectionSwitchLabel')}
                description={t('reflectionSwitchDescription')}
                checked={reflectionEnabled}
                disabled={savingReflection}
                onChange={(e) => saveReflectionEnabled(e.currentTarget.checked)}
              />
              <Switch
                label={t('reasoningSwitchLabel')}
                description={t('reasoningSwitchDescription')}
                checked={reasoningEnabled}
                disabled={savingReasoning}
                onChange={(e) => saveReasoningEnabled(e.currentTarget.checked)}
              />
              <Switch
                label={t('askSuggestionsSwitchLabel')}
                description={t('askSuggestionsSwitchDescription')}
                checked={askSuggestionsEnabled}
                disabled={savingAskSuggestions}
                onChange={(e) => saveAskSuggestionsEnabled(e.currentTarget.checked)}
              />
              <MultiSelect
                label={t('recommendationEligibilityLabel')}
                description={t('recommendationEligibilityDescription')}
                data={RECOMMENDATION_VERDICT_OPTIONS}
                value={recommendationVerdicts}
                disabled={savingVerdicts}
                onChange={saveRecommendationVerdicts}
                clearable
              />
              <Divider my="xs" />
              <Text size="sm" fw={500}>{t('debuggingHeading')}</Text>
              <Switch
                label={t('debugLogsSwitchLabel')}
                description={t('debugLogsSwitchDescription')}
                checked={debugLogsEnabled}
                onChange={(e) => {
                  const next = e.currentTarget.checked;
                  setDebugLogsEnabled(next);
                  if (typeof window !== 'undefined' && id) {
                    window.localStorage.setItem(`db:showDebugLogs:${id}`, next ? '1' : '0');
                  }
                }}
              />
              <Text size="xs" c="dimmed">
                {t('debugLogsFootnote')}
              </Text>
              <Divider my="xs" />
              <Text size="sm" fw={500}>{t('schemaCacheHeading')}</Text>
              <Text size="xs" c="dimmed">
                {t.rich('schemaCacheDescription', { strong: (chunks) => <strong>{chunks}</strong> })}
              </Text>
              {id && <ClearSchemaCacheButton projectId={id} />}
              <Divider my="md" />
              <Text size="sm" fw={500} c="red">{t('dangerZoneHeading')}</Text>
              <Text size="xs" c="dimmed">
                {t.rich('dangerZoneDescription', { strong: (chunks) => <strong>{chunks}</strong> })}
              </Text>
              {id && project && (
                <DeleteProjectButton projectId={id} projectName={project.name || id} />
              )}
            </Stack>
          </SettingsSection>
        </Tabs.Panel>
      </Tabs>
    </Shell>
  );

  // suppress unused warning when router isn't wired into a tab yet
  void router;
}

// Renders the relative "…ago" cache timestamp through the settings namespace's
// ICU plural messages so pluralisation follows the active UI locale.
function formatRelativeTime(rfc3339: string, t: ReturnType<typeof useTranslations>): string {
  const ms = new Date(rfc3339).getTime();
  if (Number.isNaN(ms)) return rfc3339;
  const seconds = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (seconds < 60) return t('relativeJustNow');
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return t('relativeMinutes', { count: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t('relativeHours', { count: hours });
  const days = Math.floor(hours / 24);
  if (days < 30) return t('relativeDays', { count: days });
  const months = Math.floor(days / 30);
  return t('relativeMonths', { count: months });
}

function ClearSchemaCacheButton({ projectId }: { projectId: string }) {
  const t = useTranslations('settings');
  const fmt = useFormat();
  const [opened, setOpened] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [info, setInfo] = useState<{ cached: boolean; last?: string } | null>(null);

  const refreshInfo = useCallback(async () => {
    try {
      const res = await api.getSchemaCacheInfo(projectId);
      setInfo({ cached: res.cached, last: res.last_cached_at });
    } catch {
      setInfo({ cached: false });
    }
  }, [projectId]);

  useEffect(() => { void refreshInfo(); }, [refreshInfo]);

  const handleConfirm = async () => {
    setSubmitting(true);
    try {
      await api.invalidateSchemaCache(projectId);
      notifications.show({
        title: t('schemaCacheClearedTitle'),
        message: t('schemaCacheClearedMessage'),
        color: 'green',
      });
      setOpened(false);
      void refreshInfo();
    } catch (e: unknown) {
      notifications.show({ title: t('schemaCacheClearErrorTitle'), message: (e as Error).message, color: 'red' });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Group align="center">
        <Button variant="default" color="orange" onClick={() => setOpened(true)}>
          {t('clearSchemaCacheButton')}
        </Button>
        <Text size="xs" c="dimmed">
          {info === null
            ? t('cacheInfoLoading')
            : info.cached && info.last
              ? t('cacheInfoLastCached', {
                  relative: formatRelativeTime(info.last, t),
                  absolute: fmt.dateTime(info.last, { dateStyle: 'medium', timeStyle: 'short' }),
                })
              : t('cacheInfoNone')}
        </Text>
      </Group>
      <Modal
        opened={opened}
        onClose={() => { if (!submitting) setOpened(false); }}
        title={t('clearSchemaCacheModalTitle')}
        centered
      >
        <Stack gap="sm">
          <Text size="sm">{t('clearSchemaCacheModalIntro')}</Text>
          <ul style={{ margin: 0, paddingLeft: 20, fontSize: 14 }}>
            <li>{t('clearSchemaCacheBullet1')}</li>
            <li>{t('clearSchemaCacheBullet2')}</li>
            <li>{t.rich('clearSchemaCacheBullet3', { strong: (chunks) => <strong>{chunks}</strong> })}</li>
          </ul>
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={() => setOpened(false)} disabled={submitting}>
              {t('cancelButton')}
            </Button>
            <Button color="orange" onClick={handleConfirm} loading={submitting}>
              {t('confirmClearCacheButton')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function DeleteProjectButton({ projectId, projectName }: { projectId: string; projectName: string }) {
  const t = useTranslations('settings');
  const router = useRouter();
  const [opened, setOpened] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [confirmText, setConfirmText] = useState('');

  const matches = confirmText === projectName;

  const handleConfirm = async () => {
    setSubmitting(true);
    try {
      const res = await api.deleteProject(projectId);
      notifications.show({
        title: t('deleteSuccessTitle'),
        message: t('deleteSuccessMessage', { name: projectName }),
        color: 'green',
      });
      if (res.secrets_skipped) {
        notifications.show({
          title: t('deleteSecretsTitle'),
          message: t('deleteSecretsMessage'),
          color: 'yellow',
          autoClose: 12000,
        });
      }
      router.push('/projects');
    } catch (e: unknown) {
      notifications.show({ title: t('deleteErrorTitle'), message: (e as Error).message, color: 'red', autoClose: 8000 });
      setSubmitting(false);
    }
  };

  return (
    <>
      <Group>
        <Button color="red" onClick={() => { setConfirmText(''); setOpened(true); }}>
          {t('deleteProjectButton')}
        </Button>
      </Group>
      <Modal
        opened={opened}
        onClose={() => { if (!submitting) setOpened(false); }}
        title={<Text fw={600} c="red">{t('deleteModalTitle')}</Text>}
        centered
      >
        <Stack gap="sm">
          <Text size="sm">
            {t.rich('deleteModalBody', { name: projectName, strong: (chunks) => <strong>{chunks}</strong> })}
          </Text>
          <TextInput
            label={t.rich('deleteConfirmLabel', { name: projectName, strong: (chunks) => <strong>{chunks}</strong> })}
            value={confirmText}
            onChange={(e) => setConfirmText(e.currentTarget.value)}
            placeholder={projectName}
            disabled={submitting}
            data-autofocus
          />
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={() => setOpened(false)} disabled={submitting}>
              {t('cancelButton')}
            </Button>
            <Button color="red" onClick={handleConfirm} loading={submitting} disabled={!matches}>
              {t('deleteProjectButton')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

function SettingsSection({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: '20px',
      maxWidth: 640,
    }}>
      <Stack gap="md">{children}</Stack>
    </div>
  );
}

function ProfileEditor({ schema, profile, onChange }: {
  schema: Record<string, unknown>;
  profile: Record<string, Record<string, unknown>>;
  onChange: (profile: Record<string, Record<string, unknown>>) => void;
}) {
  const t = useTranslations('settings');
  const properties = (schema as { properties?: Record<string, unknown> }).properties || {};

  const updateField = (section: string, field: string, value: unknown) => {
    onChange({
      ...profile,
      [section]: { ...(profile[section] || {}), [field]: value },
    });
  };

  const updateSection = (section: string, value: unknown) => {
    onChange({ ...profile, [section]: value as Record<string, unknown> });
  };

  return (
    <Stack gap="md">
      {Object.entries(properties).map(([sectionKey, sectionSchema]) => {
        const sec = sectionSchema as {
          title?: string; type?: string;
          properties?: Record<string, unknown>;
          items?: Record<string, unknown>;
        };

        if (sec.type === 'array' && sec.items && (sec.items as Record<string, unknown>).type === 'object') {
          const items = (Array.isArray(profile[sectionKey]) ? profile[sectionKey] : []) as Record<string, unknown>[];
          const itemSchema = sec.items as { properties?: Record<string, unknown> };
          return (
            <ArrayOfObjectsEditor key={sectionKey} title={sec.title || sectionKey}
              itemSchema={itemSchema} items={items}
              onChange={(newItems) => updateSection(sectionKey, newItems)} />
          );
        }

        if (sec.type === 'array') {
          const items = (Array.isArray(profile[sectionKey]) ? profile[sectionKey] : []) as string[];
          return (
            <div key={sectionKey}>
              <Text size="sm" fw={600} mb="xs">{sec.title || sectionKey}</Text>
              <TextInput size="xs" description={t('commaSeparatedValues')}
                value={items.join(', ')}
                onChange={(e) => updateSection(sectionKey, e.target.value.split(',').map(s => s.trim()).filter(Boolean))} />
            </div>
          );
        }

        if (!sec.properties) return null;
        return (
          <div key={sectionKey}>
            <Text size="sm" fw={600} mb="xs">{sec.title || sectionKey}</Text>
            <Stack gap="xs">
              {Object.entries(sec.properties).map(([fieldKey, fieldSchema]) => (
                <SchemaField key={fieldKey} fieldKey={fieldKey} fieldSchema={fieldSchema}
                  value={(profile[sectionKey] || {})[fieldKey]}
                  onChange={(v) => updateField(sectionKey, fieldKey, v)} />
              ))}
            </Stack>
          </div>
        );
      })}
    </Stack>
  );
}

function SchemaField({ fieldKey, fieldSchema, value, onChange }: {
  fieldKey: string; fieldSchema: unknown; value: unknown;
  onChange: (v: unknown) => void;
}) {
  const t = useTranslations('settings');
  const fs = fieldSchema as {
    type?: string; title?: string; description?: string;
    enum?: string[]; items?: { type?: string; enum?: string[]; properties?: Record<string, unknown> };
  };

  if (fs.type === 'string' && fs.enum) {
    return (
      <Select label={fs.title || fieldKey} description={fs.description}
        data={fs.enum} value={(value as string) || null} clearable size="xs"
        onChange={(v) => onChange(v || '')} />
    );
  }
  if (fs.type === 'array' && fs.items?.enum) {
    return (
      <MultiSelect label={fs.title || fieldKey} description={fs.description}
        data={fs.items.enum} value={(value as string[]) || []} size="xs"
        onChange={(v) => onChange(v)} />
    );
  }
  if (fs.type === 'array' && fs.items?.type === 'string') {
    const items = (Array.isArray(value) ? value : []) as string[];
    return (
      <TextInput label={fs.title || fieldKey} description={fs.description || t('commaSeparated')}
        value={items.join(', ')} size="xs"
        onChange={(e) => onChange(e.target.value.split(',').map(s => s.trim()).filter(Boolean))} />
    );
  }
  if (fs.type === 'array' && fs.items?.type === 'object') {
    const itemSchema = fs.items as { properties?: Record<string, unknown> };
    const items = (Array.isArray(value) ? value : []) as Record<string, unknown>[];
    return (
      <InlineArrayEditor title={fs.title || fieldKey} itemSchema={itemSchema}
        items={items} onChange={onChange} />
    );
  }
  if (fs.type === 'boolean') {
    return (
      <Checkbox label={fs.title || fieldKey} description={fs.description}
        checked={!!value} size="xs"
        onChange={(e) => onChange(e.currentTarget.checked)} />
    );
  }
  if (fs.type === 'number' || fs.type === 'integer') {
    return (
      <NumberInput label={fs.title || fieldKey} description={fs.description}
        value={(value as number) ?? ''} size="xs"
        onChange={(v) => onChange(v)} />
    );
  }
  return (
    <TextInput label={fs.title || fieldKey} description={fs.description}
      value={(value as string) || ''} size="xs"
      onChange={(e) => onChange(e.target.value)} />
  );
}

function ArrayOfObjectsEditor({ title, itemSchema, items, onChange }: {
  title: string;
  itemSchema: { properties?: Record<string, unknown> };
  items: Record<string, unknown>[];
  onChange: (items: Record<string, unknown>[]) => void;
}) {
  const t = useTranslations('settings');
  const addItem = () => onChange([...items, {}]);
  const removeItem = (idx: number) => onChange(items.filter((_, i) => i !== idx));
  const updateItem = (idx: number, field: string, value: unknown) => {
    const updated = [...items];
    updated[idx] = { ...updated[idx], [field]: value };
    onChange(updated);
  };

  const fields = itemSchema.properties || {};

  return (
    <div>
      <Group justify="space-between" mb="xs">
        <Text size="sm" fw={600}>{t('titleWithCount', { title, count: items.length })}</Text>
        <ActionIcon variant="light" size="sm" onClick={addItem}>
          <IconPlus size={14} />
        </ActionIcon>
      </Group>
      <Stack gap="sm">
        {items.map((item, idx) => (
          <div key={idx} style={{
            border: '1px solid var(--db-border-default)',
            borderRadius: 'var(--db-radius-lg)',
            padding: 16, background: 'var(--db-bg-muted)',
          }}>
            <Group justify="space-between" mb={8}>
              <Text size="xs" fw={500} c="dimmed">#{idx + 1}</Text>
              <CloseButton size="xs" onClick={() => removeItem(idx)} />
            </Group>
            <div style={{
              display: 'grid',
              gridTemplateColumns: 'repeat(2, 1fr)',
              gap: 12,
            }}>
              {Object.entries(fields).map(([fieldKey, fieldSchema]) => {
                const fs = fieldSchema as { type?: string; title?: string };
                const isWide = fs.type === 'array' || fieldKey === 'description' || fieldKey === 'name';
                return (
                  <div key={fieldKey} style={{ gridColumn: isWide ? '1 / -1' : undefined }}>
                    <SchemaField fieldKey={fieldKey} fieldSchema={fieldSchema}
                      value={item[fieldKey]}
                      onChange={(v) => updateItem(idx, fieldKey, v)} />
                  </div>
                );
              })}
            </div>
          </div>
        ))}
        {items.length === 0 && (
          <div style={{
            border: '2px dashed var(--db-border-strong)',
            borderRadius: 'var(--db-radius)',
            padding: '20px', textAlign: 'center',
          }}>
            <Text size="xs" c="dimmed">{t('noItemsYet')}</Text>
          </div>
        )}
      </Stack>
    </div>
  );
}

function InlineArrayEditor({ title, itemSchema, items, onChange }: {
  title: string;
  itemSchema: { properties?: Record<string, unknown> };
  items: Record<string, unknown>[];
  onChange: (items: unknown) => void;
}) {
  const t = useTranslations('settings');
  const fields = itemSchema.properties || {};
  const fieldEntries = Object.entries(fields);
  const addItem = () => onChange([...items, {}]);
  const removeItem = (idx: number) => onChange(items.filter((_, i) => i !== idx));
  const updateItem = (idx: number, field: string, value: unknown) => {
    const updated = [...items];
    updated[idx] = { ...updated[idx], [field]: value };
    onChange(updated);
  };

  return (
    <div>
      <Group justify="space-between" mb={6}>
        <Text size="xs" fw={600}>{title}</Text>
        <ActionIcon variant="subtle" size="xs" onClick={addItem}>
          <IconPlus size={12} />
        </ActionIcon>
      </Group>

      {items.length > 0 && (
        <Group gap={8} mb={4} wrap="nowrap" style={{ paddingRight: 28 }}>
          {fieldEntries.map(([fk, fs]) => {
            const f = fs as { title?: string; type?: string };
            const isNumber = f.type === 'integer' || f.type === 'number';
            return (
              <Text key={fk} size="xs" c="dimmed" fw={500}
                style={{ flex: isNumber ? 1 : 2, fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.3px' }}>
                {f.title || fk}
              </Text>
            );
          })}
        </Group>
      )}

      <Stack gap={6}>
        {items.map((item, idx) => (
          <Group key={idx} gap={8} wrap="nowrap" align="center">
            {fieldEntries.map(([fk, fs]) => {
              const f = fs as { type?: string; title?: string };
              if (f.type === 'integer' || f.type === 'number') {
                return (
                  <NumberInput key={fk} placeholder={f.title || fk} size="xs"
                    value={(item[fk] as number) ?? ''} style={{ flex: 1 }}
                    onChange={(v) => updateItem(idx, fk, v)} />
                );
              }
              return (
                <TextInput key={fk} placeholder={f.title || fk} size="xs"
                  value={(item[fk] as string) || ''} style={{ flex: 2 }}
                  onChange={(e) => updateItem(idx, fk, e.target.value)} />
              );
            })}
            <CloseButton size="xs" onClick={() => removeItem(idx)} />
          </Group>
        ))}
      </Stack>

      {items.length === 0 && (
        <Text size="xs" c="dimmed" ta="center" py="xs">{t('noItems')}</Text>
      )}
    </div>
  );
}
