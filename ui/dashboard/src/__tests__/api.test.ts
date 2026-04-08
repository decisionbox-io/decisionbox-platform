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

// --- Providers ---

describe('api.listLLMProviders', () => {
  it('returns provider metadata', async () => {
    const providers = [
      { id: 'claude', name: 'Claude', description: 'test', config_fields: [{ key: 'api_key', label: 'API Key', required: true, type: 'string' }] },
    ];
    mockSuccess(providers);

    const result = await api.listLLMProviders();
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe('claude');
    expect(result[0].config_fields).toHaveLength(1);
    expect(result[0].config_fields[0].key).toBe('api_key');
  });
});

describe('api.listWarehouseProviders', () => {
  it('returns provider metadata with config fields', async () => {
    const providers = [
      { id: 'bigquery', name: 'BigQuery', description: 'test', config_fields: [
        { key: 'project_id', label: 'GCP Project', required: true, type: 'string' },
        { key: 'dataset', label: 'Dataset', required: true, type: 'string' },
      ]},
    ];
    mockSuccess(providers);

    const result = await api.listWarehouseProviders();
    expect(result[0].config_fields).toHaveLength(2);
    expect(result[0].config_fields[0].key).toBe('project_id');
  });
});

// --- Domain Packs (CRUD) ---

describe('api.listDomainPacks', () => {
  it('returns all domain packs', async () => {
    const packs = [
      { id: 'dp-1', slug: 'gaming', name: 'Gaming', is_published: true, categories: [] },
      { id: 'dp-2', slug: 'ecommerce', name: 'E-Commerce', is_published: true, categories: [] },
    ];
    mockSuccess(packs);

    const result = await api.listDomainPacks();
    expect(result).toHaveLength(2);
    expect(result[0].slug).toBe('gaming');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domain-packs'),
      expect.any(Object)
    );
  });
});

describe('api.getDomainPack', () => {
  it('includes slug in URL', async () => {
    mockSuccess({ id: 'dp-1', slug: 'gaming', name: 'Gaming' });
    const result = await api.getDomainPack('gaming');
    expect(result.slug).toBe('gaming');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domain-packs/gaming'),
      expect.any(Object)
    );
  });

  it('throws on not found', async () => {
    mockError(404, 'domain pack not found: nonexistent');
    await expect(api.getDomainPack('nonexistent')).rejects.toThrow('domain pack not found');
  });
});

describe('api.createDomainPack', () => {
  it('sends POST with pack data', async () => {
    const pack = { slug: 'fintech', name: 'FinTech', is_published: true };
    mockSuccess({ id: 'dp-3', ...pack });

    const result = await api.createDomainPack(pack);
    expect(result.slug).toBe('fintech');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/domain-packs');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toMatchObject({ slug: 'fintech', name: 'FinTech' });
  });

  it('throws on duplicate slug', async () => {
    mockError(409, 'domain pack with slug "gaming" already exists');
    await expect(api.createDomainPack({ slug: 'gaming' })).rejects.toThrow('already exists');
  });
});

describe('api.updateDomainPack', () => {
  it('sends PUT with slug in URL', async () => {
    mockSuccess({ id: 'dp-1', slug: 'gaming', name: 'Updated Gaming' });
    await api.updateDomainPack('gaming', { name: 'Updated Gaming' });

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/domain-packs/gaming');
    expect(opts.method).toBe('PUT');
  });
});

describe('api.deleteDomainPack', () => {
  it('sends DELETE', async () => {
    mockSuccess({ deleted: 'gaming' });
    const result = await api.deleteDomainPack('gaming');
    expect(result.deleted).toBe('gaming');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/domain-packs/gaming');
    expect(opts.method).toBe('DELETE');
  });
});

