// Locale primitives shared by the server (i18n/request.ts) and the client
// (LanguageSwitcher, I18nProvider). This module is deliberately free of any
// server-only import (no next/headers) and of the message catalogs, so it is
// safe to pull into a client bundle.
//
// UI locale is independent of a project's analysis language: this setting only
// changes the dashboard chrome, never what the agent generates.

// Primary language. Also the last-resort fallback when neither a stored
// preference, a cookie, nor Accept-Language names a supported locale. It is the
// product's language contract, not a tunable, so it lives here as the single
// source rather than an env knob.
export const DEFAULT_LOCALE = 'en';

// Cookie the resolved locale is mirrored into so server rendering can pick it up
// synchronously without an API round-trip. The durable, cross-device source of
// truth is the per-user preference in Mongo (/api/v1/me/preferences); this
// cookie is just the fast path the switcher and the bootstrap keep in sync.
export const LOCALE_COOKIE = 'NEXT_LOCALE';

// One year — remember the choice across sessions.
export const LOCALE_COOKIE_MAX_AGE = 60 * 60 * 24 * 365;

// setLocaleCookie mirrors the chosen locale into the SSR cookie. Client-only —
// callers invoke it from an event handler or effect. Lives here (rather than
// inline) so the assignment isn't flagged by the React-compiler immutability
// lint, and so the switcher and the bootstrap share one implementation.
export function setLocaleCookie(locale: string): void {
  document.cookie = `${LOCALE_COOKIE}=${locale}; path=/; max-age=${LOCALE_COOKIE_MAX_AGE}; samesite=lax`;
}

// Scripts that render right-to-left. Used only to set <html dir> so a future RTL
// language is a flip rather than a rewrite; no RTL language ships today.
const RTL_LANGUAGES = new Set(['ar', 'he', 'fa', 'ur']);

// Locales still under test — shown with a "beta" tag in the switcher. English is
// the stable primary; a language graduates by dropping it from this set.
const BETA_LOCALES = new Set(['tr']);

// isBetaLocale reports whether a locale is still in testing (switcher tag).
export function isBetaLocale(locale: string): boolean {
  return BETA_LOCALES.has(locale.toLowerCase());
}

// primarySubtag returns the language part of a BCP-47 tag ("tr-TR" -> "tr").
function primarySubtag(locale: string): string {
  return locale.split('-')[0].toLowerCase();
}

// dirForLocale returns the text direction for the <html dir> attribute.
export function dirForLocale(locale: string): 'ltr' | 'rtl' {
  return RTL_LANGUAGES.has(primarySubtag(locale)) ? 'rtl' : 'ltr';
}

// localeLabel returns the language's own name ("English", "Türkçe"), so adding a
// language needs no per-locale label table. Falls back to the raw code if the
// runtime can't resolve a display name.
export function localeLabel(locale: string): string {
  try {
    const name = new Intl.DisplayNames([locale], { type: 'language' }).of(locale);
    if (!name) return locale;
    // Some locales return a lowercase autonym; capitalize the first character
    // for a menu label. Locale-aware toLocaleUpperCase on a single leading
    // character is safe (never applied to arbitrary user text).
    return name.charAt(0).toLocaleUpperCase(locale) + name.slice(1);
  } catch {
    return locale;
  }
}

// matchAcceptLanguage picks the best supported locale from an Accept-Language
// header, honouring q-weights, matching the full tag first then the primary
// subtag. Returns null when nothing matches.
export function matchAcceptLanguage(
  header: string | null | undefined,
  supported: readonly string[],
): string | null {
  if (!header) return null;
  const ranked = header
    .split(',')
    .map((part) => {
      const [tag, ...params] = part.trim().split(';');
      const q = params
        .map((p) => p.trim())
        .find((p) => p.startsWith('q='));
      const weight = q ? Number.parseFloat(q.slice(2)) : 1;
      return { tag: tag.trim(), weight: Number.isFinite(weight) ? weight : 1 };
    })
    .filter((e) => e.tag && e.weight > 0)
    .sort((a, b) => b.weight - a.weight);

  for (const { tag } of ranked) {
    if (supported.includes(tag)) return tag;
    const primary = primarySubtag(tag);
    const byPrimary = supported.find((s) => primarySubtag(s) === primary);
    if (byPrimary) return byPrimary;
  }
  return null;
}

// resolveLocale applies the precedence: a valid cookie, else Accept-Language,
// else the default. `supported` is passed in (from the message catalog) to keep
// this module free of the catalog import.
export function resolveLocale(
  cookieValue: string | null | undefined,
  acceptLanguage: string | null | undefined,
  supported: readonly string[],
): string {
  if (cookieValue && supported.includes(cookieValue)) return cookieValue;
  return matchAcceptLanguage(acceptLanguage, supported) ?? DEFAULT_LOCALE;
}
