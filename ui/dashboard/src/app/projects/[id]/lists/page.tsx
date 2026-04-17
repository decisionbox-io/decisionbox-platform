'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import Link from 'next/link';
import { Button, Loader, Modal, TextInput, Textarea, ColorInput, Stack, Group, Text } from '@mantine/core';
import { IconBookmark, IconPlus } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { EmptyState, SectionHeader } from '@/components/common/UIComponents';
import { api, BookmarkList } from '@/lib/api';

export default function ListsPage() {
  const { id } = useParams<{ id: string }>();
  const [lists, setLists] = useState<BookmarkList[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState('');
  const [newDescription, setNewDescription] = useState('');
  const [newColor, setNewColor] = useState('#2563eb');
  const [submitting, setSubmitting] = useState(false);

  async function load() {
    try {
      const data = await api.listBookmarkLists(id);
      setLists(data || []);
    } catch (e) {
      setError((e as Error).message);
      setLists([]);
    }
  }

  useEffect(() => {
    load();
  }, [id]); // eslint-disable-line react-hooks/exhaustive-deps

  async function handleCreate() {
    const name = newName.trim();
    if (!name) return;
    setSubmitting(true);
    try {
      await api.createBookmarkList(id, {
        name,
        description: newDescription.trim() || undefined,
        color: newColor || undefined,
      });
      setCreating(false);
      setNewName('');
      setNewDescription('');
      setNewColor('#2563eb');
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  }

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: 'Lists' },
  ];

  return (
    <Shell
      breadcrumb={breadcrumb}
      actions={
        <Button
          size="sm"
          leftSection={<IconPlus size={14} />}
          onClick={() => setCreating(true)}
        >
          New list
        </Button>
      }
    >
      <SectionHeader title="Lists" count={lists?.length} />

      {error && (
        <div style={{
          background: 'var(--db-red-bg)', color: 'var(--db-red-text)',
          padding: 12, borderRadius: 'var(--db-radius)', marginBottom: 12, fontSize: 13,
        }}>{error}</div>
      )}

      {lists === null ? (
        <div style={{ padding: 32, textAlign: 'center' }}><Loader /></div>
      ) : lists.length === 0 ? (
        <EmptyState
          icon={<IconBookmark size={32} />}
          title="No lists yet"
          description="Create your first list to bookmark insights and recommendations you want to revisit."
        />
      ) : (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))',
          gap: 12,
        }}>
          {lists.map(list => (
            <Link
              key={list.id}
              href={`/projects/${id}/lists/${list.id}`}
              style={{ textDecoration: 'none', color: 'inherit' }}
            >
              <div
                style={{
                  background: 'var(--db-bg-white)',
                  border: '1px solid var(--db-border-default)',
                  borderRadius: 'var(--db-radius-lg)',
                  padding: 16,
                  cursor: 'pointer',
                  transition: 'border-color 120ms ease',
                  borderLeft: `3px solid ${list.color || 'var(--db-border-strong)'}`,
                  height: '100%',
                }}
                onMouseEnter={e => { e.currentTarget.style.borderColor = 'var(--db-border-strong)'; }}
                onMouseLeave={e => { e.currentTarget.style.borderColor = 'var(--db-border-default)'; }}
              >
                <div style={{
                  fontSize: 15, fontWeight: 500, color: 'var(--db-text-primary)',
                  marginBottom: 4, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                }}>{list.name}</div>
                {list.description && (
                  <div style={{
                    fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 8,
                    display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden',
                  }}>{list.description}</div>
                )}
                <div style={{
                  fontSize: 11, color: 'var(--db-text-tertiary)', marginTop: 8,
                  display: 'flex', justifyContent: 'space-between',
                }}>
                  <span>{list.item_count} {list.item_count === 1 ? 'item' : 'items'}</span>
                  <span>Updated {new Date(list.updated_at).toLocaleDateString()}</span>
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}

      <Modal opened={creating} onClose={() => setCreating(false)} title="New list" centered>
        <Stack gap="sm">
          <TextInput
            label="Name"
            placeholder="Retention ideas"
            value={newName}
            onChange={e => setNewName(e.currentTarget.value)}
            required
            maxLength={200}
            data-autofocus
          />
          <Textarea
            label="Description"
            placeholder="Optional — what kind of items will you collect here?"
            value={newDescription}
            onChange={e => setNewDescription(e.currentTarget.value)}
            autosize
            minRows={2}
          />
          <ColorInput
            label="Color"
            value={newColor}
            onChange={setNewColor}
            format="hex"
            swatches={['#2563eb', '#16a34a', '#dc2626', '#ea580c', '#9333ea', '#0891b2']}
          />
          <Group justify="flex-end" mt="sm">
            <Button variant="default" onClick={() => setCreating(false)} disabled={submitting}>Cancel</Button>
            <Button onClick={handleCreate} loading={submitting} disabled={!newName.trim()}>Create</Button>
          </Group>
          {error && <Text size="xs" c="red">{error}</Text>}
        </Stack>
      </Modal>
    </Shell>
  );
}
