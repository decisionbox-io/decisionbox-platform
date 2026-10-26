/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import ProjectLayout from '@/app/projects/[id]/layout';

// The project-scoped guard: a project that has not been configured for
// per-layer models is sent to its settings page before anything else in it
// can be used. The API gate is the enforcement; this is the difference
// between finding out by clicking "Run discovery" and being taken to the one
// page that fixes it.

const replace = jest.fn();
let pathname = '/projects/proj-1';

jest.mock('next/navigation', () => ({
  useParams: () => ({ id: 'proj-1' }),
  usePathname: () => pathname,
  useRouter: () => ({ replace, push: jest.fn() }),
}));
jest.mock('@/lib/api', () => ({ api: { getProject: jest.fn() } }));

import { api } from '@/lib/api';

function renderLayout() {
  return render(
    <MantineProvider>
      <ProjectLayout><div>project page</div></ProjectLayout>
    </MantineProvider>,
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  pathname = '/projects/proj-1';
});

describe('project layout — per-layer LLM configuration gate', () => {
  it('lets a configured project through', async () => {
    (api.getProject as jest.Mock).mockResolvedValue({ id: 'proj-1', llm_config_required: false });
    renderLayout();
    expect(await screen.findByText('project page')).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
  });

  it('sends an unconfigured project to the models tab', async () => {
    (api.getProject as jest.Mock).mockResolvedValue({ id: 'proj-1', llm_config_required: true });
    renderLayout();
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/projects/proj-1/settings#models'));
    // And it explains why rather than silently bouncing.
    expect(await screen.findByText(/needs a provider and a model for each layer/i)).toBeInTheDocument();
    expect(screen.queryByText('project page')).not.toBeInTheDocument();
  });

  // The exclusion that stops the redirect looping.
  it('never gates the settings route itself', async () => {
    pathname = '/projects/proj-1/settings';
    (api.getProject as jest.Mock).mockResolvedValue({ id: 'proj-1', llm_config_required: true });
    renderLayout();
    expect(await screen.findByText('project page')).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
    // It does not even ask: the settings page is where the fix happens.
    expect(api.getProject).not.toHaveBeenCalled();
  });

  // A managed deployment hides the whole AI configuration surface, so the API
  // reports the flag false and there is nowhere to send anyone.
  it('does not gate when the API says no configuration is required', async () => {
    (api.getProject as jest.Mock).mockResolvedValue({ id: 'proj-1' });
    renderLayout();
    expect(await screen.findByText('project page')).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
  });

  // The other half of the redirect, and the one that matters: this layout
  // WRAPS the settings page it sends people to, and Next.js keeps the layout
  // mounted across that navigation. If the alert is rendered from the
  // redirect state alone, the user lands on settings and sees the alert
  // instead of the form — the page they were sent to is the page they cannot
  // reach.
  it('shows the settings page once the redirect lands there', async () => {
    (api.getProject as jest.Mock).mockResolvedValue({ id: 'proj-1', llm_config_required: true });
    const { rerender } = renderLayout();
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/projects/proj-1/settings#models'));
    expect(await screen.findByText(/needs a provider and a model for each layer/i)).toBeInTheDocument();

    // The navigation the router just performed, with the layout and its state
    // preserved — which is what Next.js does.
    pathname = '/projects/proj-1/settings#models';
    rerender(
      <MantineProvider>
        <ProjectLayout><div>project page</div></ProjectLayout>
      </MantineProvider>,
    );

    expect(await screen.findByText('project page')).toBeInTheDocument();
    expect(screen.queryByText(/needs a provider and a model for each layer/i)).not.toBeInTheDocument();
  });

  // Navigation that breaks on a transient error is worse than navigation that
  // lets the user reach a page which will tell them what is wrong.
  it('lets the user through when the check itself fails', async () => {
    (api.getProject as jest.Mock).mockRejectedValue(new Error('network'));
    renderLayout();
    expect(await screen.findByText('project page')).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
  });
});