describe('api.importDomainPack', () => {
  it('sends POST with portable format', async () => {
    const portable = {
      format: 'decisionbox-domain-pack',
      format_version: 1,
      pack: { slug: 'fintech', name: 'FinTech' },
    };
    mockSuccess({ id: 'dp-3', slug: 'fintech', name: 'FinTech' });

    const result = await api.importDomainPack(portable as never);
    expect(result.slug).toBe('fintech');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/domain-packs/import');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toMatchObject({ format: 'decisionbox-domain-pack' });
  });
});

describe('api.exportDomainPack', () => {
  it('returns portable format', async () => {
    const portable = {
      format: 'decisionbox-domain-pack',
      format_version: 1,
      pack: { slug: 'gaming', name: 'Gaming' },
    };
    mockSuccess(portable);

    const result = await api.exportDomainPack('gaming');
    expect(result.format).toBe('decisionbox-domain-pack');
    expect(result.pack.slug).toBe('gaming');

    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/domain-packs/gaming/export'),
      expect.any(Object)
    );
  });
});

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

// --- Prompts ---

describe('api.getPrompts', () => {
  it('includes project id in URL', async () => {
    mockSuccess({ exploration: 'explore prompt', recommendations: 'rec prompt' });
    const result = await api.getPrompts('proj-1');
    expect(result.exploration).toBe('explore prompt');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/projects/proj-1/prompts'),
      expect.any(Object)
    );
  });
});

describe('api.updatePrompts', () => {
  it('sends PUT with prompts data', async () => {
    const prompts = { exploration: 'new prompt', recommendations: 'new rec' };
    mockSuccess(prompts);
    await api.updatePrompts('proj-1', prompts as never);

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/prompts');
    expect(opts.method).toBe('PUT');
    expect(JSON.parse(opts.body)).toMatchObject({ exploration: 'new prompt' });
  });
});

// --- Runs ---

describe('api.getRun', () => {
  it('returns run status', async () => {
    mockSuccess({ id: 'run-1', status: 'running', phase: 'exploration', progress: 42 });
    const result = await api.getRun('run-1');
    expect(result.status).toBe('running');
    expect(result.progress).toBe(42);
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/runs/run-1'),
      expect.any(Object)
    );
  });
});

describe('api.cancelRun', () => {
  it('sends DELETE to run endpoint', async () => {
    mockSuccess({ status: 'cancelled', message: 'run cancelled' });
    const result = await api.cancelRun('run-1');
    expect(result.status).toBe('cancelled');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/runs/run-1');
    expect(opts.method).toBe('DELETE');
  });
});

// --- Feedback ---

describe('api.submitFeedback', () => {
  it('sends POST with feedback data', async () => {
    const fb = { target_type: 'insight', target_id: 'i-1', rating: 'like', comment: 'good' };
    mockSuccess({ id: 'fb-1', ...fb });
    const result = await api.submitFeedback('disc-1', fb);
    expect(result.id).toBe('fb-1');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/discoveries/disc-1/feedback');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toMatchObject({ target_type: 'insight', rating: 'like' });
  });
});

describe('api.listFeedback', () => {
  it('returns feedback for discovery', async () => {
    mockSuccess([{ id: 'fb-1', rating: 'like' }, { id: 'fb-2', rating: 'dislike' }]);
    const result = await api.listFeedback('disc-1');
    expect(result).toHaveLength(2);
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/discoveries/disc-1/feedback'),
      expect.any(Object)
    );
  });
});

describe('api.deleteFeedback', () => {
  it('sends DELETE', async () => {
    mockSuccess({ status: 'deleted' });
    await api.deleteFeedback('fb-1');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/feedback/fb-1');
    expect(opts.method).toBe('DELETE');
  });
});

// --- Cost Estimation ---

describe('api.estimateCost', () => {
  it('sends POST to estimate endpoint', async () => {
    mockSuccess({ total_cost: 1.5, llm_cost: 1.0, warehouse_cost: 0.5 });
    const result = await api.estimateCost('proj-1', { areas: ['churn'], max_steps: 10 });
    expect(result.total_cost).toBe(1.5);

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/discover/estimate');
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toMatchObject({ areas: ['churn'], max_steps: 10 });
  });

  it('sends POST without options', async () => {
    mockSuccess({ total_cost: 2.0 });
    await api.estimateCost('proj-1');

    const [, opts] = mockFetch.mock.calls[0];
    expect(opts.method).toBe('POST');
    expect(opts.body).toBeUndefined();
  });
});

