'use client';

import { ActionIcon, Tooltip } from '@mantine/core';
import { IconMessageCircle } from '@tabler/icons-react';
import { SeedContext } from '@/lib/api';
import { useChatDrawer } from '@/components/ask/ChatDrawerProvider';

// AskAboutThisButton is the compact per-row trigger that opens the Ask drawer
// seeded with one insight / recommendation. Renders nothing when no drawer
// provider is mounted, so list pages can drop it in unconditionally.
export default function AskAboutThisButton({ projectId, seed }: { projectId: string; seed: SeedContext }) {
  const ctx = useChatDrawer();
  if (!ctx) return null;
  return (
    <Tooltip label={`Ask about this ${seed.type}`} withArrow>
      <ActionIcon
        variant="subtle"
        color="blue"
        size="sm"
        aria-label={`Ask about this ${seed.type}`}
        onClick={(e) => { e.stopPropagation(); e.preventDefault(); ctx.openWithSeed(projectId, seed); }}
      >
        <IconMessageCircle size={15} />
      </ActionIcon>
    </Tooltip>
  );
}
