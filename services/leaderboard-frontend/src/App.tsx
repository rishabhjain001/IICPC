import { useState } from 'react'
import './App.css'
import { ContestantChart } from './components/ContestantChart'
import { LeaderboardTable } from './components/LeaderboardTable'
import { useContestantMetrics } from './hooks/useContestantMetrics'
import { useLeaderboard } from './hooks/useLeaderboard'

/**
 * Root application component.
 *
 * On mount:
 *   1. Fetches initial snapshot via GET /api/v1/leaderboard (inside useLeaderboard)
 *   2. Establishes WebSocket connection and applies live SCORE_UPDATE events
 *   3. Shows the leaderboard table immediately; chart renders when a row is selected
 *
 * Requirements: 10.1, 10.2, 10.3, 10.4, 10.5, 10.7, 10.8
 */
function App() {
  const [selectedHandle, setSelectedHandle] = useState<string | null>(null)
  const { entries, loading, error, lastUpdated, connected } = useLeaderboard()

  const {
    dataPoints,
    runStatus,
    elapsedMs,
    currentTPS,
    latestP99Ms,
  } = useContestantMetrics(selectedHandle)

  const handleSelect = (handle: string) => {
    setSelectedHandle(prev => (prev === handle ? null : handle))
  }

  return (
    <>
      <header className="app-header">
        <div>
          <div className="app-title">IICPC — Live Leaderboard</div>
          <div className="app-subtitle">Distributed Benchmarking Platform</div>
          {lastUpdated && (
            <div className="last-updated">
              Last updated: {lastUpdated.toLocaleTimeString()}
            </div>
          )}
        </div>
        <div className="connection-status">
          <span
            className={`connection-dot ${connected ? 'connected' : 'disconnected'}`}
          />
          <span>{connected ? 'Live' : 'Reconnecting…'}</span>
        </div>
      </header>

      <p className="section-label">Standings (click a row to view metrics)</p>

      <LeaderboardTable
        entries={entries}
        loading={loading}
        error={error}
        selectedHandle={selectedHandle}
        onSelect={handleSelect}
      />

      {selectedHandle && (
        <>
          <p className="section-label">{selectedHandle} — Analytics</p>
          <ContestantChart
            contestantHandle={selectedHandle}
            dataPoints={dataPoints}
            runStatus={runStatus}
            elapsedMs={elapsedMs}
            currentTPS={currentTPS}
            latestP99Ms={latestP99Ms}
          />
        </>
      )}
    </>
  )
}

export default App
