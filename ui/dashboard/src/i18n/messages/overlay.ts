import type { Messages } from '../deepMerge';

// Enterprise catalog overlay — community stub (empty).
//
// This file is a same-path overlay point: the enterprise image replaces it (via
// the existing `COPY ui/src/ src/`) with a version that statically imports the
// `*.enterprise.json` catalogs and exports them here, keyed by locale. The
// community build keeps this empty stub, so the loader merges base-only.
//
// Using a replaced module (rather than a dynamic import of an optional file)
// keeps the merge fully static: no bundler warnings, traced into the standalone
// output, and works identically under `next build`, `next dev`, and Jest.
export const ENTERPRISE_OVERLAY: Record<string, Messages> = {};
