'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { notifications } from '@mantine/notifications';
import { api, ApiError } from '@/lib/api';
import type {
  InsightValidation,
  ValidationDocKind,
  ValidationJob,
} from '@/lib/api';
import { NewValidationPanel } from './NewValidationPanel';
import { LegacyValidationCard } from './LegacyValidationCard';
import { NoValidationCard } from './NoValidationCard';
import { ValidationJobProgressCard } from './ValidationJobProgressCard';
import { isLegacyValidation } from './validationShape';

// The router is the entry point pages should use whenever they have
// a (discovery, doc, validation, project) tuple. Decision tree:
//
//   isLegacy       → <LegacyValidationCard />
//   isValidated    → <NewValidationPanel />
//   inFlightJob    → <ValidationJobProgressCard />
//   else           → <NoValidationCard validationEnabled={...} />
//
// isValidated is RICHER than "validation != null" — a doc with
// combined ∈ {validation_disabled, skipped_budget_cap} AND no
// verifier/refuter detail was stamped during a disabled run, not
// actually validated. Treating those as "validated" would hide the
// Run button forever once the user re-enables the toggle. Codex
// prod-r2 P0.
//
// The router owns the polling. On mount it asks
// `/validation-jobs?doc_kind=...&doc_id=...` once; if there's a
// non-terminal job it polls every 2s. On any terminal status
// (completed | failed | cancelled) it triggers `onTerminal` so the
// parent can refetch the discovery doc — the agent may have written
// the verdict back even when the process ultimately failed.

const POLL_INTERVAL_MS = 2000;

function isPanelWorthyValidation(v: InsightValidation | undefined): boolean {
  if (!v) return false;
  if (v.combined && v.combined !== 'validation_disabled' && v.combined !== 'skipped_budget_cap') {
    return true;
  }
  // A docs-level skip with no agent detail is a stamp, not a verdict —
  // surface the Run button instead of pretending it's validated.
  if (v.verifier != null || v.refuter != null) {
    return true;
  }
  return false;
}

export function ValidationRouter({
  validation,
  discoveryId,
  docKind,
  docId,
  validationEnabled,
  projectSettingsHref,
  onTerminal,
}: {
  validation: InsightValidation | undefined;
  discoveryId: string;
  docKind: ValidationDocKind;
  docId: string;
  validationEnabled: boolean;
  projectSettingsHref?: string;
  // Called when the in-flight job moves to a terminal status — the
  // parent should refetch the discovery doc so the validation field
  // picks up the new verdict (if the agent wrote one).
  onTerminal?: () => void;
}) {
  const [activeJob, setActiveJob] = useState<ValidationJob | null>(null);
  const [enqueuing, setEnqueuing] = useState(false);
  const pollTimer = useRef<ReturnType<typeof setInterval> | null>(null);
  const lastTerminalRef = useRef<string | null>(null);

  const stopPolling = useCallback(() => {
    if (pollTimer.current != null) {
      clearInterval(pollTimer.current);
      pollTimer.current = null;
    }
  }, []);

  const fetchActiveJob = useCallback(async () => {
    try {
      const jobs = await api.listValidationJobs(discoveryId, docKind, docId);
      // ListByDoc returns most-recent first.
      const top = jobs[0];
      if (!top) {
        setActiveJob(null);
        stopPolling();
        return null;
      }
      const isTerminal = top.status === 'completed' || top.status === 'failed' || top.status === 'cancelled';
      // Drive the terminal-refetch hook exactly once per job id so
      // the discovery isn't refetched on every poll after completion.
      if (isTerminal && lastTerminalRef.current !== top.id) {
        lastTerminalRef.current = top.id;
        onTerminal?.();
        // Successful + completed → don't render the progress card
        // any more; the new validation field will route us to the
        // panel. Failed / cancelled → keep the progress card up so
        // the user can see the error + retry.
        if (top.status === 'completed') {
          setActiveJob(null);
          stopPolling();
          return null;
        }
      }
      setActiveJob(top);
      if (isTerminal) stopPolling();
      return top;
    } catch (err) {
      // Don't spam; one warn per session is plenty.
      console.warn('listValidationJobs failed', err);
      return null;
    }
  }, [discoveryId, docKind, docId, onTerminal, stopPolling]);

  // On mount + on any (discoveryId, docKind, docId) change: probe
  // once and start polling if there's a non-terminal job.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const top = await fetchActiveJob();
      if (cancelled) return;
      if (top && (top.status === 'pending' || top.status === 'running')) {
        pollTimer.current = setInterval(fetchActiveJob, POLL_INTERVAL_MS);
      }
    })();
    return () => {
      cancelled = true;
      stopPolling();
    };
  }, [fetchActiveJob, stopPolling]);

  const enqueue = useCallback(async () => {
    setEnqueuing(true);
    try {
      const enqueueFn = docKind === 'recommendation'
        ? api.enqueueValidateRecommendation
        : api.enqueueValidateInsight;
      const job = await enqueueFn(discoveryId, docId);
      setActiveJob(job);
      lastTerminalRef.current = null;
      // Kick off polling.
      stopPolling();
      pollTimer.current = setInterval(fetchActiveJob, POLL_INTERVAL_MS);
    } catch (err) {
      const message = err instanceof ApiError ? err.message : (err as Error).message;
      notifications.show({ title: 'Could not start validation', message, color: 'red' });
    } finally {
      setEnqueuing(false);
    }
  }, [docKind, discoveryId, docId, fetchActiveJob, stopPolling]);

  const cancel = useCallback(async (jobId: string) => {
    try {
      await api.cancelValidationJob(jobId);
      // Refresh; the worker may take a moment to honour the cancel
      // so the row may stay running for one more poll.
      await fetchActiveJob();
    } catch (err) {
      const message = err instanceof ApiError ? err.message : (err as Error).message;
      notifications.show({ title: 'Cancel failed', message, color: 'red' });
    }
  }, [fetchActiveJob]);

  // Legacy shape — render the pre-plan-v5 card regardless of job
  // state. Legacy docs are read-only with respect to the new
  // pipeline; no manual re-run available.
  if (validation && isLegacyValidation(validation)) {
    return <LegacyValidationCard validation={validation} />;
  }

  // Validated by the agent (verifier/refuter detail present, or
  // combined is a real verdict).
  if (validation && isPanelWorthyValidation(validation)) {
    return <NewValidationPanel validation={validation} />;
  }

  // In-flight job — pending / running / failed / cancelled.
  if (activeJob && activeJob.status !== 'completed') {
    return (
      <ValidationJobProgressCard
        job={activeJob}
        onCancel={cancel}
        onRetry={() => {
          // Failed → clear the progress card, let NoValidationCard
          // surface, user clicks Run validation again.
          setActiveJob(null);
          void enqueue();
        }}
      />
    );
  }

  // Empty state.
  return (
    <NoValidationCard
      validationEnabled={validationEnabled}
      onRun={enqueue}
      running={enqueuing}
      settingsHref={projectSettingsHref}
    />
  );
}
