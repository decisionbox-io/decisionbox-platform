/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import PackGenStatusPanel from '@/components/projects/PackGenStatusPanel';
import {
  api,
  Project,
  SchemaIndexStatus,
  WarehouseConfig,
  PROJECT_STATE_PACK_GENERATION_PENDING,
} from '@/lib/api';

// next/navigation router.push lives behind a hook; jest can't traverse
// it from the component without a stub. PackGenStatusPanel only uses
// router.push() for the wizard-resume button, so a no-op is fine.
jest.mock('next/navigation', () => ({
  useRouter: () => ({ push: jest.fn() }),
}));

jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: jest.fn(),
      getSchemaIndexStatus: jest.fn(),
      retrySchemaIndex: jest.fn(),
      reindexSchema: jest.fn(),
      cancelSchemaIndex: jest.fn(),
      listSchemaIndexLogs: jest.fn(),
      getDomainPack: jest.fn(),
    },
  };
});

const mockedApi = api as jest.Mocked<typeof api>;

// pendingProject without warehouse mirrors the very-fresh-draft state:
// project just created, operator hasn't reached the warehouse step yet,
// schema-index status would be empty on the server, and the
// SchemaIndexPanel must NOT mount on the home page (clicking its
// "Build schema index" action against a missing warehouse would land
// the run in a confusing failed state — see PR #228 Codex review).
function pendingProject(): Project {
  return {
    id: 'p1',
    name: 'Demo',
    state: PROJECT_STATE_PACK_GENERATION_PENDING,
  } as Project;
}

// Minimal but complete WarehouseConfig — every required field
// stamped so strict TypeScript accepts the fixture without an
// `as unknown` cast. The fields we don't exercise in this test
// suite (`project_id`, `location`, `filter_field`, `filter_value`)
// are still required by the type contract.
function warehouseFixture(overrides: Partial<WarehouseConfig> = {}): WarehouseConfig {
  return {
    provider: 'bigquery',
    project_id: 'demo-proj',
    datasets: ['analytics'],
    location: 'US',
    filter_field: '',
    filter_value: '',
    ...overrides,
  };
}

function pendingProjectWithWarehouse(): Project {
  // A fully-configured warehouse needs both a provider AND at
  // least one dataset — the agent's index-schema mode rejects
  // with "no datasets configured in project" without datasets.
  // Test fixtures mirror the gate.
  const p = pendingProject();
  (p as Project).warehouse = warehouseFixture();
  return p;
}

function pendingProjectWithProviderOnly(): Project {
  // Half-configured warehouse: provider set, datasets empty. The
  // wizard treats this as incomplete; the home page must NOT
  // mount SchemaIndexPanel in this state — otherwise the panel's
  // Build action would POST a reindex against a warehouse that
  // doesn't know what to index.
  const p = pendingProject();
  (p as Project).warehouse = warehouseFixture({ datasets: [] });
  return p;
}

// Canonical SchemaIndexStatus shape: status + progress.{phase,
// tables_total, tables_done}. Tests assemble responses through this
// builder so a future field rename surfaces in one place and the
// suite catches drift from the real wire contract — see PR #228
// Copilot comment on the original `progress_pct` mock.
function statusResponse(
  status: string,
  progress?: { phase?: string; tables_total?: number; tables_done?: number },
  extra: Partial<{ updated_at: string; error: string }> = {},
): SchemaIndexStatus {
  // `updated_at` and `error` are optional on the wire — only
  // included when the caller supplies them, so the fixture
  // matches the real `SchemaIndexStatus` type (which declares
  // both as optional `string`, NOT `string | null`).
  const out: SchemaIndexStatus = { status };
  if (extra.updated_at !== undefined) out.updated_at = extra.updated_at;
  if (extra.error !== undefined) out.error = extra.error;
  if (progress !== undefined) {
    out.progress = {
      phase: progress.phase ?? 'embedding',
      tables_total: progress.tables_total ?? 100,
      tables_done: progress.tables_done ?? 42,
    };
  }
  return out;
}

function mount(p: Project = pendingProject()) {
  return render(
    <MantineProvider>
      <Notifications />
      <PackGenStatusPanel project={p} onProjectChanged={() => {}} />
    </MantineProvider>
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  window.localStorage.clear();
  (mockedApi.listSchemaIndexLogs as jest.Mock).mockResolvedValue([]);
  // getProject is only called by the panel's own polling timer; the
  // tests don't advance fake timers, so a single resolved value keeps
  // the polling stable without affecting assertions.
  (mockedApi.getProject as jest.Mock).mockResolvedValue(pendingProject());
  // SchemaIndexPanel calls getSchemaIndexStatus on mount; default to
  // never-started so the panel-mount-gate tests are deterministic.
  (mockedApi.getSchemaIndexStatus as jest.Mock).mockResolvedValue(statusResponse(''));
});

