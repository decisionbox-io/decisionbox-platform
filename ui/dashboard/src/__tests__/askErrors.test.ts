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

  it('maps llm_upstream to provider-side copy and includes details when present', () => {
    const e = new ApiError('rate limited', 502, 'llm_upstream', 'rate_limit_error — slow down');
    const msg = askErrorMessage(e);
    expect(msg).toContain('LLM provider rejected');
    expect(msg).toContain('rate_limit_error');
  });

  it('maps llm_upstream to provider-side copy without details', () => {
    const e = new ApiError('rate limited', 502, 'llm_upstream');
    const msg = askErrorMessage(e);
    expect(msg).toContain('LLM provider rejected');
    expect(msg).not.toContain('()');
  });

  it('maps llm_synthesis_failed to a try-again line', () => {
    const e = new ApiError('synthesis failed', 500, 'llm_synthesis_failed');
    const msg = askErrorMessage(e);
    expect(msg).toMatch(/failed to answer|start a new chat|Try again/i);
  });

  it('falls through to the message on unknown codes', () => {
    const e = new ApiError('mystery', 500, 'something_new' as never);
    const msg = askErrorMessage(e);
    expect(msg).toBe('mystery');
  });

  it('falls back to the generic line when message is empty too', () => {
    const e = new ApiError('', 500);
    const msg = askErrorMessage(e);
    expect(msg).toContain('could not answer');
  });
});
