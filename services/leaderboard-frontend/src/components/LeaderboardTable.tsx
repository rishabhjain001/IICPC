import type { LeaderboardEntry } from '../types/leaderboard'
import { StatusBadge } from './StatusBadge'
import './LeaderboardTable.css'

interface LeaderboardTableProps {
  entries: LeaderboardEntry[]
  loading: boolean
  error: string | null
  selectedHandle: string | null
  onSelect: (handle: string) => void
}

function fmt4(n: number): string {
  return n.toFixed(4)
}

function fmtPct(n: number): string {
  return `${n.toFixed(2)}%`
}

function fmtTPS(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function rankClass(rank: number): string {
  if (rank === 1) return 'rank-cell top1'
  if (rank === 2) return 'rank-cell top2'
  if (rank === 3) return 'rank-cell top3'
  return 'rank-cell'
}

/**
 * Ranked standings table.
 *
 * - Sorted by CompositeScore descending (handled upstream in useLeaderboard)
 * - Scores formatted to 4 decimal places
 * - Status shown as a colored badge
 *
 * Requirements: 10.3, 10.8
 */
export function LeaderboardTable({
  entries,
  loading,
  error,
  selectedHandle,
  onSelect,
}: LeaderboardTableProps) {
  if (loading) {
    return <div className="lb-loading">Loading leaderboard…</div>
  }

  if (error) {
    return <div className="lb-error">Error: {error}</div>
  }

  if (entries.length === 0) {
    return <div className="lb-empty">No contestants yet.</div>
  }

  return (
    <div className="lb-table-wrapper">
      <table className="lb-table">
        <thead>
          <tr>
            <th>#</th>
            <th>Handle</th>
            <th>Composite</th>
            <th>Speed</th>
            <th>Stability</th>
            <th>Accuracy</th>
            <th>p99 (ms)</th>
            <th>Max TPS</th>
            <th>Fill%</th>
            <th>Status</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(entry => (
            <tr
              key={entry.contestant_handle}
              className={entry.contestant_handle === selectedHandle ? 'selected' : ''}
              onClick={() => onSelect(entry.contestant_handle)}
            >
              <td className={rankClass(entry.rank)}>{entry.rank}</td>
              <td>{entry.contestant_handle}</td>
              <td className="score-highlight">{fmt4(entry.composite_score)}</td>
              <td>{fmt4(entry.speed_score)}</td>
              <td>{fmt4(entry.stability_score)}</td>
              <td>{fmt4(entry.accuracy_score)}</td>
              <td>{entry.p99_latency_ms.toFixed(3)}</td>
              <td>{fmtTPS(entry.max_tps)}</td>
              <td>{fmtPct(entry.fill_accuracy_pct)}</td>
              <td>
                <StatusBadge status={entry.run_status} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
