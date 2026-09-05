import { getRequestConfig } from 'next-intl/server';
import { cookies, headers } from 'next/headers';
import { LOCALE_COOKIE, resolveLocale } from './locales';
import { SUPPORTED_LOCALES, loadMessages } from './messages';

// Per-request locale + messages for next-intl's App Router "without i18n
// routing" mode: the locale comes from the NEXT_LOCALE cookie (falling back to
// Accept-Language, then the default), not a URL segment. Runs during the Server
// Components render pass, so cookies()/headers() are available.
export default getRequestConfig(async () => {
  const [cookieStore, headerStore] = await Promise.all([cookies(), headers()]);
  const locale = resolveLocale(
    cookieStore.get(LOCALE_COOKIE)?.value,
    headerStore.get('accept-language'),
    SUPPORTED_LOCALES,
  );
  return { locale, messages: loadMessages(locale) };
});
