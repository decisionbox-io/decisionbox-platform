'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  Alert, Badge, Button, Collapse, Group, Loader, Modal, ScrollArea,
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
  const { hasRole, loading: permsLoading } = usePermissions();
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
      <Stack gap="md" p="md">
        <Group justify="space-between" wrap="nowrap">
          <Group gap="xs">
            <Button variant="subtle" size="compact-sm" leftSection={<IconArrowLeft size={16} />}
              onClick={() => router.push(`/projects/${id}/settings`)}>Back to settings</Button>
            <Title order={4}>Edit indexed schema</Title>
          </Group>
          <Button variant="light" size="compact-sm" leftSection={<IconHistory size={14} />}
            onClick={() => setShowHistory((v) => !v)}>
            {showHistory ? 'Hide history' : `Edit history${edits.length ? ` (${edits.length})` : ''}`}
          </Button>
        </Group>

        <Text size="xs" c="dimmed" maw={760}>
          Correct a table&apos;s description (blurb), remove columns you don&apos;t want the
          agent to use, or drop a table entirely. Changes take effect immediately — discovery
          and Ask read this schema directly. <strong>Note:</strong> these edits are ephemeral —
          the next re-index re-discovers the schema from the warehouse and discards them
          (<em>Clear schema cache</em> discards them too). Every change is recorded in the edit
          history below so you can re-apply it.
        </Text>

        {sinceRebuild > 0 && (
          <Alert color="yellow" variant="light" icon={<IconAlertCircle size={16} />} maw={760}>
            {sinceRebuild} manual {sinceRebuild === 1 ? 'edit' : 'edits'} since the last index.
            The next re-index or <em>Clear schema cache</em> will discard {sinceRebuild === 1 ? 'it' : 'them'} — the edit history keeps a copy so you can re-apply.
          </Alert>
        )}

        <Collapse in={showHistory}>
          <EditHistory edits={edits} />
        </Collapse>

        <TextInput
          placeholder="Search tables…"
          leftSection={<IconSearch size={14} />}
          value={searchInput}
          onChange={(e) => setSearchInput(e.currentTarget.value)}
          maw={360}
        />

        {loading ? (
          <Loader />
        ) : error ? (
          <Alert color="red" variant="light" icon={<IconAlertCircle size={16} />} maw={760}>
            Couldn&apos;t load the indexed schema: {error}
          </Alert>
        ) : tables.length === 0 ? (
          <Text size="sm" c="dimmed">
            {search ? 'No tables match your search.' : 'No indexed tables for this data source yet.'}
          </Text>
        ) : (
          <Group align="flex-start" gap="md" wrap="nowrap" style={{ minHeight: 0 }}>
            {/* Left: table list */}
            <ScrollArea.Autosize mah={560} style={{ width: 320, flexShrink: 0 }}>
              <Stack gap={2}>
                {truncated && (
                  <Text size="xs" c="dimmed" mb={4}>
                    Showing {tables.length} of {total} — refine your search to see the rest.
                  </Text>
                )}
                {tables.map((t) => (
                  <Button
                    key={t.table}
                    variant={t.table === selected ? 'light' : 'subtle'}
                    size="compact-sm"
                    justify="flex-start"
                    onClick={() => setSelected(t.table)}
                    styles={{ label: { overflow: 'hidden', textOverflow: 'ellipsis' } }}
                  >
                    {t.table}
                    {!t.has_blurb && <Badge ml={6} size="xs" color="gray" variant="outline">no blurb</Badge>}
                  </Button>
                ))}
              </Stack>
            </ScrollArea.Autosize>

            {/* Right: detail */}
            <div style={{ flex: 1, minWidth: 0 }}>
              {!selectedTable ? (
                <Text size="sm" c="dimmed">Select a table to view or edit it.</Text>
              ) : (
                <Stack gap="sm">
                  <Group justify="space-between" wrap="nowrap">
                    <Text fw={600} style={{ fontFamily: 'monospace' }}>{selectedTable.table}</Text>
                    <Group gap="xs">
                      <Text size="xs" c="dimmed">{selectedTable.row_count.toLocaleString()} rows · {selectedTable.columns.length} columns</Text>
                      {canEdit && (
                        <Button color="red" variant="light" size="compact-xs"
                          leftSection={<IconTrash size={14} />}
                          onClick={() => setDeleteTarget(selectedTable.table)}>
                          Remove table
                        </Button>
                      )}
                    </Group>
                  </Group>

                  <div>
                    <Text size="sm" fw={500} mb={4}>Blurb</Text>
                    <Textarea
                      value={blurbDraft}
                      onChange={(e) => setBlurbDraft(e.currentTarget.value)}
                      autosize minRows={3} maxRows={8}
                      readOnly={!canEdit}
                      placeholder={selectedTable.has_blurb ? '' : 'No blurb yet — write one to make this table findable.'}
                    />
                  </div>

                  <div>
                    <Text size="sm" fw={500} mb={4}>Keywords <Text span size="xs" c="dimmed">(comma-separated)</Text></Text>
                    <TextInput
                      value={keywordsDraft}
                      onChange={(e) => setKeywordsDraft(e.currentTarget.value)}
                      readOnly={!canEdit}
                      placeholder="e.g. orders, revenue, fulfilment"
                    />
                  </div>

                  <div>
                    <Text size="sm" fw={500} mb={4}>Columns</Text>
                    <Table fz="xs" verticalSpacing={4} horizontalSpacing="sm" withTableBorder>
                      <Table.Thead>
                        <Table.Tr>
                          <Table.Th>Name</Table.Th>
                          <Table.Th>Type</Table.Th>
                          <Table.Th>Category</Table.Th>
                          {canEdit && <Table.Th w={40}></Table.Th>}
                        </Table.Tr>
                      </Table.Thead>
                      <Table.Tbody>
                        {selectedTable.columns.map((c) => {
                          const isRemoved = removed.has(c.name);
                          return (
                            <Table.Tr key={c.name} style={isRemoved ? { opacity: 0.4, textDecoration: 'line-through' } : undefined}>
                              <Table.Td style={{ fontFamily: 'monospace' }}>{c.name}</Table.Td>
                              <Table.Td>{c.type}</Table.Td>
                              <Table.Td>{c.category || '—'}</Table.Td>
                              {canEdit && (
                                <Table.Td>
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
                      <Text size="xs" c="orange" mt={4}>{removed.size} column{removed.size === 1 ? '' : 's'} will be removed on save.</Text>
                    )}
                  </div>

                  {canEdit ? (
                    <Group justify="flex-end">
                      <Button onClick={handleSave} loading={saving} disabled={!dirty}>Save changes</Button>
                    </Group>
                  ) : (
                    <Text size="xs" c="dimmed">You have read-only access to this project.</Text>
                  )}
                </Stack>
              )}
            </div>
          </Group>
        )}
      </Stack>

      <Modal opened={!!deleteTarget} onClose={() => setDeleteTarget(null)} title="Remove table from index" centered>
        <Stack gap="sm">
          <Text size="sm">
            Remove <strong style={{ fontFamily: 'monospace' }}>{deleteTarget}</strong> from the schema index?
            The agent will stop using it in discovery and search. A plain re-index keeps it
            removed; to bring it back, use <em>Clear schema cache</em> (a full rebuild that
            rediscovers from the warehouse).
          </Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setDeleteTarget(null)}>Cancel</Button>
            <Button color="red" onClick={handleDelete}>Remove</Button>
          </Group>
        </Stack>
      </Modal>

      {permsLoading && null}
    </Shell>
  );
}

function EditHistory({ edits }: { edits: SchemaEdit[] }) {
  if (edits.length === 0) {
    return <Text size="xs" c="dimmed">No manual edits recorded yet.</Text>;
  }
  return (
    <ScrollArea.Autosize mah={280}>
      <Table fz="xs" striped withTableBorder verticalSpacing={4} horizontalSpacing="sm">
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
              <Table.Td style={{ fontFamily: 'monospace' }}>{e.table}</Table.Td>
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
