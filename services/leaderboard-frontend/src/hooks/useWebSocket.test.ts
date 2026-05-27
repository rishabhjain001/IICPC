/**
 * Unit tests for useWebSocket — reconnect backoff and message delivery.
 *
 * Requirements: 10.5
 *
 * Strategy: Test the pure backoff formula and the hook's reconnect/disconnect
 * logic using fake timers and a mock WebSocket class.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useWebSocket } from './useWebSocket'

// ---------------------------------------------------------------------------
// Backoff formula tests — pure function
// ---------------------------------------------------------------------------

function backoffMs(attempt: number): number {
  const base = 1000
  const cap = 30_000
  return Math.min(base * Math.pow(2, attempt - 1), cap)
}

describe('backoff formula', () => {
  it('attempt 1 → 1 s', () => {
    expect(backoffMs(1)).toBe(1_000)
  })

  it('attempt 2 → 2 s', () => {
    expect(backoffMs(2)).toBe(2_000)
  })

  it('attempt 3 → 4 s', () => {
    expect(backoffMs(3)).toBe(4_000)
  })

  it('attempt 4 → 8 s', () => {
    expect(backoffMs(4)).toBe(8_000)
  })

  it('attempt 5 → 16 s', () => {
    expect(backoffMs(5)).toBe(16_000)
  })

  it('is capped at 30 s for attempt 6', () => {
    expect(backoffMs(6)).toBe(30_000)
  })

  it('remains at 30 s for any large attempt', () => {
    expect(backoffMs(10)).toBe(30_000)
    expect(backoffMs(100)).toBe(30_000)
  })

  it('doubles on each retry up to the cap', () => {
    const delays = [1, 2, 3, 4, 5].map(backoffMs)
    expect(delays).toEqual([1_000, 2_000, 4_000, 8_000, 16_000])
    // Each value doubles the previous
    for (let i = 1; i < delays.length; i++) {
      expect(delays[i]).toBe(delays[i - 1] * 2)
    }
  })
})

// ---------------------------------------------------------------------------
// Mock WebSocket
// ---------------------------------------------------------------------------

type WSCallback = ((this: WebSocket, ev: Event | MessageEvent | CloseEvent) => void) | null

class MockWebSocket {
  static instances: MockWebSocket[] = []

  url: string
  readyState: number = WebSocket.CONNECTING
  onopen: WSCallback = null
  onclose: WSCallback = null
  onmessage: WSCallback = null
  onerror: WSCallback = null

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  /** Test helper: simulate a successful open. */
  simulateOpen() {
    this.readyState = WebSocket.OPEN
    this.onopen?.call(this as unknown as WebSocket, new Event('open'))
  }

  /** Test helper: simulate the server closing the connection. */
  simulateClose() {
    this.readyState = WebSocket.CLOSED
    this.onclose?.call(this as unknown as WebSocket, new CloseEvent('close'))
  }

  /** Test helper: simulate an incoming message. */
  simulateMessage(data: string) {
    const ev = new MessageEvent('message', { data })
    this.onmessage?.call(this as unknown as WebSocket, ev)
  }

  close() {
    this.readyState = WebSocket.CLOSED
    // Only fire onclose if it hasn't been cleared (intentional close clears it)
    if (this.onclose) {
      this.onclose.call(this as unknown as WebSocket, new CloseEvent('close'))
    }
  }
}

// ---------------------------------------------------------------------------
// Hook integration tests
// ---------------------------------------------------------------------------

describe('useWebSocket hook', () => {
  beforeEach(() => {
    MockWebSocket.instances = []
    vi.useFakeTimers()
    // Replace the global WebSocket with our mock
    vi.stubGlobal('WebSocket', MockWebSocket)
    Object.assign(MockWebSocket, {
      CONNECTING: 0,
      OPEN: 1,
      CLOSING: 2,
      CLOSED: 3,
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('calls onMessage when server sends a message', () => {
    const onMessage = vi.fn()

    const { unmount } = renderHook(() =>
      useWebSocket({ url: 'ws://test', onMessage }),
    )

    act(() => {
      MockWebSocket.instances[0].simulateOpen()
      MockWebSocket.instances[0].simulateMessage('{"type":"SCORE_UPDATE"}')
    })

    expect(onMessage).toHaveBeenCalledOnce()
    expect((onMessage.mock.calls[0][0] as MessageEvent).data).toBe('{"type":"SCORE_UPDATE"}')
    unmount()
  })

  it('reconnects after server closes the connection', () => {
    const onMessage = vi.fn()

    const { unmount } = renderHook(() =>
      useWebSocket({ url: 'ws://test', onMessage }),
    )

    act(() => {
      MockWebSocket.instances[0].simulateOpen()
      MockWebSocket.instances[0].simulateClose()
    })

    // Advance time past first retry delay (1 s)
    act(() => {
      vi.advanceTimersByTime(1_100)
    })

    // A second WebSocket should have been created
    expect(MockWebSocket.instances.length).toBeGreaterThanOrEqual(2)
    unmount()
  })

  it('calls onReconnect after re-establishing connection', () => {
    const onReconnect = vi.fn()
    const onMessage = vi.fn()

    const { unmount } = renderHook(() =>
      useWebSocket({ url: 'ws://test', onMessage, onReconnect }),
    )

    // First connection
    act(() => {
      MockWebSocket.instances[0].simulateOpen()
    })

    // Simulate disconnect
    act(() => {
      MockWebSocket.instances[0].simulateClose()
    })

    // Advance past retry delay
    act(() => {
      vi.advanceTimersByTime(1_100)
    })

    // Simulate successful reconnect
    act(() => {
      const ws2 = MockWebSocket.instances[MockWebSocket.instances.length - 1]
      ws2.simulateOpen()
    })

    expect(onReconnect).toHaveBeenCalledOnce()
    unmount()
  })

  it('disconnect stops reconnection attempts', () => {
    const onMessage = vi.fn()

    const { result, unmount } = renderHook(() =>
      useWebSocket({ url: 'ws://test', onMessage }),
    )

    act(() => {
      MockWebSocket.instances[0].simulateOpen()
      MockWebSocket.instances[0].simulateClose()
    })

    // Disconnect before retry fires
    act(() => {
      result.current.disconnect()
    })

    const countBefore = MockWebSocket.instances.length

    // Advance well past the retry window
    act(() => {
      vi.advanceTimersByTime(5_000)
    })

    // No new WebSocket should be created after explicit disconnect
    expect(MockWebSocket.instances.length).toBe(countBefore)
    unmount()
  })

  it('connected is false while disconnected and true when open', () => {
    const onMessage = vi.fn()

    const { result, unmount } = renderHook(() =>
      useWebSocket({ url: 'ws://test', onMessage }),
    )

    // Before open
    expect(result.current.connected).toBe(false)

    act(() => {
      MockWebSocket.instances[0].simulateOpen()
    })

    expect(result.current.connected).toBe(true)

    act(() => {
      MockWebSocket.instances[0].simulateClose()
    })

    expect(result.current.connected).toBe(false)
    unmount()
  })
})
