# Dashboard languages (i18n)

The dashboard UI can render in more than one language.
English is the default; Turkish ships alongside it.
This covers the **UI chrome only** — navigation, buttons, labels, empty states, errors.
It does **not** translate analysed content (insights, recommendations, Ask answers): that follows each project's own `Language` setting and is independent of the UI language.

## Switching language

Use the language picker (globe icon) in the app header.
The choice is:

- **Persisted per user** in MongoDB via `GET`/`PUT /api/v1/me/preferences` (`{ "locale": "tr" }`), so it follows you across reloads, navigation, and devices.
- **Mirrored to a cookie** (`NEXT_LOCALE`) so server rendering picks it up immediately.

When no preference is stored, the locale falls back to the browser's `Accept-Language`, then to English.

## How it works

- **Library:** [next-intl](https://next-intl.dev) in App-Router "without i18n routing" mode — the locale comes from a cookie, not a URL segment, so no routes are restructured.
- **Catalogs:** message files live in `ui/dashboard/src/i18n/messages/<locale>.json`.
- **Formatting:** numbers, dates, and currency use `Intl.NumberFormat` / `Intl.DateTimeFormat` bound to the active locale (see `src/lib/format.ts`); Mantine date pickers use the matching dayjs locale.
- **Casing:** never use `toUpperCase()` / `toLowerCase()` on user-facing text — Turkish dotted/dotless İ/ı breaks under naïve casing. Use locale-aware casing or CSS `text-transform`.

## Key convention

Keys are dot-namespaced by area, for example:

```
nav.discoveryRuns
common.save
projects.emptyTitle
```

Use `useTranslations('<namespace>')` in a component and call `t('<key>')`:

```tsx
import { useTranslations } from 'next-intl';

const t = useTranslations('projects');
// ...
<Button>{t('newProject')}</Button>
```

Keep a consistent glossary for product terms so translations stay stable:

| English | Turkish |
|---|---|
| Insight | İçgörü |
| Recommendation | Öneri |
| Discovery | Keşif |
| Warehouse | Veri ambarı |
| Executive Summary | Yönetici Özeti |
| DecisionBox | DecisionBox (brand — never translated) |

## Adding a language

Adding a language is a drop-in — no component or extraction changes:

1. Add `ui/dashboard/src/i18n/messages/<locale>.json` (copy `en.json` and translate every value).
2. Register the locale in `ui/dashboard/src/i18n/messages/index.ts` (a static import + one `BASE` entry).
3. Rebuild. The switcher lists it automatically (its label comes from `Intl.DisplayNames`), and the resolver accepts it.

Keys must match `en.json` exactly — a parity test fails the build if a locale is missing a key or carries an extra one.

## Right-to-left readiness

Migrated components use CSS logical properties (`margin-inline`, `padding-inline`, `inset-inline-*`) and set `<html dir>` from the locale, so adding a right-to-left language later is a flip rather than a rewrite.
No right-to-left language ships today.
