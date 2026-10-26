'use client';

/**
 * The project-scoped layout, which exists for one reason: to send a project
 * that has not been configured for per-layer LLM models to its settings page
 * before anything else in it can be used (#407).
 *
 * The gate itself lives in the API — StartRun refuses, and so does every
 * layer resolver — so this is not the enforcement. It is the difference
 * between a user discovering the requirement by clicking "Run discovery" and
 * getting a 409, and being taken to the one page that fixes it with the
 * values already filled in.
 *
 * There is no project-scoped layout in either build today, which is why this
 * is the right place for it: the enterprise dashboard overlays individual
 * files and has no copy of this one, so both builds inherit one
 * implementation rather than two that can drift.
 *
 * Two exclusions, both load-bearing:
 *   - the settings route itself, or the redirect loops;
 *   - managed deployments, which hide the whole AI configuration surface, so
 *     there is nothing for a user to define and nowhere to send them. The API
 *     reports llm_config_required=false there, and this checks the flag again
 *     rather than inferring from the absence of data.
 */

import { useEffect, useState } from 'react';
import { useParams, usePathname, useRouter } from 'next/navigation';
import { Alert, Center, Loader } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import { api } from '@/lib/api';

/** The settings path a gated project is sent to, anchored on the Models tab. */
const SETTINGS_ANCHOR = '#models';

export default function ProjectLayout({ children }: { children: React.ReactNode }) {
  const params = useParams();
  const pathname = usePathname();
  const router = useRouter();
  const projectId = typeof params?.id === 'string' ? params.id : '';
  const onSettings = pathname?.includes('/settings') ?? false;

  // The settings page itself is never gated — gating it would loop — and
  // neither is a route with no project id, so those need no check at all.
  const needsCheck = Boolean(projectId) && !onSettings;
  const [result, setResult] = useState<'pending' | 'open' | 'redirecting'>('pending');

  useEffect(() => {
    if (!needsCheck) return;
    let cancelled = false;
    api.getProject(projectId)
      .then((project) => {
        if (cancelled) return;
        if (project.llm_config_required) {
          setResult('redirecting');
          router.replace(`/projects/${projectId}/settings${SETTINGS_ANCHOR}`);
          return;
        }
        setResult('open');
      })
      // A failed check must not lock a project out. The API gate is the
      // enforcement; this is navigation, and navigation that breaks on a
      // transient error is worse than navigation that lets the user through
      // to a page that will tell them what is wrong.
      .catch(() => {
        if (!cancelled) setResult('open');
      });
    return () => { cancelled = true; };
  }, [needsCheck, projectId, router]);

  const checked = !needsCheck || result !== 'pending';
  // Gated on needsCheck, not just on the state: this layout WRAPS the settings
  // page it redirects to, and Next.js preserves it across that navigation. The
  // state stays 'redirecting' there, so reading it alone kept rendering the
  // alert in place of the settings form — the one page the user was sent to
  // was the one page they could not reach without a reload.
  const redirecting = needsCheck && result === 'redirecting';

  if (redirecting) {
    return (
      <Center style={{ minHeight: '60vh' }}>
        <Alert
          color="blue"
          icon={<IconAlertCircle size={16} />}
          title="Choose this project's models"
          maw={520}
        >
          This project needs a provider and a model for each layer before it can
          run. The values from its current setup are already filled in — review
          them and save.
        </Alert>
      </Center>
    );
  }

  if (!checked) {
    return (
      <Center style={{ minHeight: '40vh' }}>
        <Loader />
      </Center>
    );
  }

  return <>{children}</>;
}