describe('PackGenStatusPanel — pack_generation_pending', () => {
  it('shows the "Pick up where you left off" copy when the project has no warehouse and no schema-index has started', async () => {
    // pendingProject() has no warehouse → SchemaIndexPanel is gated
    // out → home page is purely the wizard-resume prompt. This is
    // the very-fresh-draft state.
    mount();
    await waitFor(() =>
      expect(screen.getByText(/Pick up where you left off/i)).toBeInTheDocument()
    );
    expect(screen.getByRole('button', { name: /Continue setup/i })).toBeInTheDocument();
    // Panel must NOT be mounted before warehouse is configured —
    // otherwise its "Build schema index" action would call
    // reindexSchema against an unconfigured warehouse. Absence of
    // the SchemaIndexPanel's banner ("Schema index:") is the proof.
    expect(screen.queryByText(/Schema index:/i)).not.toBeInTheDocument();
  });

  it('mounts SchemaIndexPanel once a warehouse is configured (Build action available pre-index)', async () => {
    mount(pendingProjectWithWarehouse());
    // SchemaIndexPanel renders its empty-status banner ("Schema
    // index: Not indexed") so the operator can start indexing from
    // the home page without re-entering the wizard.
    await waitFor(() =>
      expect(screen.getByText(/Schema index:/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/Pick up where you left off/i)).toBeInTheDocument();
  });

  it('does NOT mount SchemaIndexPanel when warehouse has a provider but no datasets', async () => {
    // Half-configured warehouse is treated as incomplete by the
    // wizard (`agent --mode index-schema` rejects with "no datasets
    // configured in project"). Mounting the panel here would
    // expose its Build action and enqueue a doomed indexing run
    // from the home page. The gate requires BOTH provider AND at
    // least one dataset before the panel renders.
    mount(pendingProjectWithProviderOnly());
    await waitFor(() =>
      expect(screen.getByText(/Pick up where you left off/i)).toBeInTheDocument()
    );
    expect(screen.queryByText(/Schema index:/i)).not.toBeInTheDocument();
  });

  it('replaces the wizard copy with an "indexing is running" message when indexStatus is indexing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue(
      statusResponse('indexing', { phase: 'embedding', tables_done: 42, tables_total: 100 }),
    );
    mount(pendingProjectWithWarehouse());
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing is running/i)).toBeInTheDocument()
    );
    // Badge flips from gray "Pending" to blue "Indexing schema" so the
    // operator can see the new state at a glance.
    expect(screen.getByText(/Indexing schema/i)).toBeInTheDocument();
    // The original "Pick up where you left off" sentence is gone — the
    // home page is now a progress surface, not a wizard prompt.
    expect(screen.queryByText(/Pick up where you left off/i)).not.toBeInTheDocument();
  });

  it('shows the "indexing pending" message when indexStatus is pending_indexing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue(statusResponse('pending_indexing'));
    mount(pendingProjectWithWarehouse());
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing is running/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/Indexing schema/i)).toBeInTheDocument();
  });

  it('signals step 1 of 2 done and points at the wizard\'s Launch button when indexStatus is ready', async () => {
    // The previous copy ("Schema index is ready. Continue in the
    // wizard…" + Continue setup button) was too optimistic — at-a-
    // glance, operators thought the pack itself was generated and
    // got stuck waiting for a "Start discovery" button that never
    // appears in this state. The new copy spells out that pack
    // synthesis is a separate step that still has to be launched
    // from the wizard's final step.
    mockedApi.getSchemaIndexStatus.mockResolvedValue(
      statusResponse('ready', undefined, { updated_at: '2026-05-22T10:00:00Z' }),
    );
    mount(pendingProjectWithWarehouse());
    // Step disambiguator in the badge + the helper copy.
    await waitFor(() => expect(screen.getAllByText(/step 1 of 2/i).length).toBeGreaterThan(0));
    // Badge shows the green "Schema indexed — step 1 of 2".
    expect(screen.getByText(/Schema indexed/i)).toBeInTheDocument();
    // Helper copy must name pack synthesis as the still-pending
    // next step so the operator knows another click is required.
    expect(screen.getByText(/pack synthesis/i)).toBeInTheDocument();
    // Button verb names the action the next click triggers (the
    // wizard opens, the operator clicks Generate pack there).
    expect(screen.getByRole('button', { name: /Open wizard to launch pack/i })).toBeInTheDocument();
  });

  it('shows a recovery message + retry button label when indexStatus is failed', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue(
      statusResponse('failed', undefined, {
        error: 'blurb: too many per-table blurb failures',
      }),
    );
    mount(pendingProjectWithWarehouse());
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing did not finish/i)).toBeInTheDocument()
    );
    // Title is preserved; badge flips to "Indexing failed".
    expect(screen.getAllByText(/Indexing failed/i).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: /Open wizard to retry/i })).toBeInTheDocument();
  });

  it('shows a recovery message when indexStatus is cancelled', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue(statusResponse('cancelled'));
    mount(pendingProjectWithWarehouse());
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing was cancelled/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/Indexing cancelled/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Open wizard to retry/i })).toBeInTheDocument();
  });

  it('shows a "Re-index required" badge when indexStatus is needs_reindex (operator cleared the cache)', async () => {
    // After clicking Settings → Advanced → Clear schema cache, the
    // server flips the status to needs_reindex. The home page must
    // surface that as an actionable orange badge — without this
    // branch the home page rendered the default gray "Pending"
    // label and the operator wouldn't know a re-index was needed
    // before pack synthesis can proceed.
    mockedApi.getSchemaIndexStatus.mockResolvedValue(statusResponse('needs_reindex'));
    mount(pendingProjectWithWarehouse());
    await waitFor(() =>
      expect(screen.getByText(/Schema cache was cleared/i)).toBeInTheDocument()
    );
    expect(screen.getAllByText(/Re-index required/i).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: /Open wizard to retry/i })).toBeInTheDocument();
  });

  it('keeps the existing pack_gen_last_error banner when the previous pack synthesis attempt failed', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue(statusResponse('ready'));
    const proj = pendingProjectWithWarehouse();
    (proj as Project).pack_gen_last_error = 'invalid pack JSON: expected object';
    mount(proj);
    await waitFor(() =>
      expect(screen.getByText(/Pack generation failed/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/invalid pack JSON/i)).toBeInTheDocument();
    // Even with schema index ready, a prior pack-gen failure dominates
    // the badge so the operator notices the higher-priority signal.
    expect(screen.getAllByText(/Last attempt failed/i).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: /Retry in wizard/i })).toBeInTheDocument();
  });
});
