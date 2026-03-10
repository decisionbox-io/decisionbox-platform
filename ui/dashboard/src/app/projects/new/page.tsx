'use client';

import { useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import {
  Alert, Button, Card, Group, Loader, Select, Stack, Stepper, Text, TextInput, Textarea, Title, NumberInput, Switch,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { IconAlertCircle } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { api, Domain, Category } from '@/lib/api';

export default function NewProjectPage() {
  const router = useRouter();
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);
  const [domains, setDomains] = useState<Domain[]>([]);
  const [domainsLoading, setDomainsLoading] = useState(true);
  const [domainsError, setDomainsError] = useState<string | null>(null);

  // Form state
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [domain, setDomain] = useState('');
  const [category, setCategory] = useState('');
  const [warehouseProvider, setWarehouseProvider] = useState('bigquery');
  const [warehouseProjectId, setWarehouseProjectId] = useState('');
  const [warehouseDataset, setWarehouseDataset] = useState('');
  const [warehouseLocation, setWarehouseLocation] = useState('US');
  const [filterField, setFilterField] = useState('');
  const [filterValue, setFilterValue] = useState('');
  const [llmProvider, setLlmProvider] = useState('claude');
  const [llmModel, setLlmModel] = useState('claude-sonnet-4-20250514');
  const [scheduleEnabled, setScheduleEnabled] = useState(true);
  const [scheduleCron, setScheduleCron] = useState('0 2 * * *');
  const [maxSteps, setMaxSteps] = useState(100);

  useEffect(() => {
    api.listDomains()
      .then((data) => {
        setDomains(data);
        // Auto-select first domain + category if only one
        if (data.length === 1) {
          setDomain(data[0].id);
          if (data[0].categories.length === 1) {
            setCategory(data[0].categories[0].id);
          }
        }
      })
      .catch((e) => setDomainsError(e.message))
      .finally(() => setDomainsLoading(false));
  }, []);

  const categories: Category[] = domains.find((d) => d.id === domain)?.categories || [];

  const canProceed = [
    () => name && domain && category,
    () => warehouseDataset,
    () => llmProvider && llmModel,
    () => true,
  ];

  const handleCreate = async () => {
    setLoading(true);
    try {
      const project = await api.createProject({
        name,
        description,
        domain,
        category,
        warehouse: {
          provider: warehouseProvider,
          project_id: warehouseProjectId,
          dataset: warehouseDataset,
          location: warehouseLocation,
          filter_field: filterField,
          filter_value: filterValue,
        },
        llm: { provider: llmProvider, model: llmModel },
        schedule: { enabled: scheduleEnabled, cron_expr: scheduleCron, max_steps: maxSteps },
      });
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

        {domainsError && (
          <Alert icon={<IconAlertCircle size={16} />} title="Cannot load domains" color="red">
            {domainsError}
          </Alert>
        )}

        <Stepper active={active} onStepClick={setActive}>
          <Stepper.Step label="Basics" description="Name and domain">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <TextInput label="Project Name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="My Game Analytics" />
                <Textarea label="Description" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Optional description" />

                {domainsLoading ? (
                  <Group><Loader size="sm" /><Text size="sm" c="dimmed">Loading domains...</Text></Group>
                ) : (
                  <Select
                    label="Domain" required
                    placeholder="Select a domain"
                    data={domains.map((d) => ({ value: d.id, label: d.id.charAt(0).toUpperCase() + d.id.slice(1) }))}
                    value={domain}
                    onChange={(v) => { setDomain(v || ''); setCategory(''); }}
                  />
                )}

                {domain && categories.length > 0 && (
                  <Select
                    label="Category" required
                    placeholder="Select a category"
                    data={categories.map((c) => ({ value: c.id, label: c.name }))}
                    value={category}
                    onChange={(v) => setCategory(v || '')}
                  />
                )}
              </Stack>
            </Card>
          </Stepper.Step>

          <Stepper.Step label="Warehouse" description="Data source">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <Select
                  label="Warehouse Provider" required
                  data={[
                    { value: 'bigquery', label: 'Google BigQuery' },
                    { value: 'postgres', label: 'PostgreSQL (coming soon)', disabled: true },
                  ]}
                  value={warehouseProvider}
                  onChange={(v) => setWarehouseProvider(v || 'bigquery')}
                />
                {warehouseProvider === 'bigquery' && (
                  <TextInput label="GCP Project ID" value={warehouseProjectId}
                    onChange={(e) => setWarehouseProjectId(e.target.value)} placeholder="my-gcp-project" />
                )}
                <TextInput label="Dataset" required value={warehouseDataset}
                  onChange={(e) => setWarehouseDataset(e.target.value)} placeholder="my_analytics_dataset" />
                <TextInput label="Location" value={warehouseLocation}
                  onChange={(e) => setWarehouseLocation(e.target.value)} />
                <Text size="sm" fw={600} mt="sm">Filter (optional)</Text>
                <Text size="xs" c="dimmed">For shared datasets with multiple apps. Leave empty if the entire dataset is yours.</Text>
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
                <Select
                  label="LLM Provider" required
                  data={[
                    { value: 'claude', label: 'Claude (Anthropic)' },
                    { value: 'openai', label: 'OpenAI' },
                    { value: 'ollama', label: 'Ollama (Local)' },
                    { value: 'vertex-ai', label: 'Vertex AI (coming soon)', disabled: true },
                    { value: 'bedrock', label: 'AWS Bedrock (coming soon)', disabled: true },
                  ]}
                  value={llmProvider}
                  onChange={(v) => setLlmProvider(v || 'claude')}
                />
                <TextInput label="Model" required value={llmModel}
                  onChange={(e) => setLlmModel(e.target.value)} />
                <Text size="xs" c="dimmed">
                  API keys are configured via environment variables (LLM_API_KEY), not stored in the database.
                </Text>
              </Stack>
            </Card>
          </Stepper.Step>

          <Stepper.Step label="Schedule" description="Discovery schedule">
            <Card withBorder p="lg" mt="md">
              <Stack>
                <Switch
                  label="Enable automatic discovery"
                  checked={scheduleEnabled}
                  onChange={(e) => setScheduleEnabled(e.currentTarget.checked)}
                />
                {scheduleEnabled && (
                  <TextInput label="Cron Expression" value={scheduleCron}
                    onChange={(e) => setScheduleCron(e.target.value)}
                    description="Default: daily at 2 AM UTC" />
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
                <Text><strong>Warehouse:</strong> {warehouseProvider} / {warehouseDataset}</Text>
                <Text><strong>LLM:</strong> {llmProvider} / {llmModel}</Text>
                <Button onClick={handleCreate} loading={loading} fullWidth mt="md">
                  Create Project
                </Button>
              </Stack>
            </Card>
          </Stepper.Completed>
        </Stepper>

        <Group justify="flex-end">
          {active > 0 && (
            <Button variant="default" onClick={() => setActive((c) => c - 1)}>Back</Button>
          )}
          {active < 4 && (
            <Button onClick={() => setActive((c) => c + 1)} disabled={!canProceed[active]?.()}>
              Next
            </Button>
          )}
        </Group>
      </Stack>
    </Shell>
  );
}
