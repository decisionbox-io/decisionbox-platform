'use client';

import { useEffect, useState } from 'react';
import { ActionIcon, Tooltip } from '@mantine/core';
import { IconBookmark, IconBookmarkFilled } from '@tabler/icons-react';
import { api } from '@/lib/api';
import AddToListMenu from './AddToListMenu';

interface Props {
  projectId: string;
  discoveryId: string;
  targetType: 'insight' | 'recommendation';
  targetId: string;
  // 'icon' renders an icon-only button (detail page headers, list row menus);
  // 'chip' renders a small button with a label (unused today but part of the API).
  variant?: 'icon' | 'chip';
  size?: 'sm' | 'md' | 'lg';
}

// BookmarkButton shows a bookmark icon that fills when the target is in any
// list, and opens AddToListMenu on click. The initial fill state is fetched
// via api.listsContaining; membership changes from the menu update it.
export default function BookmarkButton({
  projectId, discoveryId, targetType, targetId, variant = 'icon', size = 'md',
}: Props) {
  const [containing, setContaining] = useState<string[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.listsContaining(projectId, targetType, targetId)
      .then(ids => { if (!cancelled) setContaining(ids || []); })
      .catch(() => { if (!cancelled) setContaining([]); });
    return () => { cancelled = true; };
  }, [projectId, targetType, targetId]);

  const bookmarked = (containing?.length ?? 0) > 0;
  const Icon = bookmarked ? IconBookmarkFilled : IconBookmark;
  const label = bookmarked
    ? `Bookmarked in ${containing!.length} list${containing!.length === 1 ? '' : 's'}`
    : 'Add to list';

  return (
    <AddToListMenu
      projectId={projectId}
      discoveryId={discoveryId}
      targetType={targetType}
      targetId={targetId}
      onMembershipChange={setContaining}
    >
      {variant === 'icon' ? (
        <Tooltip label={label} withArrow>
          <ActionIcon
            variant="subtle"
            size={size === 'sm' ? 'sm' : 'md'}
            color={bookmarked ? 'blue' : 'gray'}
            aria-label={label}
          >
            <Icon size={size === 'sm' ? 14 : 16} />
          </ActionIcon>
        </Tooltip>
      ) : (
        <span
          role="button"
          aria-label={label}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 4,
            padding: '4px 8px', fontSize: 12,
            border: '1px solid var(--db-border-default)',
            borderRadius: 'var(--db-radius)',
            background: bookmarked ? 'var(--db-blue-bg, var(--db-bg-white))' : 'var(--db-bg-white)',
            color: bookmarked ? 'var(--db-blue-text, var(--db-text-primary))' : 'var(--db-text-secondary)',
            cursor: 'pointer',
          }}
        >
          <Icon size={14} />
          {bookmarked ? 'Bookmarked' : 'Bookmark'}
        </span>
      )}
    </AddToListMenu>
  );
}
