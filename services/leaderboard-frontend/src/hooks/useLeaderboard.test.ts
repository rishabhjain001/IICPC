/**
 * Unit tests for useLeaderboard — snapshot fetch and WebSocket update handling.
 *
 * Requirements: 10.5, 10.8
 *
 * Strategy:
 *   - Mock global fetch for the REST snapshot
 *   - Mock global WebSocket for live updates
 *   - Verify entries are populated from the snapshot
 *   - Verify SCORE_UPDATE messages are applied to entries
 *   - Verify stale data is preserved during disconnection
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { useLeaderboard } from './useLeaderboard'
import type { LeaderboardEntry, LeaderboardSnapshot, ScoreUpdate } from '../types/leaderboard'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeEntry(handle: string, score: number): LeaderboardEntry {
  return {
    rank: 1,
    contestant_handle: handle,
    benchmark_run_id: 'run-1',
    composite_score: score,
    speed_score: score,
    stability_score: score,
    accuracy_score: score,
    p99_latency_ms: 5,
    max_tps: 100_000,
    fill_accuracy_pct: 90,
    run_status: 'COMPLETE',
  }
}

function makeSnapshot(entries: LeaderboardEntry[]): LeaderboardSnapshot {
  return {
    entries,
    total: entries.length,
    computed_at: new Date().toISOString(),
  }
}

function makeScoreUpdate(handle: string, score: number): ScoreUpdate {
  return {
    type: 'SCORE_UPDATE',
    benchmark_run_id: 'run-2',
    contestant_handle: handle,
    rank: 1,
    composite_score: score,
    speed_score: score,
    stability_score: score,
    accuracy_score: score,
    p99_latency_ms: 3,
    max_tps: 200_000,
    fill_accuracy_pct: 95,
    run_status: 'RUNNING',
    timestamp: new Date().toISOString(),
  }
}

// ---------------------------------------------------------------------------
// Minimal mock WebSocket that stores registered callbacks so tests can drive them.
// ---------------------------------------------------------------------------

type WSEventHandler = ((this: WebSocket, ev: Event | MessageEvent | CloseEvent) => void) | null

class MockWebSocket {
  static instances: MockWebSocket[] = []

  url: string
  readyState = 0
  onopen: WSEventHandler = null
  onclose: WSEventHandler = null
  onmessage: WSEventHandler = null
  onerror: WSEventHandler = null

  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  simulateOpen() {
    this.readyState = 1
    this.onopen?.call(this as unknown as WebSocket, new Event('open'))
  }

  simulateClose() {
    this.readyState = 3
    // Only call if not already cleared (hook clears onclose on intentional close)
    this.onclose?.call(this as unknown as WebSocket, new CloseEvent('close'))
  }

  simulateMessage(data: string) {
    const ev = new MessageEvent('message', { data })
    this.onmessage?.call(this as unknown as WebSocket, ev)
  }

  close() {
    if (this.readyState !== MockWebSocket.CLOSED) {
      this.readyState = 3
      if (this.onclose) {
        this.onclose.call(this as unknown as WebSocket, new CloseEvent('close'))
      }
    }
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('useLeaderboard', () => {
  beforeEach(() => {
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it('fetches initial snapshot from /api/v1/leaderboard', async () => {
    const entries = [makeEntry('alice', 0.9), makeEntry('bob', 0.8)]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => makeSnapshot(entries),
      }),
    )

    const { result } = renderHook(() => useLeaderboard())

    expect(result.current.loading).toBe(true)

    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.entries).toHaveLength(2)
    expect(result.current.entries[0].contestant_handle).toBe('alice')
    expect(result.current.entries[1].contestant_handle).toBe('bob')
    expect(result.current.error).toBeNull()
  })

  it('applies SCORE_UPDATE WebSocket message to the entry list', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => makeSnapshot([makeEntry('alice', 0.9)]),
      }),
    )

    const { result } = renderHook(() => useLeaderboard())
    await waitFor(() => expect(result.current.loading).toBe(false))

    // Deliver a SCORE_UPDATE for a new contestant with a higher score
    act(() => {
      const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1]
      ws.simulateMessage(JSON.stringify(makeScoreUpdate('bob', 0.95)))
    })

    await waitFor(() => expect(result.current.entries).toHaveLength(2))

    // bob should be ranked 1 (0.95 > 0.9)
    expect(result.current.entries[0].contestant_handle).toBe('bob')
    expect(result.current.entries[0].composite_score).toBe(0.95)
  })

  it('updates an existing contestant entry on SCORE_UPDATE (no duplicate)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => makeSnapshot([makeEntry('alice', 0.9)]),
      }),
    )

    const { result } = renderHook(() => useLeaderboard())
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => {
      const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1]
      ws.simulateMessage(JSON.stringify(makeScoreUpdate('alice', 0.99)))
    })

    await waitFor(() =>
      expect(result.current.entries[0].composite_score).toBe(0.99),
    )
    // alice replaced, not duplicated
    expect(result.current.entries).toHaveLength(1)
  })

  it('shows stale data during WebSocket disconnection — entries not cleared', async () => {
    const initialEntries = [makeEntry('alice', 0.9), makeEntry('bob', 0.8)]
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => makeSnapshot(initialEntries),
      }),
    )

    const { result } = renderHook(() => useLeaderboard())
    await waitFor(() => expect(result.current.loading).toBe(false))

    const entriesBefore = result.current.entries

    // Drop the WebSocket connection
    act(() => {
      const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1]
      // Null out onclose first so the close doesn't trigger reconnect timer
      // (we only care about entry preservation here)
      ws.onclose = null
      ws.readyState = 3
    })

    // Entries MUST still be present (stale data retained per Req 10.5)
    expect(result.current.entries).toHaveLength(entriesBefore.length)
    expect(result.current.entries[0].contestant_handle).toBe('alice')
  })

  it('re-fetches snapshot on WebSocket reconnect', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => makeSnapshot([makeEntry('alice', 0.9)]),
    })
    vi.stubGlobal('fetch', fetchMock)

    vi.useFakeTimers({ shouldAdvanceTime: true })

    const { unmount } = renderHook(() => useLeaderboard())

    // Let initial fetch complete
    await act(async () => {
      await vi.runAllTimersAsync()
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)

    // Mark that we've connected once so the hook knows next open = reconnect
    act(() => {
      MockWebSocket.instances[0].simulateOpen()
    })

    // Simulate disconnect — triggers backoff timer
    act(() => {
      MockWebSocket.instances[0].simulateClose()
    })

    // Advance past the 1 s backoff delay so a new WS is created
    await act(async () => {
      vi.advanceTimersByTime(1_500)
    })

    // New WS opens → hook sees it as a reconnect → re-fetches snapshot
    act(() => {
      const ws = MockWebSocket.instances[MockWebSocket.instances.length - 1]
      ws.simulateOpen()
    })

    // Flush pending microtasks (e.g. the async fetchSnapshot call)
    await act(async () => {
      await new Promise(resolve => setTimeout(resolve, 0))
    })

    expect(fetchMock).toHaveBeenCalledTimes(2)

    vi.useRealTimers()
    unmount()
  })

  it('sets error state when initial fetch fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ ok: false, status: 503 }),
    )

    const { result } = renderHook(() => useLeaderboard())
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toMatch(/503/)
    expect(result.current.entries).toEqual([])
  })
})
