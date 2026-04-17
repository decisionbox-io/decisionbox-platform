'use client';

import { useState, useEffect } from 'react';
import {
  Modal, Stack, TextInput, Textarea, ColorInput, Group, Button, Text,
} from '@mantine/core';
import { api, BookmarkList } from '@/lib/api';

interface Props {
  projectId: string;
  opened: boolean;
  onClose: () => void;
  // Called with the newly-created list when the user submits successfully.
  // Callers that want to add an item to the list right after creation should
  // do so here.
  onCreated?: (list: BookmarkList) => void | Promise<void>;
}

// CreateListModal is the shared "New list" dialog used by both the /lists page
// and the AddToListMenu on detail pages. Presents name, description, and a
// color swatch picker with sensible defaults.
export default function CreateListModal({ projectId, opened, onClose, onCreated }: Props) {
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [color, setColor] = useState('#2563eb');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Reset form each time the modal opens so stale input from a previous
  // cancelled session doesn't surface.
  useEffect(() => {
    if (opened) {
      setName('');
      setDescription('');
      setColor('#2563eb');
      setError(null);
    }
  }, [opened]);

  async function handleCreate() {
    const trimmed = name.trim();
    if (!trimmed) return;
    setSubmitting(true);
    setError(null);
    try {
      const created = await api.createBookmarkList(projectId, {
        name: trimmed,
        description: description.trim() || undefined,
        color: color || undefined,
      });
      await onCreated?.(created);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal opened={opened} onClose={onClose} title="New list" centered>
      <Stack gap="sm">
        <TextInput
          label="Name"
          placeholder="Retention ideas"
          value={name}
          onChange={e => setName(e.currentTarget.value)}
          required
          maxLength={200}
          data-autofocus
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) handleCreate();
          }}
        />
        <Textarea
          label="Description"
          placeholder="Optional — what kind of items will you collect here?"
          value={description}
          onChange={e => setDescription(e.currentTarget.value)}
          autosize
          minRows={2}
        />
        <ColorInput
          label="Color"
          value={color}
          onChange={setColor}
          format="hex"
          swatches={['#2563eb', '#16a34a', '#dc2626', '#ea580c', '#9333ea', '#0891b2']}
        />
        <Group justify="flex-end" mt="sm">
          <Button variant="default" onClick={onClose} disabled={submitting}>Cancel</Button>
          <Button onClick={handleCreate} loading={submitting} disabled={!name.trim()}>
            Create
          </Button>
        </Group>
        {error && <Text size="xs" c="red">{error}</Text>}
      </Stack>
    </Modal>
  );
}
