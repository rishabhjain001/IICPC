import { useCallback, useEffect, useRef, useState } from 'react'

export interface UseWebSocketOptions {
  url: string
  onMessage: (event: MessageEvent) => void
  onOpen?: () => void
  onClose?: () => void
  onReconnect?: () => void
}

/**
 * Reconnecting WebSocket hook with exponential backoff.
 *
 * Backoff schedule (capped at 30 s):
 *   attempt 1 → 1 s
 *   attempt 2 → 2 s
 *   attempt 3 → 4 s
 *   attempt 4 → 8 s
 *   ...
 *   attempt N → min(2^(N-1), 30) s
 *
 * Requirements: 10.1, 10.2, 10.5
 */
export function useWebSocket(options: UseWebSocketOptions): {
  connected: boolean
  disconnect: () => void
} {
  const { url, onMessage, onOpen, onClose, onReconnect } = options

  const [connected, setConnected] = useState(false)

  // Refs so closures inside WebSocket callbacks always see the latest values
  // without re-running the effect.
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectAttemptRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const intentionalCloseRef = useRef(false)

  // Keep option callbacks stable in refs so callers don't need to memoize them.
  const onMessageRef = useRef(onMessage)
  const onOpenRef = useRef(onOpen)
  const onCloseRef = useRef(onClose)
  const onReconnectRef = useRef(onReconnect)

  useEffect(() => { onMessageRef.current = onMessage }, [onMessage])
  useEffect(() => { onOpenRef.current = onOpen }, [onOpen])
  useEffect(() => { onCloseRef.current = onClose }, [onClose])
  useEffect(() => { onReconnectRef.current = onReconnect }, [onReconnect])

  /** Compute delay in ms for a given attempt number (1-indexed). */
  const backoffMs = (attempt: number): number => {
    const base = 1000 // 1 s
    const cap = 30_000 // 30 s
    return Math.min(base * Math.pow(2, attempt - 1), cap)
  }

  const clearReconnectTimer = useCallback(() => {
    if (reconnectTimerRef.current !== null) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }
  }, [])

  const connect = useCallback(() => {
    if (intentionalCloseRef.current) return

    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      reconnectAttemptRef.current = 0
      setConnected(true)
      onOpenRef.current?.()

      // If this is a reconnection (attempt was >0 before reset) call onReconnect.
      // We detect a reconnect when the previous close was not the very first connect.
      // We track this via a separate "hasConnectedBefore" ref.
      if (hasConnectedBeforeRef.current) {
        onReconnectRef.current?.()
      }
      hasConnectedBeforeRef.current = true
    }

    ws.onmessage = (event: MessageEvent) => {
      onMessageRef.current(event)
    }

    ws.onclose = () => {
      setConnected(false)
      onCloseRef.current?.()
      wsRef.current = null

      if (!intentionalCloseRef.current) {
        reconnectAttemptRef.current += 1
        const delay = backoffMs(reconnectAttemptRef.current)
        reconnectTimerRef.current = setTimeout(() => {
          connect()
        }, delay)
      }
    }

    ws.onerror = () => {
      // onerror is always followed by onclose; close handles retry.
      ws.close()
    }
  }, [url, clearReconnectTimer]) // eslint-disable-line react-hooks/exhaustive-deps

  const hasConnectedBeforeRef = useRef(false)

  useEffect(() => {
    intentionalCloseRef.current = false
    reconnectAttemptRef.current = 0
    hasConnectedBeforeRef.current = false
    connect()

    return () => {
      intentionalCloseRef.current = true
      clearReconnectTimer()
      if (wsRef.current) {
        wsRef.current.onclose = null // prevent reconnect on cleanup
        wsRef.current.close()
        wsRef.current = null
      }
    }
  }, [connect, clearReconnectTimer])

  const disconnect = useCallback(() => {
    intentionalCloseRef.current = true
    clearReconnectTimer()
    if (wsRef.current) {
      wsRef.current.onclose = null
      wsRef.current.close()
      wsRef.current = null
    }
    setConnected(false)
  }, [clearReconnectTimer])

  return { connected, disconnect }
}
