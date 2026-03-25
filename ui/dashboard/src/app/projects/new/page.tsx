'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, Select, Stack, Stepper, Text, TextInput, Textarea, Title, NumberInput, Switch,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, Domain, Category, ProviderMeta, ConfigField } from '@/lib/api';

export default function NewProjectPage() {
  const router = useRouter();
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);

  // Data from API (dynamic)
  const [domains, setDomains] = useState<Domain[]>([]);
  const [warehouseProviders, setWarehouseProviders] = useState<ProviderMeta[]>([]);
  const [llmProviders, setLlmProviders] = useState<ProviderMeta[]>([]);
  const [dataLoading, setDataLoading] = useState(true);
  const [dataError, setDataError] = useState<string | null>(null);

  // Form state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [domain, setDomain] = useState('');
  const [category, setCategory] = useState('');
  const [warehouseProvider, setWarehouseProvider] = useState('');
  const [warehouseConfig, setWarehouseConfig] = useState<Record<string, string>>({});
  const [warehouseAuthMethod, setWarehouseAuthMethod] = useState('');
  const [warehouseCredential, setWarehouseCredential] = useState('');
  const [filterField, setFilterField] = useState('');
  const [filterValue, setFilterValue] = useState('');
  const [llmProvider, setLlmProvider] = useState('');
  const [llmConfig, setLlmConfig] = useState<Record<string, string>>({});
  const [llmApiKey, setLlmApiKey] = useState('');
  const [scheduleEnabled, setScheduleEnabled] = useState(true);
  const [scheduleCron, setScheduleCron] = useState('0 2 * * *');
  const [maxSteps, setMaxSteps] = useState(100);

  useEffect(() => {
    Promise.all([
      api.listDomains(),
      api.listWarehouseProviders(),
      api.listLLMProviders(),
    ])
      .then(([domainsData, whProviders, llmProvs]) => {
        setDomains(domainsData);
        setWarehouseProviders(whProviders);
        setLlmProviders(llmProvs);

        if (domainsData.length === 1) {
          setDomain(domainsData[0].id);
          if (domainsData[0].categories.length === 1) setCategory(domainsData[0].categories[0].id);
        }
        if (whProviders.length > 0) {
          setWarehouseProvider(whProviders[0].id);
          setWarehouseConfig(buildDefaults(whProviders[0].config_fields));
          if (whProviders[0].auth_methods?.length === 1) setWarehouseAuthMethod(whProviders[0].auth_methods[0].id);
        }
        if (llmProvs.length > 0) {
          const claude = llmProvs.find((p) => p.id === 'claude');
          const first = claude || llmProvs[0];
          setLlmProvider(first.id);
          setLlmConfig(buildDefaults(first.config_fields));
        }
      })
      .catch((e) => setDataError(e.message))
      .finally(() => setDataLoading(false));
  }, []);

  const categories: Category[] = domains.find((d) => d.id === domain)?.categories || [];
  const selectedWarehouse = warehouseProviders.find((p) => p.id === warehouseProvider);
  const selectedLLM = llmProviders.find((p) => p.id === llmProvider);

  const whAuthMethods = selectedWarehouse?.auth_methods || [];
  const selectedAuthMethod = whAuthMethods.find((m) => m.id === warehouseAuthMethod);
  const authFields = selectedAuthMethod?.fields || [];
  const authCredentialField = authFields.find((f) => f.type === 'credential');
  const authNeedsCredential = authCredentialField?.required ?? false;
  const authConfigFields = authFields.filter((f) => f.type !== 'credential');
  const llmNeedsApiKey = selectedLLM?.config_fields.some((f) => f.key === 'api_key') ?? false;

  const canProceed = [
    () => name && domain && category,
    () => warehouseProvider && warehouseConfig['dataset'] && (whAuthMethods.length === 0 || warehouseAuthMethod) && (!authNeedsCredential || warehouseCredential),
    () => llmProvider && llmConfig['model'] && (!llmNeedsApiKey || llmApiKey),
    () => true,
  ];

  const handleCreate = async () => {
    setLoading(true);
    try {
      const project = await api.createProject({
        name, description, domain, category,
        warehouse: {
          provider: warehouseProvider,
          project_id: warehouseConfig['project_id'] || '',
          datasets: (warehouseConfig['dataset'] || '').split(',').map((d) => d.trim()).filter(Boolean),
          location: warehouseConfig['location'] || '',
          filter_field: filterField,
          filter_value: filterValue,
          config: {
            ...Object.fromEntries(
              Object.entries(warehouseConfig).filter(([k]) => k !== 'project_id' && k !== 'location' && k !== 'dataset')
            ),
            ...(warehouseAuthMethod ? { auth_method: warehouseAuthMethod } : {}),
          },
        },
        llm: {
          provider: llmProvider,
          model: llmConfig['model'] || '',
          config: Object.fromEntries(
            Object.entries(llmConfig).filter(([k]) => k !== 'model' && k !== 'api_key')
          ),
        },
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
      });
      // Save secrets
      if (llmApiKey && project.id) {
        await api.setSecret(project.id, 'llm-api-key', llmApiKey);
      }
      if (warehouseCredential && project.id) {
        await api.setSecret(project.id, 'warehouse-credentials', warehouseCredential);
      }

      notifications.show({ title: 'Project created', message: project.name, color: 'green' });
      router.push(`/projects/${project.id}`);
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setLoading(false);
    }
  };

  return (
    <Shell>
      <Stack gap="lg" maw={700}>
        <Title order={2}>New Project</Title>

        {dataError && (
          <Alert icon={<IconAlertCircle size={16} />} title="Cannot load configuration" color="red">{dataError}</Alert>
        )}

        {dataLoading && (
          <Group><Loader size="sm" /><Text size="sm" c="dimmed">Loading configuration...</Text></Group>
        )}

        {!dataLoading && !dataError && (
          <>
            <Stepper active={active} onStepClick={setActive}>
              <Stepper.Step label="Basics" description="Name and domain">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="My Game Analytics" />
                    <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional description" />
                    <Select label="Domain" required placeholder="Select a domain"
                      data={domains.map((d) => ({ value: d.id, label: d.id.charAt(0).toUpperCase() + d.id.slice(1) }))}
                      value={domain} onChange={(v) => { setDomain(v || ''); setCategory(''); }} />
                    {domain && categories.length > 0 && (
                      <Select label="Category" required placeholder="Select a category"
                        data={categories.map((c) => ({ value: c.id, label: c.name }))}
                        value={category} onChange={(v) => setCategory(v || '')} />
                    )}
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Warehouse" description="Data source">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Select label="Warehouse Provider" required placeholder="Select warehouse"
                      data={warehouseProviders.map((p) => ({ value: p.id, label: p.name }))}
                      value={warehouseProvider}
                      onChange={(v) => {
                        setWarehouseProvider(v || '');
                        setWarehouseAuthMethod('');
                        setWarehouseCredential('');
                        const prov = warehouseProviders.find((p) => p.id === v);
                        if (prov) {
                          setWarehouseConfig(buildDefaults(prov.config_fields));
                          if (prov.auth_methods?.length === 1) setWarehouseAuthMethod(prov.auth_methods[0].id);
                        }
                      }} />
                    {selectedWarehouse && (
                      <Text size="xs" c="dimmed">{selectedWarehouse.description}</Text>
                    )}

                    {selectedWarehouse?.config_fields.map((field) => (
                      <DynamicField key={field.key} field={field}
                        value={warehouseConfig[field.key] || ''}
                        onChange={(val) => setWarehouseConfig((prev) => ({ ...prev, [field.key]: val }))} />
                    ))}

                    {whAuthMethods.length > 0 && (
                      <Select key={`auth-${warehouseProvider}`} label="Authentication" required placeholder="Select auth method"
                        data={whAuthMethods.map((m) => ({ value: m.id, label: m.name }))}
                        value={warehouseAuthMethod}
                        onChange={(v) => { setWarehouseAuthMethod(v || ''); setWarehouseCredential(''); }} />
                    )}

                    {selectedAuthMethod?.description && (
                      <Text size="xs" c="dimmed">{selectedAuthMethod.description}</Text>
                    )}

                    {authConfigFields.map((field) => (
                      <DynamicField key={field.key} field={field}
                        value={warehouseConfig[field.key] || ''}
                        onChange={(val) => setWarehouseConfig((prev) => ({ ...prev, [field.key]: val }))} />
                    ))}

                    {authCredentialField && (
                      <Textarea
                        label={authCredentialField.label}
                        required={authCredentialField.required}
                        placeholder={authCredentialField.placeholder}
                        description={(authCredentialField.description || '') + ' Stored encrypted.'}
                        value={warehouseCredential}
                        onChange={(e) => setWarehouseCredential(e.target.value)}
                        minRows={3}
                        autosize
                        styles={{ input: { fontFamily: 'monospace', fontSize: '13px' } }}
                      />
                    )}

                    <Text size="sm" fw={600} mt="sm">Filter (optional)</Text>
                    <Text size="xs" c="dimmed">For shared datasets. Leave empty if the entire dataset is yours.</Text>
                    <Group grow>
                      <TextInput label="Filter Field" placeholder="e.g. app_id" value={filterField}
                        onChange={(e) => setFilterField(e.target.value)} />
                      <TextInput label="Filter Value" placeholder="e.g. my-app-123" value={filterValue}
                        onChange={(e) => setFilterValue(e.target.value)} />
                    </Group>
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="AI" description="LLM provider">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Select label="LLM Provider" required placeholder="Select LLM provider"
                      data={llmProviders.map((p) => ({ value: p.id, label: p.name }))}
                      value={llmProvider}
                      onChange={(v) => {
                        setLlmProvider(v || '');
                        setLlmApiKey('');
                        const prov = llmProviders.find((p) => p.id === v);
                        if (prov) setLlmConfig(buildDefaults(prov.config_fields));
                      }} />
                    {selectedLLM && (
                      <Text size="xs" c="dimmed">{selectedLLM.description}</Text>
                    )}

                    {selectedLLM?.config_fields
                      .filter((f) => f.key !== 'api_key')
                      .map((field) => (
                        <DynamicField key={field.key} field={field}
                          value={llmConfig[field.key] || ''}
                          onChange={(val) => setLlmConfig((prev) => ({ ...prev, [field.key]: val }))} />
                      ))}

                    {llmNeedsApiKey && (
                      <TextInput label="API Key" required type="password"
                        placeholder={selectedLLM?.config_fields.find((f) => f.key === 'api_key')?.placeholder || 'Enter API key'}
                        value={llmApiKey} onChange={(e) => setLlmApiKey(e.target.value)}
                        description="Stored encrypted. Never exposed in full." />
                    )}

                    {!llmNeedsApiKey && (
                      <Text size="xs" c="dimmed">
                        This provider uses cloud credentials (IAM / ADC). No API key needed.
                      </Text>
                    )}
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Step label="Schedule" description="Discovery schedule">
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Switch label="Enable automatic discovery" checked={scheduleEnabled}
                      onChange={(e) => setScheduleEnabled(e.currentTarget.checked)} />
                    {scheduleEnabled && (
                      <TextInput label="Cron Expression" value={scheduleCron}
                        onChange={(e) => setScheduleCron(e.target.value)} description="Default: daily at 2 AM UTC" />
                    )}
                    <NumberInput label="Max Exploration Steps" value={maxSteps}
                      onChange={(v) => setMaxSteps(Number(v) || 100)} min={10} max={500} />
                  </Stack>
                </Card>
              </Stepper.Step>

              <Stepper.Completed>
                <Card withBorder p="lg" mt="md">
                  <Stack>
                    <Title order={4}>Ready to create</Title>
                    <Text><strong>Name:</strong> {name}</Text>
                    <Text><strong>Domain:</strong> {domain} / {category}</Text>
                    <Text><strong>Warehouse:</strong> {selectedWarehouse?.name} / {warehouseConfig['dataset']}</Text>
                    <Text><strong>LLM:</strong> {selectedLLM?.name} / {llmConfig['model']}</Text>
                    <Button onClick={handleCreate} loading={loading} fullWidth mt="md">Create Project</Button>
                  </Stack>
                </Card>
              </Stepper.Completed>
            </Stepper>

            <Group justify="flex-end">
              {active > 0 && <Button variant="default" onClick={() => setActive((c) => c - 1)}>Back</Button>}
              {active < 4 && <Button onClick={() => setActive((c) => c + 1)} disabled={!canProceed[active]?.()}>Next</Button>}
            </Group>
          </>
        )}
      </Stack>
    </Shell>
  );
}

function DynamicField({ field, value, onChange }: { field: ConfigField; value: string; onChange: (v: string) => void }) {
  if (field.type === 'textarea') {
    return (
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

function buildDefaults(fields: ConfigField[]): Record<string, string> {
  const defaults: Record<string, string> = {};
  for (const f of fields) {
    if (f.default) defaults[f.key] = f.default;
  }
  return defaults;
}
