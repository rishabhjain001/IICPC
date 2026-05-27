import type { RunStatus } from '../types/leaderboard'

interface StatusBadgeProps {
  status: RunStatus
}

function badgeColor(status: RunStatus): string {
  switch (status) {
    case 'COMPLETE':
      return '#22c55e' // green
    case 'RUNNING':
    case 'IN_PROGRESS':
    case 'COLLECTING':
    case 'SCORING':
    case 'DEPLOYING':
    case 'BUILDING':
    case 'QUEUED':
      return '#f59e0b' // yellow/amber
    case 'BUILD_FAILED':
    case 'BUILD_TIMEOUT':
    case 'SANDBOX_CRASH':
    case 'NO_ENDPOINTS':
    case 'PROVISIONING_TIMEOUT':
    case 'INSUFFICIENT_CAPACITY':
    case 'COLLECTION_TIMEOUT':
    case 'NETWORK_SETUP_FAILED':
    case 'FAILED':
      return '#ef4444' // red
    default:
      return '#6b7280' // grey fallback
  }
}

/**
 * Colored pill badge for Benchmark Run status.
 */
export function StatusBadge({ status }: StatusBadgeProps) {
  const color = badgeColor(status)
  return (
    <span
      style={{
        display: 'inline-block',
        padding: '2px 8px',
        borderRadius: '9999px',
        backgroundColor: color,
        color: '#fff',
        fontSize: '0.75rem',
        fontWeight: 600,
        letterSpacing: '0.03em',
        whiteSpace: 'nowrap',
      }}
    >
      {status}
    </span>
  )
}
