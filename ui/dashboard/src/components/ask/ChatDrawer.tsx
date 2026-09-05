'use client';

import { Drawer, Group, Text } from '@mantine/core';
import { IconMessageCircle } from '@tabler/icons-react';
import { useTranslations } from 'next-intl';
import ChatPanel from '@/components/ask/ChatPanel';
import { useChatDrawer } from '@/components/ask/ChatDrawerProvider';

// ChatDrawer is the global, right-side Ask conversation. It renders the SAME
// <ChatPanel> the /ask page uses, so behaviour and styling never diverge. It is
// non-blocking (no overlay / focus trap / scroll lock) so the user can keep
// reading the page while chatting, and keepMounted so the conversation survives
// close/reopen and client-side navigation. The panel is keyed by project +
// seedNonce: a new "Ask about this" remounts a fresh seeded chat, while the
// generic launcher resumes the current conversation.
export default function ChatDrawer() {
  const t = useTranslations('askUi');
  const ctx = useChatDrawer();
  if (!ctx || !ctx.projectId) return null;

  const { open, projectId, seedContext, initialQuestion, seedNonce, close } = ctx;

  return (
    <Drawer
      opened={open}
      onClose={close}
      position="right"
      size={460}
      keepMounted
      withOverlay={false}
      trapFocus={false}
      lockScroll={false}
      closeOnClickOutside={false}
      title={
        <Group gap="xs">
          <IconMessageCircle size={18} />
          <Text fw={600}>{t('ask')}</Text>
        </Group>
      }
      styles={{
        body: { height: 'calc(100% - 60px)', padding: 0 },
        content: { display: 'flex', flexDirection: 'column' },
      }}
    >
      <ChatPanel
        key={`${projectId}-${seedNonce}`}
        projectId={projectId}
        seedContext={seedContext}
        initialQuestion={initialQuestion}
        showHistory={false}
      />
    </Drawer>
  );
}
