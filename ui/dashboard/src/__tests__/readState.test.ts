/**
 * @jest-environment jsdom
 */
import { renderHook, act, waitFor } from '@testing-library/react';
import { useReadSet, markRead, markUnread, useIsRead, __resetReadStore } from '@/lib/readState';
import { api } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: {
    listReadIDs: jest.fn(),
    markRead: jest.fn(),
    markUnread: jest.fn(),
  },
}));

const mockedApi = api as jest.Mocked<typeof api>;

beforeEach(() => {
  __resetReadStore();
  jest.clearAllMocks();
});

describe('useReadSet', () => {
  it('fetches once per (projectId, targetType) and shares across hooks', async () => {
    mockedApi.listReadIDs.mockResolvedValue(['i1', 'i2']);

    const h1 = renderHook(() => useReadSet('p1', 'insight'));
    const h2 = renderHook(() => useReadSet('p1', 'insight'));

    await waitFor(() => {
      expect(h1.result.current.has('i1')).toBe(true);
      expect(h2.result.current.has('i2')).toBe(true);
    });

    // Both hooks caused at most one listReadIDs call — the module-level
    // store deduplicates inflight requests.
    expect(mockedApi.listReadIDs).toHaveBeenCalledTimes(1);
  });

  it('refetches for different (projectId, targetType) keys', async () => {
    mockedApi.listReadIDs.mockResolvedValue([]);
    renderHook(() => useReadSet('p1', 'insight'));
    renderHook(() => useReadSet('p1', 'recommendation'));
    renderHook(() => useReadSet('p2', 'insight'));

    await waitFor(() => {
      expect(mockedApi.listReadIDs).toHaveBeenCalledTimes(3);
    });
    expect(mockedApi.listReadIDs).toHaveBeenCalledWith('p1', 'insight');
    expect(mockedApi.listReadIDs).toHaveBeenCalledWith('p1', 'recommendation');
    expect(mockedApi.listReadIDs).toHaveBeenCalledWith('p2', 'insight');
  });

  it('returns empty set on api error', async () => {
    mockedApi.listReadIDs.mockRejectedValue(new Error('boom'));
    const { result } = renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(mockedApi.listReadIDs).toHaveBeenCalled());
    expect(result.current.size).toBe(0);
  });
});

describe('markRead', () => {
  it('optimistically updates the set before the API responds', async () => {
    mockedApi.listReadIDs.mockResolvedValue([]);
    let resolveMark: (v: { target_id: string; read_at: string }) => void = () => {};
    mockedApi.markRead.mockReturnValue(new Promise(r => { resolveMark = r; }));

    const { result } = renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(mockedApi.listReadIDs).toHaveBeenCalled());

    act(() => {
      void markRead('p1', 'insight', 'i1');
    });

    // Updated synchronously before the API resolves.
    expect(result.current.has('i1')).toBe(true);

    await act(async () => {
      resolveMark({ target_id: 'i1', read_at: '' });
    });
    expect(result.current.has('i1')).toBe(true);
  });

  it('dedupes repeated calls for the same target', async () => {
    mockedApi.listReadIDs.mockResolvedValue([]);
    mockedApi.markRead.mockResolvedValue({ target_id: 'i1', read_at: '' });

    renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(mockedApi.listReadIDs).toHaveBeenCalled());

    await act(async () => {
      await markRead('p1', 'insight', 'i1');
      await markRead('p1', 'insight', 'i1');
      await markRead('p1', 'insight', 'i1');
    });

    expect(mockedApi.markRead).toHaveBeenCalledTimes(1);
  });

  it('rolls back when the server rejects', async () => {
    mockedApi.listReadIDs.mockResolvedValue([]);
    mockedApi.markRead.mockRejectedValue(new Error('nope'));

    const { result } = renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(mockedApi.listReadIDs).toHaveBeenCalled());

    await act(async () => {
      await markRead('p1', 'insight', 'i1');
    });

    expect(result.current.has('i1')).toBe(false);
  });
});

describe('markUnread', () => {
  it('removes from set optimistically and rolls back on error', async () => {
    mockedApi.listReadIDs.mockResolvedValue(['i1']);
    mockedApi.markUnread.mockRejectedValue(new Error('nope'));

    const { result } = renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(result.current.has('i1')).toBe(true));

    await act(async () => {
      await markUnread('p1', 'insight', 'i1');
    });

    // Rolled back.
    expect(result.current.has('i1')).toBe(true);
  });

  it('is a no-op when target is not read', async () => {
    mockedApi.listReadIDs.mockResolvedValue([]);
    renderHook(() => useReadSet('p1', 'insight'));
    await waitFor(() => expect(mockedApi.listReadIDs).toHaveBeenCalled());

    await act(async () => {
      await markUnread('p1', 'insight', 'nope');
    });

    expect(mockedApi.markUnread).not.toHaveBeenCalled();
  });
});

describe('useIsRead', () => {
  it('returns boolean reflecting membership', async () => {
    mockedApi.listReadIDs.mockResolvedValue(['i1']);
    const { result: isI1 } = renderHook(() => useIsRead('p1', 'insight', 'i1'));
    const { result: isI2 } = renderHook(() => useIsRead('p1', 'insight', 'i2'));

    await waitFor(() => {
      expect(isI1.current).toBe(true);
      expect(isI2.current).toBe(false);
    });
  });
});
