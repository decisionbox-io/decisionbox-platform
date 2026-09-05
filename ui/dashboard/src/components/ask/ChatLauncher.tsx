'use client';

import { usePathname } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { IconMessageCircle } from '@tabler/icons-react';
import { useChatDrawer } from '@/components/ask/ChatDrawerProvider';

// ChatLauncher is the persistent, project-scoped entry point to the Ask drawer
// from any page. It sits bottom-right (clear of the top-right QuestionsDrawer
// tab), resolves the project from the route, and opens a generic chat. It hides
// itself off project routes, on the full Ask page (redundant there), and while
// the drawer is open.
export default function ChatLauncher() {
  const t = useTranslations('askUi');
  const ctx = useChatDrawer();
  const pathname = usePathname() || '';

  const match = pathname.match(/\/projects\/([^/]+)/);
  const projectId = match?.[1];

  if (!ctx || !projectId || projectId === 'new') return null;
  if (pathname.endsWith('/ask')) return null;
  if (ctx.open) return null;

  return (
    <button
      type="button"
      aria-label={t('launcherAriaLabel')}
      title={t('ask')}
      onClick={() => ctx.openGeneric(projectId)}
      style={{
        position: 'fixed', right: 20, bottom: 20, zIndex: 40,
        display: 'flex', alignItems: 'center', gap: 8,
        background: 'var(--db-text-link, #2563eb)', color: '#fff',
        border: 'none', borderRadius: 999, padding: '10px 16px',
        cursor: 'pointer', fontSize: 14, fontWeight: 600, fontFamily: 'inherit',
        boxShadow: '0 4px 14px rgba(0,0,0,0.18)',
      }}
    >
      <IconMessageCircle size={18} />
      <span>{t('ask')}</span>
    </button>
  );
}
