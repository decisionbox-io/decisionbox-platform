import { ApiError, askErrorMessage, api } from '@/lib/api';

const mockFetch = jest.fn();
global.fetch = mockFetch;

beforeEach(() => {
  mockFetch.mockClear();
});

// --- ApiError parsing ----------------------------------------------

describe('request() ApiError parsing', () => {
  it('attaches code + details from typed error body', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 412,
      json: async () => ({
        error: 'embedding provider not configured for this project',
        code: 'embedding_not_configured',
        details: 'project has no Embedding.Provider set',
      }),
    });
    let captured: unknown = null;
    try {
      await api.askInsights('proj-1', { question: 'q' });
    } catch (err) {
      captured = err;
    }
    expect(captured).toBeInstanceOf(ApiError);
    const e = captured as ApiError;
    expect(e.status).toBe(412);
    expect(e.code).toBe('embedding_not_configured');
    expect(e.details).toContain('Embedding.Provider');
    expect(e.message).toContain('embedding provider not configured');
  });

  it('keeps code undefined when the backend body has no code field (legacy 4xx)', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 400,
      json: async () => ({ error: 'invalid request body' }),
    });
    try {
      await api.askInsights('proj-1', { question: 'q' });
      fail('expected throw');
    } catch (err) {
      const e = err as ApiError;
      expect(e).toBeInstanceOf(ApiError);
      expect(e.status).toBe(400);
      expect(e.code).toBeUndefined();
      expect(e.message).toBe('invalid request body');
    }
  });

  it('survives non-JSON bodies on 5xx with a status-only ApiError', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      status: 502,
      json: async () => {
        throw new SyntaxError('unexpected token');
      },
    });
    try {
      await api.askInsights('proj-1', { question: 'q' });
      fail('expected throw');
    } catch (err) {
      const e = err as ApiError;
      expect(e).toBeInstanceOf(ApiError);
      expect(e.status).toBe(502);
      expect(e.message).toContain('502');
    }
  });

  it('rejects 2xx responses whose body is not valid JSON with a typed ApiError', async () => {
    // Misconfigured proxy returning HTML 200, or a server that
    // forgot to set Content-Type to JSON. Without explicit handling,
    // request() previously returned `undefined` silently. The
    // caller's `result.field` deref then produced a mystery
    // TypeError far away from the actual failure.
    mockFetch.mockResolvedValueOnce({
      ok: true,
      status: 200,
      json: async () => {
        throw new SyntaxError('Unexpected token < in JSON');
      },
    });
    try {
      await api.askInsights('proj-1', { question: 'q' });
      fail('expected throw');
    } catch (err) {
      const e = err as ApiError;
      expect(e).toBeInstanceOf(ApiError);
      expect(e.status).toBe(200);
      expect(e.message).toMatch(/non-JSON|200/);
    }
  });

  it('throws ApiError(status=0) on network failure', async () => {
    mockFetch.mockRejectedValueOnce(new TypeError('network down'));
    try {
      await api.askInsights('proj-1', { question: 'q' });
      fail('expected throw');
    } catch (err) {
      const e = err as ApiError;
      expect(e).toBeInstanceOf(ApiError);
      expect(e.status).toBe(0);
      expect(e.message).toContain('Cannot connect');
    }
  });
});

// --- askErrorMessage mapping --------------------------------------

describe('askErrorMessage', () => {
  it('returns a generic line for non-ApiError throwables', () => {
    expect(askErrorMessage(new Error('boom'))).toContain('could not answer');
    expect(askErrorMessage('string-thrown')).toContain('could not answer');
    expect(askErrorMessage(undefined)).toContain('could not answer');
  });

  it('maps embedding_not_configured to project-settings copy', () => {
    const e = new ApiError('embedding provider not configured', 412, 'embedding_not_configured');
    const msg = askErrorMessage(e);
    expect(msg).toContain('no embedding provider');
    expect(msg).toContain('Embedding');
  });

  it('maps llm_not_configured to project-settings copy', () => {
    const e = new ApiError('LLM provider not configured', 412, 'llm_not_configured');
    const msg = askErrorMessage(e);
    expect(msg).toContain('no LLM provider');
    expect(msg).toContain('LLM');
  });

  it('maps context_overflow to "start a new chat / wider model" copy', () => {
    const e = new ApiError('context window exceeded', 413, 'context_overflow');
    const msg = askErrorMessage(e);
    expect(msg).toContain("context window");
    expect(msg).toMatch(/new chat|wider/i);
  });

  it('maps llm_upstream to provider-side copy WITHOUT inlining details', () => {
    // The ApiError doc says `details` belongs in a secondary expander,
    // not in the primary user-facing sentence. Inlining it would leak
    // raw upstream text into the headline copy and defeat the
    // consistent-copy contract.
    const e = new ApiError('rate limited', 502, 'llm_upstream', 'rate_limit_error — slow down');
    const msg = askErrorMessage(e);
    expect(msg).toContain('LLM provider rejected');
    expect(msg).not.toContain('rate_limit_error');
    expect(msg).not.toContain('slow down');
  });

  it('llm_upstream produces the same primary message regardless of details presence', () => {
    // Details vs no-details must not change the primary message —
    // callers reading err.details for an expander get the same UX.
    const withDetails = askErrorMessage(new ApiError('x', 502, 'llm_upstream', 'detail'));
    const without = askErrorMessage(new ApiError('x', 502, 'llm_upstream'));
    expect(withDetails).toBe(without);
  });

  it('maps llm_synthesis_failed to a try-again line', () => {
    const e = new ApiError('synthesis failed', 500, 'llm_synthesis_failed');
    const msg = askErrorMessage(e);
    expect(msg).toMatch(/failed to answer|start a new chat|Try again/i);
  });

  it('returns the generic line for unknown codes — never leaks raw backend message', () => {
    // Surfacing raw backend text for new codes would defeat the
    // consistent-copy contract and could leak provider internals,
    // so the default branch goes to the generic line. Future codes
    // need an explicit case in the switch.
    const e = new ApiError('mystery upstream detail', 500, 'something_new' as never);
    const msg = askErrorMessage(e);
    expect(msg).not.toBe('mystery upstream detail');
    expect(msg).toContain('could not answer');
  });

  it('returns the generic line when the ApiError has no code at all', () => {
    const e = new ApiError('raw http 400 message', 400);
    const msg = askErrorMessage(e);
    expect(msg).not.toBe('raw http 400 message');
    expect(msg).toContain('could not answer');
  });

  it('falls back to the generic line when message is empty too', () => {
    const e = new ApiError('', 500);
    const msg = askErrorMessage(e);
    expect(msg).toContain('could not answer');
  });
});
