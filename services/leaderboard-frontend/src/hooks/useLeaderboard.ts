import { useCallback, useEffect, useRef, useState } from 'react'
import type { LeaderboardEntry, ScoreUpdate } from '../types/leaderboard'

export interface UseLeaderboardResult {
  entries: LeaderboardEntry[]
  loading: boolean
  error: string | null
  lastUpdated: Date | null
  connected: boolean
}

/** Derive the WebSocket URL from the current page location. */
function getWsUrl(): string {
  if (typeof window === 'undefined') {
    return 'ws://localhost:8082/api/v1/leaderboard/ws'
  }
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${window.location.host}/api/v1/leaderboard/ws`
}

const RECONNECT_BASE_MS = 1_000
const RECONNECT_CAP_MS = 30_000

function backoffMs(attempt: number): number {
  return Math.min(RECONNECT_BASE_MS * Math.pow(2, attempt - 1), RECONNECT_CAP_MS)
}

function sortEntries(entries: LeaderboardEntry[]): LeaderboardEntry[] {
  return [...entries]
    .sort((a, b) => b.composite_score - a.composite_score)
    .map((e, i) => ({ ...e, rank: i + 1 }))
}

/**
 * Combines the initial REST snapshot fetch with live WebSocket SCORE_UPDATE
 * events to maintain an always-current sorted leaderboard.
 *
 * - Initial fetch via GET /api/v1/leaderboard on mount (Req 10.8)
 * - WebSocket SCORE_UPDATE events applied immediately (Req 10.1, 10.2)
 * - Reconnect with exponential backoff; re-fetch snapshot on reconnect (Req 10.5)
 * - Stale entries preserved during disconnection (Req 10.5)
 *
 * Requirements: 10.1, 10.2, 10.3, 10.5, 10.8
 */
export function useLeaderboard(): UseLeaderboardResult {
  const [entries, setEntries] = useState<LeaderboardEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const [connected, setConnected] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)
  const reconnectAttemptRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const unmountedRef = useRef(false)
  const hasConnectedRef = useRef(false)

  /** Fetch the current snapshot from the REST endpoint. */
  const fetchSnapshot = useCallback(async () => {
    try {
      const res = await fetch('/api/v1/leaderboard')
      if (!res.ok) {
        throw new Error(`Snapshot fetch failed: HTTP ${res.status}`)
      }
      const data = (await res.json()) as {
        entries: LeaderboardEntry[]
        total: number
        computed_at: string
      }
      if (!unmountedRef.current) {
        setEntries(sortEntries(data.entries))
        setLastUpdated(new Date(data.computed_at))
        setError(null)
      }
    } catch (err) {
      if (!unmountedRef.current) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      if (!unmountedRef.current) {
        setLoading(false)
      }
    }
  }, [])

  const clearReconnectTimer = useCallback(() => {
    if (reconnectTimerRef.current !== null) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }
  }, [])

  const connect = useCallback(() => {
    if (unmountedRef.current) return

    const url = getWsUrl()
    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      if (unmountedRef.current) return
      reconnectAttemptRef.current = 0
      setConnected(true)

      // If this is a reconnect (not the first open), re-fetch snapshot.
      if (hasConnectedRef.current) {
        void fetchSnapshot()
      }
      hasConnectedRef.current = true
    }

    ws.onmessage = (event: MessageEvent) => {
      if (unmountedRef.current) return
      try {
        const msg = JSON.parse(event.data as string) as ScoreUpdate
        if (msg.type !== 'SCORE_UPDATE') return

        setEntries(prev => {
          const filtered = prev.filter(
            e => e.contestant_handle !== msg.contestant_handle,
          )
          const updated: LeaderboardEntry = {
            rank: msg.rank,
            contestant_handle: msg.contestant_handle,
            benchmark_run_id: msg.benchmark_run_id,
            composite_score: msg.composite_score,
            speed_score: msg.speed_score,
            stability_score: msg.stability_score,
            accuracy_score: msg.accuracy_score,
            p99_latency_ms: msg.p99_latency_ms,
            max_tps: msg.max_tps,
            fill_accuracy_pct: msg.fill_accuracy_pct,
            run_status: msg.run_status,
          }
          return sortEntries([...filtered, updated])
        })
        setLastUpdated(new Date(msg.timestamp))
      } catch {
        // Ignore malformed messages
      }
    }

    ws.onclose = () => {
      if (unmountedRef.current) return
      setConnected(false)
      wsRef.current = null

      reconnectAttemptRef.current += 1
      const delay = backoffMs(reconnectAttemptRef.current)
      reconnectTimerRef.current = setTimeout(() => {
        connect()
      }, delay)
    }

    ws.onerror = () => {
      ws.close()
    }
  }, [fetchSnapshot, clearReconnectTimer]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    unmountedRef.current = false

    // Initial snapshot fetch
    void fetchSnapshot()

    // Start WebSocket connection
    connect()

    return () => {
      unmountedRef.current = true
      clearReconnectTimer()
      if (wsRef.current) {
        wsRef.current.onclose = null // suppress reconnect on cleanup
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [fetchSnapshot, connect, clearReconnectTimer])

  return { entries, loading, error, lastUpdated, connected }
}
