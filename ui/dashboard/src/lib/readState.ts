'use client';

import { useCallback, useEffect, useRef, useSyncExternalStore } from 'react';
import { api } from '@/lib/api';

type TargetType = 'insight' | 'recommendation';

// Module-level store keyed by `${projectId}:${targetType}`. Multiple list pages
// that mount the same view share a single Set instance and a single in-flight
// fetch — the loader is memoised so we don't fire one request per row.
interface StoreState {
  ids: Set<string>;
  loaded: boolean;
  loading: Promise<void> | null;
}

const store = new Map<string, StoreState>();
const listeners = new Map<string, Set<() => void>>();

function key(projectId: string, targetType: TargetType): string {
  return `${projectId}:${targetType}`;
}

function getOrInit(k: string): StoreState {
  let s = store.get(k);
  if (!s) {
    s = { ids: new Set<string>(), loaded: false, loading: null };
    store.set(k, s);
  }
  return s;
}

function emit(k: string): void {
  const set = listeners.get(k);
  if (!set) return;
  for (const fn of set) fn();
}

function subscribe(k: string, fn: () => void): () => void {
  let set = listeners.get(k);
  if (!set) {
    set = new Set();
    listeners.set(k, set);
  }
  set.add(fn);
  return () => {
    set!.delete(fn);
  };
}

async function ensureLoaded(projectId: string, targetType: TargetType): Promise<void> {
  const k = key(projectId, targetType);
  const s = getOrInit(k);
  if (s.loaded) return;
  if (s.loading) return s.loading;
  s.loading = (async () => {
    try {
      const ids = await api.listReadIDs(projectId, targetType);
      s.ids = new Set(ids || []);
      s.loaded = true;
      emit(k);
    } catch {
      s.loaded = true;
      emit(k);
    } finally {
      s.loading = null;
    }
  })();
  return s.loading;
}

// useReadSet returns a Set of the target_ids the current user has read in this
// project for this target type. The first hook instance fetches; subsequent
// instances share the same set via the module-level store + subscription.
export function useReadSet(projectId: string, targetType: TargetType): Set<string> {
  const k = key(projectId, targetType);
  // Keep the Set reference stable per version for useSyncExternalStore —
  // return a new Set only when the underlying data has changed.
  const snapshotRef = useRef<Set<string>>(getOrInit(k).ids);
  const subscribeHere = useCallback((fn: () => void) => subscribe(k, fn), [k]);
  const getSnapshot = useCallback(() => {
    const s = getOrInit(k);
    if (s.ids !== snapshotRef.current) snapshotRef.current = s.ids;
    return snapshotRef.current;
  }, [k]);
  const set = useSyncExternalStore(subscribeHere, getSnapshot, getSnapshot);

  useEffect(() => {
    ensureLoaded(projectId, targetType);
  }, [projectId, targetType]);

  return set;
}

// markRead tells the server this target has been read, optimistically updates
// the local set, and rolls back on error. Safe to call multiple times — the
// server deduplicates via unique index.
export async function markRead(projectId: string, targetType: TargetType, targetId: string): Promise<void> {
  const k = key(projectId, targetType);
  const s = getOrInit(k);
  if (s.ids.has(targetId)) return;
  // Optimistic: replace the Set reference (so useSyncExternalStore sees a change).
  const next = new Set(s.ids);
  next.add(targetId);
  s.ids = next;
  emit(k);
  try {
    await api.markRead(projectId, targetType, targetId);
  } catch {
    // Rollback — if the server rejected, remove from the local set.
    const rollback = new Set(s.ids);
    rollback.delete(targetId);
    s.ids = rollback;
    emit(k);
  }
}

// markUnread removes the server-side mark and updates the local set.
export async function markUnread(projectId: string, targetType: TargetType, targetId: string): Promise<void> {
  const k = key(projectId, targetType);
  const s = getOrInit(k);
  if (!s.ids.has(targetId)) return;
  const next = new Set(s.ids);
  next.delete(targetId);
  s.ids = next;
  emit(k);
  try {
    await api.markUnread(projectId, targetType, targetId);
  } catch {
    const rollback = new Set(s.ids);
    rollback.add(targetId);
    s.ids = rollback;
    emit(k);
  }
}

// useIsRead is a convenience for a single target — returns true when the target
// is in the read set. Internally subscribes to the same store as useReadSet.
export function useIsRead(projectId: string, targetType: TargetType, targetId: string): boolean {
  const set = useReadSet(projectId, targetType);
  return set.has(targetId);
}

// resetForTest clears the module store — exported only for jest tests.
export function __resetReadStore(): void {
  store.clear();
  listeners.clear();
}
