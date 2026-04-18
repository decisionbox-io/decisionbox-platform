'use client';

import { useEffect, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import Link from 'next/link';
import {
  Button, Loader, Modal, TextInput, Textarea, ColorInput, Stack, Group, Text, Menu, ActionIcon,
} from '@mantine/core';
import { IconDots, IconTrash, IconPencil, IconBookmark } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { EmptyState, SectionHeader } from '@/components/common/UIComponents';
import { api, BookmarkListWithItems, StandaloneInsight, StandaloneRecommendation } from '@/lib/api';

export default function ListDetailPage() {
  const { id, listId } = useParams<{ id: string; listId: string }>();
  const router = useRouter();

  const [list, setList] = useState<BookmarkListWithItems | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [editing, setEditing] = useState(false);
  const [editName, setEditName] = useState('');
  const [editDescription, setEditDescription] = useState('');
  const [editColor, setEditColor] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const [confirmingDelete, setConfirmingDelete] = useState(false);

  async function load() {
    try {
      const data = await api.getBookmarkList(id, listId);
      setList(data);
    } catch (e) {
      const msg = (e as Error).message;
      if (msg.toLowerCase().includes('not found')) {
        setNotFound(true);
      } else {
        setError(msg);
      }
    }
  }

  useEffect(() => {
    load();
  }, [id, listId]); // eslint-disable-line react-hooks/exhaustive-deps

  function openEdit() {
    if (!list) return;
    setEditName(list.name);
    setEditDescription(list.description || '');
    setEditColor(list.color || '');
    setEditing(true);
  }

  async function handleSave() {
    const name = editName.trim();
    if (!name) return;
    setSubmitting(true);
    try {
      await api.updateBookmarkList(id, listId, {
        name,
        description: editDescription,
        color: editColor,
      });
      setEditing(false);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  }

  async function handleDeleteList() {
    try {
      await api.deleteBookmarkList(id, listId);
      router.push(`/projects/${id}/lists`);
    } catch (e) {
      setError((e as Error).message);
      setConfirmingDelete(false);
    }
  }

  async function handleRemoveBookmark(bookmarkId: string) {
    try {
      await api.removeBookmark(id, listId, bookmarkId);
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  if (notFound) {
    return (
      <Shell breadcrumb={[{ label: 'Projects', href: '/' }, { label: 'Lists', href: `/projects/${id}/lists` }, { label: 'Not found' }]}>
        <EmptyState
          icon={<IconBookmark size={32} />}
          title="List not found"
          description="This list may have been deleted."
        />
      </Shell>
    );
  }

  if (!list) {
    return (
      <Shell breadcrumb={[{ label: 'Projects', href: '/' }, { label: 'Lists', href: `/projects/${id}/lists` }]}>
        <div style={{ padding: 32, textAlign: 'center' }}><Loader /></div>
      </Shell>
    );
  }

  const insightItems = list.items.filter(it => it.bookmark.target_type === 'insight');
  const recommendationItems = list.items.filter(it => it.bookmark.target_type === 'recommendation');

  return (
    <Shell
      breadcrumb={[
        { label: 'Projects', href: '/' },
        { label: 'Lists', href: `/projects/${id}/lists` },
        { label: list.name },
      ]}
      actions={
        <Menu position="bottom-end" width={180}>
          <Menu.Target>
            <ActionIcon variant="subtle" size="lg" aria-label="List actions">
              <IconDots size={18} />
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Item leftSection={<IconPencil size={14} />} onClick={openEdit}>Edit</Menu.Item>
            <Menu.Item leftSection={<IconTrash size={14} />} color="red" onClick={() => setConfirmingDelete(true)}>
              Delete list
            </Menu.Item>
          </Menu.Dropdown>
        </Menu>
      }
    >
      <div style={{
        borderLeft: `3px solid ${list.color || 'var(--db-border-strong)'}`,
        paddingLeft: 12,
        marginBottom: 16,
      }}>
        <div style={{ fontSize: 20, fontWeight: 500 }}>{list.name}</div>
        {list.description && (
          <div style={{ fontSize: 13, color: 'var(--db-text-tertiary)', marginTop: 4 }}>{list.description}</div>
        )}
      </div>

      {error && (
        <div style={{
          background: 'var(--db-red-bg)', color: 'var(--db-red-text)',
          padding: 12, borderRadius: 'var(--db-radius)', marginBottom: 12, fontSize: 13,
        }}>{error}</div>
      )}

      {list.items.length === 0 ? (
        <EmptyState
          icon={<IconBookmark size={32} />}
          title="No bookmarks yet"
          description="Open an insight or recommendation and use the bookmark button to add it to this list."
        />
      ) : (
        <>
          {insightItems.length > 0 && (
            <>
              <SectionHeader title="Insights" count={insightItems.length} />
              <Stack gap="xs" mb="lg">
                {insightItems.map((it) => renderItem(it, id, handleRemoveBookmark))}
              </Stack>
            </>
          )}
          {recommendationItems.length > 0 && (
            <>
              <SectionHeader title="Recommendations" count={recommendationItems.length} />
              <Stack gap="xs">
                {recommendationItems.map((it) => renderItem(it, id, handleRemoveBookmark))}
              </Stack>
            </>
          )}
        </>
      )}

      <Modal opened={editing} onClose={() => setEditing(false)} title="Edit list" centered>
        <Stack gap="sm">
          <TextInput
            label="Name"
            value={editName}
            onChange={e => setEditName(e.currentTarget.value)}
            required
            maxLength={200}
          />
          <Textarea
            label="Description"
            value={editDescription}
            onChange={e => setEditDescription(e.currentTarget.value)}
            autosize
            minRows={2}
          />
          <ColorInput label="Color" value={editColor} onChange={setEditColor} format="hex" />
          <Group justify="flex-end" mt="sm">
            <Button variant="default" onClick={() => setEditing(false)} disabled={submitting}>Cancel</Button>
            <Button onClick={handleSave} loading={submitting} disabled={!editName.trim()}>Save</Button>
          </Group>
        </Stack>
      </Modal>

      <Modal
        opened={confirmingDelete}
        onClose={() => setConfirmingDelete(false)}
        title="Delete this list?"
        centered
      >
        <Text size="sm" mb="md">
          This will permanently remove the list and all {list.item_count} bookmark{list.item_count === 1 ? '' : 's'} in it.
          The underlying insights and recommendations are not affected.
        </Text>
        <Group justify="flex-end">
          <Button variant="default" onClick={() => setConfirmingDelete(false)}>Cancel</Button>
          <Button color="red" onClick={handleDeleteList}>Delete</Button>
        </Group>
      </Modal>
    </Shell>
  );
}

function renderItem(
  it: BookmarkListWithItems['items'][number],
  projectId: string,
  onRemove: (bookmarkId: string) => void,
) {
  const { bookmark, target, deleted } = it;

  if (deleted || !target) {
    return (
      <div
        key={bookmark.id}
        style={{
          border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius)',
          padding: '10px 14px',
          display: 'flex', alignItems: 'center', justifyContent: 'space-between',
          opacity: 0.55,
        }}
      >
        <div>
          <Text size="sm" fw={500}>[removed]</Text>
          <Text size="xs" c="dimmed">
            This {bookmark.target_type} has been deleted.
          </Text>
        </div>
        <ActionIcon variant="subtle" color="red" onClick={() => onRemove(bookmark.id)} aria-label="Remove bookmark">
          <IconTrash size={14} />
        </ActionIcon>
      </div>
    );
  }

  const href = bookmark.target_type === 'insight'
    ? `/projects/${projectId}/discoveries/${bookmark.discovery_id}/insights/${bookmark.target_id}`
    : `/projects/${projectId}/discoveries/${bookmark.discovery_id}/recommendations/${bookmark.target_id}`;

  const title = bookmark.target_type === 'insight'
    ? (target as StandaloneInsight).name
    : (target as StandaloneRecommendation).title;
  const description = (target as StandaloneInsight).description
    || (target as StandaloneRecommendation).description
    || '';

  return (
    <div
      key={bookmark.id}
      style={{
        border: '1px solid var(--db-border-default)',
        borderRadius: 'var(--db-radius)',
        padding: '10px 14px',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 12,
        transition: 'border-color 120ms ease',
      }}
      onMouseEnter={e => { e.currentTarget.style.borderColor = 'var(--db-border-strong)'; }}
      onMouseLeave={e => { e.currentTarget.style.borderColor = 'var(--db-border-default)'; }}
    >
      <Link href={href} style={{ textDecoration: 'none', color: 'inherit', flex: 1, minWidth: 0 }}>
        <Text size="sm" fw={500}>{title}</Text>
        {description && (
          <Text size="xs" c="dimmed" lineClamp={2} mt={2}>{description}</Text>
        )}
        {bookmark.note && (
          <Text size="xs" fs="italic" c="dimmed" mt={4}>Note: {bookmark.note}</Text>
        )}
      </Link>
      <ActionIcon variant="subtle" color="red" onClick={() => onRemove(bookmark.id)} aria-label="Remove bookmark">
        <IconTrash size={14} />
      </ActionIcon>
    </div>
  );
}
