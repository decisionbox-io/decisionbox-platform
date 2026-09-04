'use client';

import { useLocale, useTranslations } from 'next-intl';
import { useRouter } from 'next/navigation';
import { Menu, ActionIcon } from '@mantine/core';
import { IconWorld, IconCheck } from '@tabler/icons-react';
import { api } from '@/lib/api';
import { localeLabel, setLocaleCookie } from '@/i18n/locales';
import { SUPPORTED_LOCALES } from '@/i18n/messages';

// Language switcher for the app-shell header. Changing the language:
//   1. mirrors the choice into the NEXT_LOCALE cookie (SSR fast path),
//   2. persists it to the per-user preference in Mongo (durable, cross-device),
//   3. re-renders so server components pick up the new locale (which also
//      updates <html lang/dir> and the dayjs/date-picker locale).
// Hidden when only one language is available.
export default function LanguageSwitcher() {
  const locale = useLocale();
  const router = useRouter();
  const t = useTranslations('language');

  if (SUPPORTED_LOCALES.length < 2) return null;

  const change = (next: string) => {
    if (next === locale) return;
    setLocaleCookie(next);
    // Best-effort durable persistence — the cookie already carries the choice
    // for this browser, so a failed write must not block the switch.
    api.updatePreferences(next).catch(() => {});
    router.refresh();
  };

  return (
    <Menu position="bottom-end" width={180} withinPortal>
      <Menu.Target>
        <ActionIcon variant="subtle" color="gray" aria-label={t('label')} title={t('label')}>
          <IconWorld size={18} />
        </ActionIcon>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('label')}</Menu.Label>
        {SUPPORTED_LOCALES.map((code) => (
          <Menu.Item
            key={code}
            onClick={() => change(code)}
            rightSection={code === locale ? <IconCheck size={14} /> : null}
          >
            {localeLabel(code)}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}
