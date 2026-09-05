// Pure catalog-merge helpers, kept free of any bundler API (no require.context /
// dynamic import) so unit tests can exercise them directly.

export type Messages = Record<string, unknown>;

export function isPlainObject(v: unknown): v is Messages {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

// deepMerge overlays `overlay` onto `base`, recursing into nested objects.
// Overlay leaves win on collision — this is what lets the enterprise catalog
// override a base label (e.g. nav.askInsights -> "Ask").
export function deepMerge(base: Messages, overlay: Messages): Messages {
  const out: Messages = { ...base };
  for (const [key, value] of Object.entries(overlay)) {
    const existing = out[key];
    out[key] = isPlainObject(existing) && isPlainObject(value)
      ? deepMerge(existing, value)
      : value;
  }
  return out;
}
