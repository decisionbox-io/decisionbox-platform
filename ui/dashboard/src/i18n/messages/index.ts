// Message catalog loader — platform base + optional enterprise overlay,
// deep-merged at runtime.
//
// Layout after the enterprise image's overlay COPY:
//   <locale>.json             base UI strings (this repo, source of truth)
//   <locale>.enterprise.json  enterprise-only keys + overrides (enterprise repo)
//   overlay.ts                enterprise-replaced module that wires those in
//
// The community build ships only the base files + an empty overlay stub; the
// enterprise build also has the `.enterprise.json` files and an overlay.ts that
// imports them (both ride the existing `COPY ui/src/ src/`, no Dockerfile
// change). The overlay is deep-merged over the base at load time, enterprise
// winning on collisions, and is simply absent (empty) in the community build.
//
// Adding a language: drop `<locale>.json` (+ optional `<locale>.enterprise.json`)
// and register the locale in BASE below (a static import + one entry). No
// component or extraction changes — the switcher and resolver read BASE.

import en from './en.json';
import tr from './tr.json';
import { deepMerge, type Messages } from '../deepMerge';
import { ENTERPRISE_OVERLAY } from './overlay';

// Base catalogs. Static imports so they are always bundled and traced into the
// standalone output. Register a new locale here.
const BASE: Record<string, Messages> = { en, tr };

// SUPPORTED_LOCALES — the locales the app offers (base files define the set;
// enterprise-only files never introduce a locale on their own). Sorted for a
// stable switcher order.
export const SUPPORTED_LOCALES: string[] = Object.keys(BASE).sort();

// loadMessages returns the deep-merged catalog for a locale (base, then the
// enterprise overlay when present). Falls back to the default base catalog for
// an unknown locale.
export function loadMessages(locale: string): Messages {
  const base = BASE[locale] ?? BASE.en ?? {};
  const overlay = ENTERPRISE_OVERLAY[locale] ?? {};
  return deepMerge(base, overlay);
}
