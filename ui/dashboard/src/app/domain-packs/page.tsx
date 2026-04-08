'use client';

import { useEffect, useRef, useState } from 'react';
import { Alert, Badge, Button, Card, Group, Modal, SimpleGrid, Stack, Text, Textarea, Title } from '@mantine/core';
import { IconAlertCircle, IconDownload, IconPackages, IconPlus, IconUpload } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { api, DomainPack, PortableDomainPack } from '@/lib/api';

export default function DomainPacksPage() {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [packs, setPacks] = useState<DomainPack[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [importJson, setImportJson] = useState('');
  const [importError, setImportError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);

  const loadPacks = () => {
    setLoading(true);
    api.listDomainPacks()
      .then(setPacks)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  };

  useEffect(() => { loadPacks(); }, []);

  const handleImport = async () => {
    setImportError(null);
    setImporting(true);
    try {
      const parsed = JSON.parse(importJson) as PortableDomainPack;
      await api.importDomainPack(parsed);
      setImportOpen(false);
      setImportJson('');
      loadPacks();
    } catch (e) {
      setImportError((e as Error).message);
    } finally {
      setImporting(false);
    }
  };

  const handleFileUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (ev) => {
      setImportJson(ev.target?.result as string);
      setImportOpen(true);
    };
    reader.readAsText(file);
    e.target.value = '';
  };

  const handleExport = async (slug: string) => {
    try {
      const data = await api.exportDomainPack(slug);
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${slug}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const handleDelete = async (slug: string) => {
    if (!confirm(`Delete domain pack "${slug}"? This cannot be undone. Existing projects are not affected.`)) return;
    try {
      await api.deleteDomainPack(slug);
      loadPacks();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <Shell breadcrumb={[{ label: 'Domain Packs' }]}>
      <Stack gap="lg">
        <Group justify="space-between">
          <Title order={2}>Domain Packs</Title>
          <Group gap="sm">
            <input
              type="file"
              ref={fileInputRef}
              accept=".json"
              style={{ display: 'none' }}
              onChange={handleFileUpload}
            />
            <Button
              variant="light"
              leftSection={<IconUpload size={16} />}
              onClick={() => fileInputRef.current?.click()}
            >
              Import
            </Button>
            <Button
              component={Link}
              href="/domain-packs/new"
              leftSection={<IconPlus size={16} />}
            >
              New Pack
            </Button>
          </Group>
        </Group>

        {error && (
          <Alert icon={<IconAlertCircle size={16} />} title="Error" color="red" variant="light" withCloseButton onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {loading && <Text c="dimmed">Loading domain packs...</Text>}

        {!loading && !error && packs.length === 0 && (
          <Card withBorder p="xl" ta="center">
            <Stack align="center" gap="md">
              <IconPackages size={48} color="var(--mantine-color-gray-5)" />
              <Title order={3} c="dimmed">No domain packs</Title>
              <Text c="dimmed">Domain packs define how AI discovery works for your industry. Create one or import from a file.</Text>
            </Stack>
          </Card>
        )}

        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {packs.map((pack) => (
            <Card key={pack.slug} withBorder shadow="sm" radius="md">
              <Group justify="space-between" mb="xs">
                <Text fw={600} component={Link} href={`/domain-packs/${pack.slug}`} style={{ textDecoration: 'none', color: 'inherit', cursor: 'pointer' }}>
                  {pack.name}
                </Text>
                <Group gap={4}>
                  {pack.is_published ? (
                    <Badge color="green" variant="light" size="sm">Published</Badge>
                  ) : (
                    <Badge color="gray" variant="light" size="sm">Draft</Badge>
                  )}
                </Group>
              </Group>

              <Text size="sm" c="dimmed" lineClamp={2} mb="sm">
                {pack.description || 'No description'}
              </Text>

              <Group gap="xs" mb="sm">
                <Badge variant="outline" size="xs">
                  {pack.categories?.length || 0} categories
                </Badge>
                <Badge variant="outline" size="xs">
                  {pack.analysis_areas?.base?.length || 0} base areas
                </Badge>
                <Badge variant="outline" size="xs">
                  v{pack.version || '1.0.0'}
                </Badge>
              </Group>

              <Group gap="xs" mt="auto">
                <Button
                  variant="light"
                  size="xs"
                  component={Link}
                  href={`/domain-packs/${pack.slug}`}
                >
                  Edit
                </Button>
                <Button
                  variant="subtle"
                  size="xs"
                  leftSection={<IconDownload size={14} />}
                  onClick={() => handleExport(pack.slug)}
                >
                  Export
                </Button>
                <Button
                  variant="subtle"
                  size="xs"
                  color="red"
                  onClick={() => handleDelete(pack.slug)}
                  style={{ marginLeft: 'auto' }}
                >
                  Delete
                </Button>
              </Group>
            </Card>
          ))}
        </SimpleGrid>
      </Stack>

      {/* Import Modal */}
      <Modal opened={importOpen} onClose={() => { setImportOpen(false); setImportError(null); }} title="Import Domain Pack" size="lg">
        <Stack gap="md">
          <Text size="sm" c="dimmed">
            Paste a domain pack JSON file or use the file upload button.
          </Text>
          <Textarea
            value={importJson}
            onChange={(e) => setImportJson(e.currentTarget.value)}
            placeholder='{"format": "decisionbox-domain-pack", ...}'
            minRows={10}
            maxRows={20}
            autosize
            styles={{ input: { fontFamily: 'monospace', fontSize: 12 } }}
          />
          {importError && (
            <Alert color="red" variant="light">{importError}</Alert>
          )}
          <Group justify="flex-end">
            <Button variant="light" onClick={() => setImportOpen(false)}>Cancel</Button>
            <Button onClick={handleImport} loading={importing}>Import</Button>
          </Group>
        </Stack>
      </Modal>
    </Shell>
  );
}
