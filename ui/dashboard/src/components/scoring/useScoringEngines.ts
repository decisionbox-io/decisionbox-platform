'use client';

import { useEffect, useState } from 'react';
import { api } from '@/lib/api';
import type { ScoringEngine, ScoringEngines } from '@/lib/api';

// The engine calibration, fetched once per page and shared by every breakdown
// on it.
//
// Module-level rather than per-component, because a run page can render thirty
// confidence badges and the calibration is one immutable table per deployment.
// The promise is cached, not just the result, so thirty badges mounting in the
// same tick issue one request between them.
let cached: Promise<ScoringEngines> | null = null;

function load(): Promise<ScoringEngines> {
  if (cached == null) {
    cached = api.getScoringEngines();
  }
  return cached;
}

// resetScoringEnginesCache exists for tests, which need each case to start
// from an unfetched state.
export function resetScoringEnginesCache() {
  cached = null;
}

// useScoringEngines returns the calibration, or undefined while it is loading
// or if it could not be fetched.
//
// A failure is deliberately not surfaced as an error state. Everything a
// reader needs about a specific document is stored ON the document; the
// calibration supplies only the line saying which cutoff the score crossed. So
// an API that is older than the agent, or simply unreachable, costs the
// breakdown one line rather than breaking it.
export function useScoringEngines(): ScoringEngines | undefined {
  const [engines, setEngines] = useState<ScoringEngines | undefined>();

  useEffect(() => {
    let cancelled = false;
    load()
      .then((res) => {
        if (!cancelled) setEngines(res);
      })
      .catch(() => {
        // Swallowed on purpose; see above. Clearing the cache lets a later
        // mount retry rather than inheriting one failed fetch forever.
        cached = null;
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return engines;
}

// engineFor picks the calibration that scored a document.
//
// By the document's own stored version, never by `current_version`: that is
// the whole reason the endpoint serves a list. A document scored by engine 1
// and read on a deployment running engine 2 has to explain itself with
// engine 1's cutoffs, or the breakdown shows a reader a boundary the score was
// never measured against.
export function engineFor(
  engines: ScoringEngines | undefined,
  version: number | undefined,
): ScoringEngine | undefined {
  if (engines == null || version == null) return undefined;
  return engines.engines.find((e) => e.version === version);
}
