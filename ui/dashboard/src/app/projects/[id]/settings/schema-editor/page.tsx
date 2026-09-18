'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Alert, Badge, Button, Card, Center, Collapse, Divider, Group, Loader, Modal, ScrollArea,
  Stack, Table, Text, TextInput, Textarea, Title, Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconAlertCircle, IconArrowLeft, IconHistory, IconSearch, IconTrash, IconRestore,
} from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { usePermissions } from '@/components/PermissionProvider';
import {
  api, Project, SchemaEdit, SchemaEditorTable, resolvePrimaryDatasourceId,
} from '@/lib/api';

// Human labels for the audit-trail action codes.
const ACTION_LABEL: Record<string, string> = {
  blurb_edit: 'Blurb edited',
  keywords_edit: 'Keywords edited',
  columns_edit: 'Columns removed',
  table_delete: 'Table removed',
};

const MONO = { fontFamily: 'monospace' } as const;

function parseKeywords(raw: string): string[] {
  return raw.split(',').map((s) => s.trim()).filter(Boolean);
}

function sameKeywords(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((v, i) => v === b[i]);
}

// SchemaEditorPage is the advanced surface for correcting a datasource's
// indexed schema: rewrite a table's blurb (re-embedded into Qdrant on save),
// remove columns or a whole table, and review the manual-edit audit trail.
// Edits are ephemeral — the next re-index rediscovers from the warehouse and
// overwrites them — which is why every change is recorded here.
export default function SchemaEditorPage() {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const { hasRole } = usePermissions();
  const canEdit = hasRole('member');

  const [project, setProject] = useState<Project | null>(null);
  const [tables, setTables] = useState<SchemaEditorTable[]>([]);
  const [total, setTotal] = useState(0);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [searchInput, setSearchInput] = useState('');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<string | null>(null);

  // Per-table edit draft.
  const [blurbDraft, setBlurbDraft] = useState('');
  const [keywordsDraft, setKeywordsDraft] = useState('');
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [saving, setSaving] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  const [edits, setEdits] = useState<SchemaEdit[]>([]);
  // Manual edits the next re-index (or Clear schema cache) would discard — they
  // are ephemeral, so the banner nudges a review before a rebuild.
  const [sinceRebuild, setSinceRebuild] = useState(0);
  const [showHistory, setShowHistory] = useState(false);

  const datasourceId = useMemo(
    () => (project ? resolvePrimaryDatasourceId(project) : undefined),
    [project],
  );

  // A friendly label for the data source being edited (its provider), shown as a
  // header badge so it's obvious which datasource this page acts on.
  const datasourceLabel = useMemo(() => {
    if (!project) return undefined;
    const whs = project.warehouses;
    const primary = whs && whs.length
      ? (whs.find((w) => (w.id || 'default') === datasourceId) || whs[0])
      : project.warehouse;
    return primary?.provider || undefined;
  }, [project, datasourceId]);

  // Debounce the search box so typing doesn't fire a request per keystroke.
  useEffect(() => {
    const t = setTimeout(() => setSearch(searchInput.trim()), 300);
    return () => clearTimeout(t);
  }, [searchInput]);

  useEffect(() => {
    let alive = true;
    api.getProject(id).then((p) => { if (alive) setProject(p); }).catch((e) => {
      if (alive) setError((e as Error).message);
    });
    return () => { alive = false; };
  }, [id]);

  const loadTables = useCallback(async () => {
    if (!project) return;
    setLoading(true);
    try {
      const res = await api.listSchemaEditorTables(id, datasourceId, search || undefined);
      setTables(res.tables || []);
      setTotal(res.total);
      setTruncated(res.truncated);
      setError(null);
    } catch (e: unknown) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [id, project, datasourceId, search]);

  useEffect(() => { void loadTables(); }, [loadTables]);

  const refreshEdits = useCallback(async () => {
    try {
      const res = await api.listSchemaEdits(id, datasourceId);
      setEdits(res.edits || []);
      setSinceRebuild(res.since_last_index || 0);
    } catch {
      /* audit trail is best-effort — never blocks editing */
    }
  }, [id, datasourceId]);

  useEffect(() => { if (project) void refreshEdits(); }, [project, refreshEdits]);

  const selectedTable = useMemo(
    () => tables.find((t) => t.table === selected) || null,
    [tables, selected],
  );

  // When the selected table changes, seed the draft from its stored values.
  useEffect(() => {
    if (!selectedTable) return;
    setBlurbDraft(selectedTable.blurb || '');
    setKeywordsDraft((selectedTable.keywords || []).join(', '));
    setRemoved(new Set());
  }, [selectedTable]);

  const dirty = useMemo(() => {
    if (!selectedTable) return false;
    const blurbChanged = blurbDraft.trim() !== (selectedTable.blurb || '');
    const kwChanged = !sameKeywords(parseKeywords(keywordsDraft), selectedTable.keywords || []);
    return blurbChanged || kwChanged || removed.size > 0;
  }, [selectedTable, blurbDraft, keywordsDraft, removed]);

  const handleSave = async () => {
    if (!selectedTable) return;
    const input: { blurb?: string; keywords?: string[]; columns?: typeof selectedTable.columns } = {};
    if (blurbDraft.trim() !== (selectedTable.blurb || '')) input.blurb = blurbDraft.trim();
    const kws = parseKeywords(keywordsDraft);
    if (!sameKeywords(kws, selectedTable.keywords || [])) input.keywords = kws;
    if (removed.size > 0) input.columns = selectedTable.columns.filter((c) => !removed.has(c.name));

    setSaving(true);
    try {
      const updated = await api.updateSchemaEditorTable(id, selectedTable.table, input, datasourceId);
      setTables((prev) => prev.map((t) => (t.table === updated.table ? updated : t)));
      setRemoved(new Set());
      notifications.show({ title: 'Saved', message: `Updated ${updated.table}`, color: 'green' });
      void refreshEdits();
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await api.deleteSchemaEditorTable(id, deleteTarget, datasourceId);
      setTables((prev) => prev.filter((t) => t.table !== deleteTarget));
      if (selected === deleteTarget) setSelected(null);
      setTotal((n) => Math.max(0, n - 1));
      notifications.show({ title: 'Removed', message: `${deleteTarget} removed from the index`, color: 'green' });
      void refreshEdits();
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setDeleteTarget(null);
    }
  };

  const toggleRemoved = (name: string) => {
    setRemoved((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name); else next.add(name);
      return next;
    });
  };

  if (error && !project) {
    return <Shell><Alert color="red" icon={<IconAlertCircle size={16} />}>{error}</Alert></Shell>;
  }

  return (
    <Shell fullWidth>
      <Stack gap="lg" p="md">
        {/* Header */}
        <Stack gap="sm">
          <Group justify="space-between" wrap="nowrap" align="flex-start">
            <Stack gap={6}>
              <Button variant="subtle" size="compact-sm" leftSection={<IconArrowLeft size={16} />}
                onClick={() => router.push(`/projects/${id}/settings`)} style={{ alignSelf: 'flex-start' }}>
                Back to settings
              </Button>
              <Group gap="sm" align="center">
                <Title order={3}>Edit indexed schema</Title>
                {datasourceLabel && (
                  <Badge variant="light" color="gray" size="lg">{datasourceLabel}</Badge>
                )}
              </Group>
            </Stack>
            <Button variant="light" leftSection={<IconHistory size={16} />}
              onClick={() => setShowHistory((v) => !v)}>
              {showHistory ? 'Hide history' : `Edit history${edits.length ? ` (${edits.length})` : ''}`}
            </Button>
          </Group>

          <Text size="sm" c="dimmed" maw={780}>
            Correct a table&apos;s description (blurb), remove columns you don&apos;t want the agent
            to use, or drop a table entirely. Changes take effect immediately — discovery and Ask
            read this schema directly. These edits are <strong>ephemeral</strong>: the next re-index
            (or <em>Clear schema cache</em>) rediscovers the schema from the warehouse and discards
            them, so every change is recorded in the edit history below for re-applying.
          </Text>
        </Stack>

        {sinceRebuild > 0 && (
          <Alert color="yellow" variant="light" icon={<IconAlertCircle size={16} />} maw={780}>
            {sinceRebuild} manual {sinceRebuild === 1 ? 'edit' : 'edits'} since the last index.
            The next re-index or <em>Clear schema cache</em> will discard {sinceRebuild === 1 ? 'it' : 'them'} — the edit history keeps a copy so you can re-apply.
          </Alert>
        )}

        <Collapse in={showHistory}>
          <Card withBorder p="md" radius="md">
            <Group justify="space-between" align="center" mb="sm" wrap="nowrap">
              <Text fw={600} size="sm">Edit history</Text>
              <Text size="xs" c="dimmed">Recorded so you can re-apply changes after a rebuild.</Text>
            </Group>
            <EditHistory edits={edits} />
          </Card>
        </Collapse>

        {/* Browse + edit */}
        {loading ? (
          <Center mih={220}><Loader /></Center>
        ) : error ? (
          <Alert color="red" variant="light" icon={<IconAlertCircle size={16} />} maw={780}>
            Couldn&apos;t load the indexed schema: {error}
          </Alert>
        ) : (
          <Group align="flex-start" gap="md" wrap="nowrap">
            {/* Left: table list */}
            <Card withBorder p="sm" radius="md" style={{ width: 300, flexShrink: 0 }}>
              <Stack gap="sm">
                <TextInput
                  placeholder="Search tables…"
                  leftSection={<IconSearch size={14} />}
                  value={searchInput}
                  onChange={(e) => setSearchInput(e.currentTarget.value)}
                />
                {tables.length === 0 ? (
                  <Text size="sm" c="dimmed" py="xs">
                    {search ? 'No tables match your search.' : 'No indexed tables for this data source yet.'}
                  </Text>
                ) : (
                  <>
                    <Group justify="space-between" px={4} gap={4}>
                      <Text size="xs" c="dimmed" fw={500}>{total} table{total === 1 ? '' : 's'}</Text>
                      {truncated && <Text size="xs" c="dimmed">showing {tables.length}</Text>}
                    </Group>
                    <ScrollArea.Autosize mah={540}>
                      <Stack gap={2}>
                        {tables.map((t) => {
                          const active = t.table === selected;
                          return (
                            <Button
                              key={t.table}
                              variant={active ? 'light' : 'subtle'}
                              color={active ? 'blue' : 'gray'}
                              size="compact-sm"
                              fullWidth
                              justify="flex-start"
                              onClick={() => setSelected(t.table)}
                              styles={{
                                root: { fontWeight: active ? 600 : 400 },
                                inner: { justifyContent: 'flex-start' },
                                label: { overflow: 'hidden', textOverflow: 'ellipsis', display: 'block' },
                              }}
                            >
                              {t.table}
                              {!t.has_blurb && <Badge ml={6} size="xs" color="gray" variant="outline">no blurb</Badge>}
                            </Button>
                          );
                        })}
                      </Stack>
                    </ScrollArea.Autosize>
                  </>
                )}
              </Stack>
            </Card>

            {/* Right: detail */}
            <Card withBorder p="md" radius="md" style={{ flex: 1, minWidth: 0 }}>
              {!selectedTable ? (
                <Center mih={180}>
                  <Text size="sm" c="dimmed">Select a table on the left to view or edit it.</Text>
                </Center>
              ) : (
                <Stack gap="md">
                  {/* Detail header */}
                  <Group justify="space-between" wrap="nowrap" align="flex-start">
                    <Stack gap={6} style={{ minWidth: 0 }}>
                      <Text fw={600} style={{ ...MONO, wordBreak: 'break-all' }}>{selectedTable.table}</Text>
                      <Group gap="xs">
                        <Badge variant="light" color="gray">{selectedTable.row_count.toLocaleString()} rows</Badge>
                        <Badge variant="light" color="gray">{selectedTable.columns.length} columns</Badge>
                      </Group>
                    </Stack>
                    {canEdit && (
                      <Button color="red" variant="light" size="compact-sm"
                        leftSection={<IconTrash size={14} />}
                        onClick={() => setDeleteTarget(selectedTable.table)}>
                        Remove table
                      </Button>
                    )}
                  </Group>

                  <Divider />

                  {/* Blurb */}
                  <Stack gap={6}>
                    <div>
                      <Text size="sm" fw={600}>Description (blurb)</Text>
                      <Text size="xs" c="dimmed">What the table holds — this text is embedded for semantic search when the agent looks for relevant tables.</Text>
                    </div>
                    <Textarea
                      value={blurbDraft}
                      onChange={(e) => setBlurbDraft(e.currentTarget.value)}
                      autosize minRows={3} maxRows={8}
                      readOnly={!canEdit}
                      placeholder={selectedTable.has_blurb ? '' : 'No blurb yet — write one to make this table findable.'}
                    />
                  </Stack>

                  <Divider />

                  {/* Keywords */}
                  <Stack gap={6}>
                    <div>
                      <Text size="sm" fw={600}>Keywords</Text>
                      <Text size="xs" c="dimmed">Comma-separated terms that boost exact-match search for this table.</Text>
                    </div>
                    <TextInput
                      value={keywordsDraft}
                      onChange={(e) => setKeywordsDraft(e.currentTarget.value)}
                      readOnly={!canEdit}
                      placeholder="e.g. orders, revenue, fulfilment"
                    />
                  </Stack>

                  <Divider />

                  {/* Columns */}
                  <Stack gap={6}>
                    <div>
                      <Text size="sm" fw={600}>Columns</Text>
                      <Text size="xs" c="dimmed">Remove columns you don&apos;t want the agent to consider. Removal only — a rebuild brings them back.</Text>
                    </div>
                    <Table fz="xs" verticalSpacing={6} horizontalSpacing="sm" withTableBorder highlightOnHover>
                      <Table.Thead>
                        <Table.Tr>
                          <Table.Th>Name</Table.Th>
                          <Table.Th>Type</Table.Th>
                          <Table.Th>Category</Table.Th>
                          {canEdit && <Table.Th w={44} ta="center">Remove</Table.Th>}
                        </Table.Tr>
                      </Table.Thead>
                      <Table.Tbody>
                        {selectedTable.columns.map((c) => {
                          const isRemoved = removed.has(c.name);
                          return (
                            <Table.Tr key={c.name} style={isRemoved ? { opacity: 0.4, textDecoration: 'line-through' } : undefined}>
                              <Table.Td style={MONO}>{c.name}</Table.Td>
                              <Table.Td>{c.type}</Table.Td>
                              <Table.Td>{c.category || '—'}</Table.Td>
                              {canEdit && (
                                <Table.Td ta="center">
                                  <Tooltip label={isRemoved ? 'Keep column' : 'Remove column'}>
                                    <Button variant="subtle" color={isRemoved ? 'blue' : 'red'} size="compact-xs"
                                      aria-label={isRemoved ? `Keep column ${c.name}` : `Remove column ${c.name}`}
                                      onClick={() => toggleRemoved(c.name)} px={6}>
                                      {isRemoved ? <IconRestore size={14} /> : <IconTrash size={14} />}
                                    </Button>
                                  </Tooltip>
                                </Table.Td>
                              )}
                            </Table.Tr>
                          );
                        })}
                      </Table.Tbody>
                    </Table>
                    {removed.size > 0 && (
                      <Text size="xs" c="orange">{removed.size} column{removed.size === 1 ? '' : 's'} will be removed on save.</Text>
                    )}
                  </Stack>

                  <Divider />

                  {/* Footer */}
                  <Group justify="space-between" align="center">
                    {canEdit ? (
                      <Text size="xs" c={dirty ? 'orange' : 'dimmed'}>{dirty ? 'Unsaved changes' : 'No changes'}</Text>
                    ) : (
                      <Text size="xs" c="dimmed">You have read-only access to this project.</Text>
                    )}
                    {canEdit && (
                      <Button onClick={handleSave} loading={saving} disabled={!dirty}>Save changes</Button>
                    )}
                  </Group>
                </Stack>
              )}
            </Card>
          </Group>
        )}
      </Stack>

      <Modal opened={!!deleteTarget} onClose={() => setDeleteTarget(null)} title="Remove table from index" centered>
        <Stack gap="sm">
          <Text size="sm">
            Remove <strong style={MONO}>{deleteTarget}</strong> from the schema index? The agent
            will stop using it in discovery and search right away. This edit is <strong>ephemeral</strong>:
            the next re-index rebuilds from the warehouse and brings the table back (unless it was
            also dropped there).
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button color="red" onClick={handleDelete}>Remove</Button>
          </Group>
        </Stack>
      </Modal>
    </Shell>
  );
}

function EditHistory({ edits }: { edits: SchemaEdit[] }) {
  if (edits.length === 0) {
    return <Text size="xs" c="dimmed">No manual edits recorded yet.</Text>;
  }
  return (
    <ScrollArea.Autosize mah={280}>
      <Table fz="xs" striped withTableBorder verticalSpacing={6} horizontalSpacing="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>When</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Table</Table.Th>
            <Table.Th>Before → After</Table.Th>
            <Table.Th>By</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {edits.map((e) => (
            <Table.Tr key={e.id || `${e.table}:${e.at}`}>
              <Table.Td>{new Date(e.at).toLocaleString()}</Table.Td>
              <Table.Td>{ACTION_LABEL[e.action] || e.action}</Table.Td>
              <Table.Td style={MONO}>{e.table}</Table.Td>
              <Table.Td>
                <Text size="xs" c="dimmed" lineClamp={2} style={{ maxWidth: 420 }} title={`${e.before || ''} → ${e.after || ''}`}>
                  {e.before ? e.before : '—'}{' → '}{e.after ? e.after : '—'}
                </Text>
              </Table.Td>
              <Table.Td>{e.actor || '—'}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </ScrollArea.Autosize>
  );
}
