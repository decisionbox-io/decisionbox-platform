/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import {
  SystemInventory,
  groupByKind,
  withDashboardSelf,
} from '@/components/system/SystemInventory';
import type { SystemComponent } from '@/lib/api';

function wrap(ui: React.ReactElement) {
  return render(<MantineProvider>{ui}</MantineProvider>);
}

const dashboardSelf: SystemComponent = {
  name: 'Dashboard',
  kind: 'service',
  version: '0.9.9',
};

describe('withDashboardSelf', () => {
  it('prepends the dashboard self entry when the server omits it', () => {
    const server: SystemComponent[] = [{ name: 'API', kind: 'service', version: '1.0.0' }];
    const out = withDashboardSelf(server, dashboardSelf);
    expect(out).toHaveLength(2);
    expect(out[0].name).toBe('Dashboard');
    expect(out[0].version).toBe('0.9.9');
  });

  it('does not duplicate when the server already reports a Dashboard', () => {
    const server: SystemComponent[] = [
      { name: 'Dashboard', kind: 'service', version: 'from-server' },
      { name: 'API', kind: 'service', version: '1.0.0' },
    ];
    const out = withDashboardSelf(server, dashboardSelf);
    expect(out).toHaveLength(2);
    expect(out.filter((c) => c.name === 'Dashboard')).toHaveLength(1);
    // Server's entry wins.
    expect(out.find((c) => c.name === 'Dashboard')?.version).toBe('from-server');
  });
});

describe('groupByKind', () => {
  it('orders services before workers and unknown kinds last, names sorted', () => {
    const components: SystemComponent[] = [
      { name: 'Validation jobs', kind: 'worker' },
      { name: 'Mystery', kind: 'plugin' },
      { name: 'Schema indexing', kind: 'worker' },
      { name: 'API', kind: 'service' },
      { name: 'Dashboard', kind: 'service' },
    ];
    const groups = groupByKind(components);
    expect(groups.map((g) => g.kind)).toEqual(['service', 'worker', 'plugin']);
    expect(groups[0].label).toBe('Services');
    expect(groups[1].label).toBe('Workers');
    // Unknown kind gets a capitalised fallback label.
    expect(groups[2].label).toBe('Plugin');
    expect(groups[0].items.map((c) => c.name)).toEqual(['API', 'Dashboard']);
    expect(groups[1].items.map((c) => c.name)).toEqual(['Schema indexing', 'Validation jobs']);
  });
});

describe('SystemInventory', () => {
  it('renders each component with version, kind and worker note', () => {
    const components: SystemComponent[] = [
      { name: 'API', kind: 'service', version: '1.2.3', commit: 'abc1234' },
      {
        name: 'Schema indexing',
        kind: 'worker',
        runs_in: 'API',
        version: '1.2.3',
        note: 'runs in-process inside the API service; shares its image version',
      },
    ];
    wrap(<SystemInventory components={components} />);

    // "API" appears both as a card title and as the worker's "Runs in" value.
    expect(screen.getAllByText('API').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('Schema indexing')).toBeInTheDocument();
    expect(screen.getByText('abc1234')).toBeInTheDocument();
    expect(screen.getByText(/shares its image version/)).toBeInTheDocument();
    // Section headers render.
    expect(screen.getByText('Services')).toBeInTheDocument();
    expect(screen.getByText('Workers')).toBeInTheDocument();
  });

  it('renders an empty-state message with no components', () => {
    wrap(<SystemInventory components={[]} />);
    expect(screen.getByText(/No components reported/)).toBeInTheDocument();
  });
});
