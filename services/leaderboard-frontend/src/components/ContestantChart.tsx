import {
  CategoryScale,
  Chart as ChartJS,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  TimeScale,
  Title,
  Tooltip,
} from 'chart.js'
import { Line } from 'react-chartjs-2'
import type { MetricDataPoint, RunStatus } from '../types/leaderboard'

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  TimeScale,
)

interface ContestantChartProps {
  contestantHandle: string
  dataPoints: MetricDataPoint[]
  runStatus: RunStatus | null
  elapsedMs: number | null
  currentTPS: number | null
  latestP99Ms: number | null
}

function fmtElapsed(ms: number): string {
  const totalSec = Math.floor(ms / 1000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

/**
 * Live time-series chart for p50/p90/p99 latency and TPS.
 *
 * - Left y-axis: latency (ms)
 * - Right y-axis: TPS
 * - X-axis: last 60 data points (time labels)
 * - Shows "No data available" when no data exists
 * - Shows live progress indicator for IN_PROGRESS runs
 *
 * Requirements: 10.4, 10.7
 */
export function ContestantChart({
  contestantHandle,
  dataPoints,
  runStatus,
  elapsedMs,
  currentTPS,
  latestP99Ms,
}: ContestantChartProps) {
  const isLive = runStatus === 'IN_PROGRESS' || runStatus === 'RUNNING'
  const noData = dataPoints.length === 0

  const labels = dataPoints.map(dp => {
    const d = new Date(dp.timestamp)
    return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}:${d.getSeconds().toString().padStart(2, '0')}`
  })

  const chartData = {
    labels,
    datasets: [
      {
        label: 'p50 latency (ms)',
        data: dataPoints.map(dp => dp.p50_latency_ms),
        borderColor: '#89b4fa',
        backgroundColor: 'rgba(137,180,250,0.1)',
        pointRadius: 2,
        tension: 0.3,
        yAxisID: 'yLatency',
      },
      {
        label: 'p90 latency (ms)',
        data: dataPoints.map(dp => dp.p90_latency_ms),
        borderColor: '#f9e2af',
        backgroundColor: 'rgba(249,226,175,0.1)',
        pointRadius: 2,
        tension: 0.3,
        yAxisID: 'yLatency',
      },
      {
        label: 'p99 latency (ms)',
        data: dataPoints.map(dp => dp.p99_latency_ms),
        borderColor: '#f38ba8',
        backgroundColor: 'rgba(243,139,168,0.1)',
        pointRadius: 2,
        tension: 0.3,
        yAxisID: 'yLatency',
      },
      {
        label: 'TPS',
        data: dataPoints.map(dp => dp.tps),
        borderColor: '#a6e3a1',
        backgroundColor: 'rgba(166,227,161,0.1)',
        pointRadius: 2,
        tension: 0.3,
        yAxisID: 'yTPS',
      },
    ],
  }

  const options = {
    responsive: true,
    interaction: {
      mode: 'index' as const,
      intersect: false,
    },
    animation: {
      duration: 200,
    },
    plugins: {
      legend: {
        labels: { color: '#cdd6f4', font: { size: 12 } },
      },
      title: {
        display: true,
        text: `${contestantHandle} — Live Metrics`,
        color: '#89b4fa',
        font: { size: 14, weight: 'bold' as const },
      },
    },
    scales: {
      x: {
        ticks: { color: '#6c7086', maxTicksLimit: 10, maxRotation: 0 },
        grid: { color: '#313244' },
      },
      yLatency: {
        type: 'linear' as const,
        position: 'left' as const,
        title: {
          display: true,
          text: 'Latency (ms)',
          color: '#89b4fa',
        },
        ticks: { color: '#cdd6f4' },
        grid: { color: '#313244' },
      },
      yTPS: {
        type: 'linear' as const,
        position: 'right' as const,
        title: {
          display: true,
          text: 'TPS',
          color: '#a6e3a1',
        },
        ticks: { color: '#a6e3a1' },
        grid: { drawOnChartArea: false },
      },
    },
  }

  return (
    <div
      style={{
        background: '#1e1e2e',
        borderRadius: 8,
        padding: '16px',
        marginTop: 16,
      }}
    >
      {isLive && (
        <div
          style={{
            display: 'flex',
            gap: 24,
            marginBottom: 12,
            fontSize: '0.85rem',
            color: '#cdd6f4',
          }}
        >
          <span>
            <span style={{ color: '#f9e2af', fontWeight: 600 }}>⏱ Elapsed: </span>
            {elapsedMs !== null ? fmtElapsed(elapsedMs) : '—'}
          </span>
          <span>
            <span style={{ color: '#a6e3a1', fontWeight: 600 }}>TPS: </span>
            {currentTPS !== null ? currentTPS.toLocaleString() : '—'}
          </span>
          <span>
            <span style={{ color: '#f38ba8', fontWeight: 600 }}>p99: </span>
            {latestP99Ms !== null ? `${latestP99Ms.toFixed(3)} ms` : '—'}
          </span>
          <span
            style={{
              marginLeft: 'auto',
              color: '#a6e3a1',
              display: 'flex',
              alignItems: 'center',
              gap: 6,
            }}
          >
            <span
              style={{
                width: 8,
                height: 8,
                borderRadius: '50%',
                background: '#a6e3a1',
                display: 'inline-block',
                animation: 'pulse 1s infinite',
              }}
            />
            LIVE
          </span>
        </div>
      )}

      {noData ? (
        <div
          style={{
            height: 240,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: '#6c7086',
            fontSize: '1rem',
          }}
        >
          No data available
        </div>
      ) : (
        <Line data={chartData} options={options} />
      )}
    </div>
  )
}