// --- Pricing ---

describe('api.getPricing', () => {
  it('returns pricing data', async () => {
    mockSuccess({ llm: { claude: {} }, warehouse: { bigquery: {} } });
    const result = await api.getPricing();
    expect(result.llm).toBeDefined();
    expect(result.warehouse).toBeDefined();
  });
});

describe('api.updatePricing', () => {
  it('sends PUT with pricing', async () => {
    const pricing = { llm: { claude: {} }, warehouse: { bigquery: {} } };
    mockSuccess(pricing);
    await api.updatePricing(pricing as never);

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/pricing');
    expect(opts.method).toBe('PUT');
  });
});

// --- Secrets ---

describe('api.setSecret', () => {
  it('sends PUT with value', async () => {
    mockSuccess({ key: 'api_key', masked: 'sk-***', status: 'created' });
    const result = await api.setSecret('proj-1', 'api_key', 'sk-secret');
    expect(result.key).toBe('api_key');
    expect(result.masked).toBe('sk-***');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/secrets/api_key');
    expect(opts.method).toBe('PUT');
    expect(JSON.parse(opts.body)).toMatchObject({ value: 'sk-secret' });
  });
});

describe('api.listSecrets', () => {
  it('returns secret entries', async () => {
    mockSuccess([{ key: 'api_key', masked: 'sk-***', updated_at: '2026-03-20' }]);
    const result = await api.listSecrets('proj-1');
    expect(result).toHaveLength(1);
    expect(result[0].key).toBe('api_key');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/projects/proj-1/secrets'),
      expect.any(Object)
    );
  });
});

// --- Connection Testing ---

describe('api.testWarehouse', () => {
  it('sends POST', async () => {
    mockSuccess({ success: true, provider: 'bigquery', datasets: ['analytics'] });
    const result = await api.testWarehouse('proj-1');
    expect(result.success).toBe(true);
    expect(result.provider).toBe('bigquery');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/test/warehouse');
    expect(opts.method).toBe('POST');
  });
});

describe('api.testLLM', () => {
  it('sends POST', async () => {
    mockSuccess({ success: true, provider: 'claude', model: 'claude-sonnet-4-20250514' });
    const result = await api.testLLM('proj-1');
    expect(result.success).toBe(true);
    expect(result.model).toBe('claude-sonnet-4-20250514');

    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain('/api/v1/projects/proj-1/test/llm');
    expect(opts.method).toBe('POST');
  });
});

// --- Discovery by ID ---

describe('api.getDiscoveryById', () => {
  it('includes discovery id in URL', async () => {
    mockSuccess({ id: 'disc-abc', total_steps: 30 });
    const result = await api.getDiscoveryById('disc-abc');
    expect(result.id).toBe('disc-abc');
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('/api/v1/discoveries/disc-abc'),
      expect.any(Object)
    );
  });
});

// --- Trigger Discovery with options ---

describe('api.triggerDiscovery with options', () => {
  it('sends areas and max_steps in body', async () => {
    mockSuccess({ status: 'accepted', run_id: 'run-1' });
    const result = await api.triggerDiscovery('proj-1', { areas: ['churn', 'retention'], max_steps: 20 });
    expect(result.run_id).toBe('run-1');

    const [, opts] = mockFetch.mock.calls[0];
    const body = JSON.parse(opts.body);
    expect(body.areas).toEqual(['churn', 'retention']);
    expect(body.max_steps).toBe(20);
  });

  it('sends no body when no options', async () => {
    mockSuccess({ status: 'accepted' });
    await api.triggerDiscovery('proj-1');

    const [, opts] = mockFetch.mock.calls[0];
    expect(opts.body).toBeUndefined();
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
