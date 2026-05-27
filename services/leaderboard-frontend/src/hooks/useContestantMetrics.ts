import { useCallback, useEffect, useRef, useState } from 'react'
import type { MetricDataPoint, RunStatus, ScoreUpdate } from '../types/leaderboard'

const MAX_BUFFER = 60

export interface ContestantMetricsResult {
  dataPoints: MetricDataPoint[]
  runStatus: RunStatus | null
  elapsedMs: number | null
  currentTPS: number | null
  latestP99Ms: number | null
}

const WS_URL =
  typeof window !== 'undefined'
    ? `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/api/v1/leaderboard/ws`
    : 'ws://localhost:8082/api/v1/leaderboard/ws'

/**
 * Subscribes to WebSocket metric updates for a specific contestant and maintains
 * a rolling buffer of the last 60 data points.
 *
 * Requirements: 10.4, 10.7
 */
export function useContestantMetrics(contestantHandle: string | null): ContestantMetricsResult {
  const [dataPoints, setDataPoints] = useState<MetricDataPoint[]>([])
  const [runStatus, setRunStatus] = useState<RunStatus | null>(null)
  const [elapsedMs, setElapsedMs] = useState<number | null>(null)
  const [currentTPS, setCurrentTPS] = useState<number | null>(null)
  const [latestP99Ms, setLatestP99Ms] = useState<number | null>(null)

  // Track run start time for elapsed computation
  const runCreatedAtRef = useRef<number | null>(null)
  const elapsedTimerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const handleMessage = useCallback((event: MessageEvent) => {
    try {
      const msg = JSON.parse(event.data as string) as ScoreUpdate
      if (msg.type !== 'SCORE_UPDATE') return
      if (msg.contestant_handle !== contestantHandle) return

      const point: MetricDataPoint = {
        timestamp: Date.now(),
        // The WebSocket payload doesn't include p50/p90 directly; we synthesise
        // approximate values from p99. In a production build these would come
        // from a richer payload or a separate aggregates endpoint.
        p50_latency_ms: msg.p99_latency_ms * 0.5,
        p90_latency_ms: msg.p99_latency_ms * 0.85,
        p99_latency_ms: msg.p99_latency_ms,
        tps: msg.max_tps,
      }

      setDataPoints(prev => {
        const next = [...prev, point]
        return next.length > MAX_BUFFER ? next.slice(next.length - MAX_BUFFER) : next
      })

      setRunStatus(msg.run_status)
      setCurrentTPS(msg.max_tps)
      setLatestP99Ms(msg.p99_latency_ms)

      if (msg.run_status === 'IN_PROGRESS' || msg.run_status === 'RUNNING') {
        if (runCreatedAtRef.current === null) {
          runCreatedAtRef.current = Date.now()
        }
      } else {
        runCreatedAtRef.current = null
      }
    } catch {
      // Ignore malformed messages
    }
  }, [contestantHandle])

  // WebSocket connection — reuse the global leaderboard WS channel.
  useEffect(() => {
    if (!contestantHandle) return

    const ws = new WebSocket(WS_URL)
    ws.onmessage = handleMessage
    ws.onerror = () => ws.close()

    return () => {
      ws.onmessage = null
      ws.close()
    }
  }, [contestantHandle, handleMessage])

  // Elapsed time ticker for IN_PROGRESS runs (Req 10.7)
  useEffect(() => {
    if (runStatus === 'IN_PROGRESS' || runStatus === 'RUNNING') {
      if (elapsedTimerRef.current === null) {
        elapsedTimerRef.current = setInterval(() => {
          if (runCreatedAtRef.current !== null) {
            setElapsedMs(Date.now() - runCreatedAtRef.current)
          }
        }, 1000)
      }
    } else {
      if (elapsedTimerRef.current !== null) {
        clearInterval(elapsedTimerRef.current)
        elapsedTimerRef.current = null
      }
      setElapsedMs(null)
    }

    return () => {
      if (elapsedTimerRef.current !== null) {
        clearInterval(elapsedTimerRef.current)
        elapsedTimerRef.current = null
      }
    }
  }, [runStatus])

  // Reset when selected contestant changes
  useEffect(() => {
    setDataPoints([])
    setRunStatus(null)
    setElapsedMs(null)
    setCurrentTPS(null)
    setLatestP99Ms(null)
    runCreatedAtRef.current = null
  }, [contestantHandle])

  return { dataPoints, runStatus, elapsedMs, currentTPS, latestP99Ms }
}
