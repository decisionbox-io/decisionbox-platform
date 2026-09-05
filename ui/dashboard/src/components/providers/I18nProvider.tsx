'use client';

import { ReactNode, useEffect, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { DatesProvider } from '@mantine/dates';
import dayjs from 'dayjs';
import { api } from '@/lib/api';
import { setLocaleCookie } from '@/i18n/locales';
import { SUPPORTED_LOCALES } from '@/i18n/messages';

// Client-side i18n concerns that hang off the server-resolved locale:
//  1. Mantine date pickers get the active locale (via DatesProvider + dayjs).
//  2. The durable per-user preference (Mongo) is reconciled into the cookie so a
//     fresh browser for a signed-in user adopts their saved language.
//
// The catalogs and the <html lang/dir> are handled by the server layout +
// NextIntlClientProvider; this only covers the pieces that must run on the
// client.
export default function I18nProvider({
  locale,
  children,
}: {
  locale: string;
  children: ReactNode;
}) {
  const router = useRouter();
  const reconciled = useRef(false);

  // Load the dayjs locale data so Mantine date pickers render month/day names
  // in the active language. 'en' is dayjs's built-in default.
  useEffect(() => {
    if (locale === 'en') {
      dayjs.locale('en');
      return;
    }
    let cancelled = false;
    import(`dayjs/locale/${locale}.js`)
      .then(() => {
        if (!cancelled) dayjs.locale(locale);
      })
      .catch(() => {
        // Unknown dayjs locale — leave the previous one in place.
      });
    return () => {
      cancelled = true;
    };
  }, [locale]);

  // Reconcile the durable preference into the cookie. Runs once: if the stored
  // locale is a supported one and differs from what this render resolved to,
  // mirror it into the cookie and re-render so server components pick it up.
  useEffect(() => {
    if (reconciled.current) return;
    reconciled.current = true;
    api
      .getPreferences()
      .then((prefs) => {
        const stored = prefs?.locale;
        if (stored && stored !== locale && SUPPORTED_LOCALES.includes(stored)) {
          setLocaleCookie(stored);
          router.refresh();
        }
      })
      .catch(() => {
        // No stored preference / unauthenticated — the cookie or fallback stands.
      });
  }, [locale, router]);

  return <DatesProvider settings={{ locale }}>{children}</DatesProvider>;
}
