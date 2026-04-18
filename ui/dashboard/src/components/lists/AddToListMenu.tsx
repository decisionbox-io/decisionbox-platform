'use client';

import { useEffect, useState } from 'react';
import {
  Popover, Button, Stack, Checkbox, Loader, ScrollArea, Text, Divider,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { api, BookmarkList } from '@/lib/api';
import CreateListModal from './CreateListModal';

interface Props {
  projectId: string;
  discoveryId: string;
  targetType: 'insight' | 'recommendation';
  targetId: string;
  // Called whenever the set of lists containing this target changes, so parent
  // BookmarkButton can update its fill state without re-querying.
  onMembershipChange?: (listIds: string[]) => void;
  children: React.ReactNode; // the trigger (usually BookmarkButton)
}

// AddToListMenu shows a popover listing the user's bookmark lists with
// checkboxes reflecting current membership. Toggling a checkbox bookmarks
// the target into that list or removes it. A "New list…" inline form at the
// bottom creates a list and adds the item in one flow.
export default function AddToListMenu({
  projectId, discoveryId, targetType, targetId, onMembershipChange, children,
}: Props) {
  const [opened, setOpened] = useState(false);
  const [lists, setLists] = useState<BookmarkList[] | null>(null);
  // Map of listId -> bookmarkId for lists containing this target.
  // We need the bookmarkId to remove — the server identifies bookmarks by id,
  // not by (list, target) composite — so we fetch list detail lazily on remove.
  const [membership, setMembership] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<Set<string>>(new Set());

  const [creating, setCreating] = useState(false);

  async function load() {
    try {
      const [all, containing] = await Promise.all([
        api.listBookmarkLists(projectId),
        api.listsContaining(projectId, targetType, targetId),
      ]);
      setLists(all || []);
      const set = new Set(containing || []);
      setMembership(set);
      onMembershipChange?.(Array.from(set));
    } catch {
      setLists([]);
    }
  }

  useEffect(() => {
    if (opened && lists === null) {
      load();
    }
  }, [opened]); // eslint-disable-line react-hooks/exhaustive-deps

  function setBusyFor(listId: string, on: boolean) {
    setBusy(prev => {
      const next = new Set(prev);
      if (on) next.add(listId); else next.delete(listId);
      return next;
    });
  }

  async function toggle(list: BookmarkList) {
    if (busy.has(list.id)) return;
    setBusyFor(list.id, true);
    try {
      if (membership.has(list.id)) {
        // Remove — need the bookmark id from the list detail.
        const detail = await api.getBookmarkList(projectId, list.id);
        const match = detail.items.find(it =>
          it.bookmark.target_type === targetType && it.bookmark.target_id === targetId
        );
        if (match) {
          await api.removeBookmark(projectId, list.id, match.bookmark.id);
        }
        const next = new Set(membership);
        next.delete(list.id);
        setMembership(next);
        onMembershipChange?.(Array.from(next));
      } else {
        await api.addBookmark(projectId, list.id, { discovery_id: discoveryId, target_type: targetType, target_id: targetId });
        const next = new Set(membership);
        next.add(list.id);
        setMembership(next);
        onMembershipChange?.(Array.from(next));
      }
    } finally {
      setBusyFor(list.id, false);
    }
  }

  // When the create modal emits onCreated, add the current target to the
  // brand-new list so the full create → bookmark flow happens in one step.
  async function handleListCreated(list: BookmarkList) {
    await api.addBookmark(projectId, list.id, {
      discovery_id: discoveryId, target_type: targetType, target_id: targetId,
    });
    // Refresh so the new list shows up with its checkbox already checked.
    await load();
  }

  return (
    <Popover
      opened={opened}
      onChange={setOpened}
      position="bottom-end"
      width={260}
      shadow="md"
      trapFocus
    >
      <Popover.Target>
        <div onClick={() => setOpened(o => !o)} style={{ cursor: 'pointer' }}>
          {children}
        </div>
      </Popover.Target>
      <Popover.Dropdown p="xs">
        {lists === null ? (
          <div style={{ padding: 16, textAlign: 'center' }}><Loader size="sm" /></div>
        ) : (
          <Stack gap={4}>
            {lists.length === 0 && !creating && (
              <Text size="xs" c="dimmed" p="xs">No lists yet. Create one below.</Text>
            )}
            {lists.length > 0 && (
              <ScrollArea.Autosize mah={220}>
                <Stack gap={0}>
                  {lists.map(l => (
                    <Checkbox
                      key={l.id}
                      label={l.name}
                      checked={membership.has(l.id)}
                      onChange={() => toggle(l)}
                      disabled={busy.has(l.id)}
                      styles={{ root: { padding: '6px 4px' }, label: { fontSize: 13 } }}
                    />
                  ))}
                </Stack>
              </ScrollArea.Autosize>
            )}
            {lists.length > 0 && <Divider my={4} />}
            <Button
              size="xs"
              variant="subtle"
              leftSection={<IconPlus size={14} />}
              onClick={() => { setOpened(false); setCreating(true); }}
              justify="flex-start"
              fullWidth
            >
              New list…
            </Button>
          </Stack>
        )}
      </Popover.Dropdown>

      {/* Shared create-list modal — same form as the /lists page. Opens when
          the user clicks "New list…" inside the popover. onCreated fires after
          the list is saved, adding the current target in one flow. */}
      <CreateListModal
        projectId={projectId}
        opened={creating}
        onClose={() => setCreating(false)}
        onCreated={handleListCreated}
      />
    </Popover>
  );
}
