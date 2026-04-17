'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import Link from 'next/link';
import { Button, Loader, Menu, ActionIcon, Modal, Text, Group } from '@mantine/core';
import { IconBookmark, IconPlus, IconDots, IconTrash } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { EmptyState, SectionHeader } from '@/components/common/UIComponents';
import CreateListModal from '@/components/lists/CreateListModal';
import { api, BookmarkList } from '@/lib/api';

export default function ListsPage() {
  const { id } = useParams<{ id: string }>();
  const [lists, setLists] = useState<BookmarkList[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const [creating, setCreating] = useState(false);
  // Deleting-from-card-menu state. Shared confirm dialog, one list at a time.
  const [deleteTarget, setDeleteTarget] = useState<BookmarkList | null>(null);

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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  async function confirmDelete() {
    if (!deleteTarget) return;
    try {
      await api.deleteBookmarkList(id, deleteTarget.id);
      setDeleteTarget(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
      setDeleteTarget(null);
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
                // Only touch the three non-coloured borders on hover; setting
                // shorthand borderColor would wipe out the accent left border.
                onMouseEnter={e => {
                  e.currentTarget.style.borderTopColor = 'var(--db-border-strong)';
                  e.currentTarget.style.borderRightColor = 'var(--db-border-strong)';
                  e.currentTarget.style.borderBottomColor = 'var(--db-border-strong)';
                }}
                onMouseLeave={e => {
                  e.currentTarget.style.borderTopColor = 'var(--db-border-default)';
                  e.currentTarget.style.borderRightColor = 'var(--db-border-default)';
                  e.currentTarget.style.borderBottomColor = 'var(--db-border-default)';
                }}
              >
                <div style={{
                  display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 8,
                  marginBottom: 4,
                }}>
                  <div style={{
                    fontSize: 15, fontWeight: 500, color: 'var(--db-text-primary)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    flex: 1, minWidth: 0,
                  }}>{list.name}</div>
                  {/* Per-card menu. Must stopPropagation so clicks don't
                      navigate into the list detail page (the card is wrapped
                      in a Link). */}
                  <Menu position="bottom-end" width={140} withinPortal>
                    <Menu.Target>
                      <ActionIcon
                        variant="subtle"
                        size="sm"
                        aria-label="List actions"
                        onClick={e => { e.preventDefault(); e.stopPropagation(); }}
                      >
                        <IconDots size={14} />
                      </ActionIcon>
                    </Menu.Target>
                    <Menu.Dropdown>
                      <Menu.Item
                        color="red"
                        leftSection={<IconTrash size={14} />}
                        onClick={e => {
                          e.preventDefault();
                          e.stopPropagation();
                          setDeleteTarget(list);
                        }}
                      >
                        Delete
                      </Menu.Item>
                    </Menu.Dropdown>
                  </Menu>
                </div>
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

      <CreateListModal
        projectId={id}
        opened={creating}
        onClose={() => setCreating(false)}
        onCreated={() => load()}
      />

      <Modal
        opened={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title="Delete this list?"
        centered
      >
        {deleteTarget && (
          <>
            <Text size="sm" mb="md">
              This will permanently remove &quot;{deleteTarget.name}&quot; and all
              {' '}{deleteTarget.item_count} bookmark{deleteTarget.item_count === 1 ? '' : 's'} in it.
              The underlying insights and recommendations are not affected.
            </Text>
            <Group justify="flex-end">
              <Button variant="default" onClick={() => setDeleteTarget(null)}>Cancel</Button>
              <Button color="red" onClick={confirmDelete}>Delete</Button>
            </Group>
          </>
        )}
      </Modal>
    </Shell>
  );
}
