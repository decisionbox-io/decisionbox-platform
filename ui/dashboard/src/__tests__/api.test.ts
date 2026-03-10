import { api } from '@/lib/api';

// Mock fetch globally
const mockFetch = jest.fn();
global.fetch = mockFetch;

beforeEach(() => {
  mockFetch.mockClear();
});

function mockSuccess(data: unknown) {
  mockFetch.mockResolvedValueOnce({
    ok: true,
    json: async () => ({ data }),
  });
}

function mockError(status: number, error: string) {
  mockFetch.mockResolvedValueOnce({
    ok: false,
    status,
    json: async () => ({ error }),
  });
}

// --- Domains ---

describe('api.listDomains', () => {
  it('returns domains on success', async () => {
    const domains = [{ id: 'gaming', categories: [{ id: 'match3', name: 'Match-3', description: '' }] }];
    mockSuccess(domains);

    const result = await api.listDomains();
    expect(result).toEqual(domains);
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domains'),
      expect.any(Object)
    );
  });

  it('throws on API error', async () => {
    mockError(500, 'internal error');
    await expect(api.listDomains()).rejects.toThrow('internal error');
  });
});

describe('api.listCategories', () => {
  it('includes domain in URL', async () => {
    mockSuccess([]);
    await api.listCategories('gaming');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domains/gaming/categories'),
      expect.any(Object)
    );
  });
});

describe('api.getProfileSchema', () => {
  it('includes domain and category in URL', async () => {
    mockSuccess({ properties: {} });
    await api.getProfileSchema('gaming', 'match3');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domains/gaming/categories/match3/schema'),
      expect.any(Object)
    );
  });
});

describe('api.getAnalysisAreas', () => {
  it('returns analysis areas', async () => {
    const areas = [{ id: 'churn', name: 'Churn', description: '', keywords: [], is_base: true, priority: 1 }];
    mockSuccess(areas);

    const result = await api.getAnalysisAreas('gaming', 'match3');
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe('churn');
  });
});

// --- Projects ---

describe('api.createProject', () => {
  it('sends POST with project data', async () => {
    const project = { id: '123', name: 'Test', domain: 'gaming', category: 'match3' };
    mockSuccess(project);

    const result = await api.createProject({ name: 'Test', domain: 'gaming', category: 'match3' });
    expect(result.id).toBe('123');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toMatchObject({ name: 'Test', domain: 'gaming' });
  });

  it('throws on validation error', async () => {
    mockError(400, 'name is required');
    await expect(api.createProject({})).rejects.toThrow('name is required');
  });
});

describe('api.listProjects', () => {
  it('returns project list', async () => {
    mockSuccess([{ id: '1', name: 'P1' }, { id: '2', name: 'P2' }]);
    const result = await api.listProjects();
    expect(result).toHaveLength(2);
  });

  it('returns empty array', async () => {
    mockSuccess([]);
    const result = await api.listProjects();
    expect(result).toEqual([]);
  });
});

describe('api.getProject', () => {
  it('includes id in URL', async () => {
    mockSuccess({ id: 'abc', name: 'Test' });
    await api.getProject('abc');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/projects/abc'),
      expect.any(Object)
    );
  });

  it('throws on not found', async () => {
    mockError(404, 'project not found');
    await expect(api.getProject('nonexistent')).rejects.toThrow('project not found');
  });
});

describe('api.updateProject', () => {
  it('sends PUT', async () => {
    mockSuccess({ id: 'abc', name: 'Updated' });
    await api.updateProject('abc', { name: 'Updated' });

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/abc');
    expect(opts.method).toBe('PUT');
  });
});

describe('api.deleteProject', () => {
  it('sends DELETE', async () => {
    mockSuccess({ deleted: 'abc' });
    const result = await api.deleteProject('abc');
    expect(result.deleted).toBe('abc');

    const [, opts] = mockFetch.mock.calls[0];
    expect(opts.method).toBe('DELETE');
  });
});

// --- Discovery ---

describe('api.triggerDiscovery', () => {
  it('sends POST', async () => {
    mockSuccess({ status: 'accepted', message: 'queued' });
    const result = await api.triggerDiscovery('proj-1');
    expect(result.status).toBe('accepted');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/discover');
    expect(opts.method).toBe('POST');
  });
});

describe('api.listDiscoveries', () => {
  it('returns discoveries for project', async () => {
    mockSuccess([{ id: 'd1', total_steps: 50 }]);
    const result = await api.listDiscoveries('proj-1');
    expect(result).toHaveLength(1);
  });
});

describe('api.getLatestDiscovery', () => {
  it('returns latest discovery', async () => {
    mockSuccess({ id: 'd1', total_steps: 42, insights: [] });
    const result = await api.getLatestDiscovery('proj-1');
    expect(result.total_steps).toBe(42);
  });

  it('throws when no discoveries', async () => {
    mockError(404, 'no discoveries found');
    await expect(api.getLatestDiscovery('proj-1')).rejects.toThrow('no discoveries found');
  });
});

describe('api.getDiscoveryByDate', () => {
  it('includes date in URL', async () => {
    mockSuccess({ id: 'd1' });
    await api.getDiscoveryByDate('proj-1', '2026-03-10');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/projects/proj-1/discoveries/2026-03-10'),
      expect.any(Object)
    );
  });
});

describe('api.getProjectStatus', () => {
  it('returns status', async () => {
    mockSuccess({ project_id: 'proj-1', status: 'active', last_run_at: null });
    const result = await api.getProjectStatus('proj-1');
    expect(result.status).toBe('active');
  });
});

// --- Error Handling ---

describe('error handling', () => {
  it('throws with error message from API', async () => {
    mockError(500, 'database connection failed');
    await expect(api.listProjects()).rejects.toThrow('database connection failed');
  });

  it('throws with status code when no error message', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 503,
      json: async () => ({}),
    });
    await expect(api.listProjects()).rejects.toThrow('API error: 503');
  });

  it('handles network failure with helpful message', async () => {
    mockFetch.mockRejectedValueOnce(new Error('fetch failed'));
    await expect(api.listProjects()).rejects.toThrow('Cannot connect to DecisionBox API');
  });
});
