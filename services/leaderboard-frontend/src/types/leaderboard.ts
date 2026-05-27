/**
 * Leaderboard domain types for the DBHP frontend.
 * These mirror the WebSocket push schema and REST snapshot response
 * documented in the Leaderboard Service design.
 */

/** All possible run statuses surfaced to the frontend. */
export type RunStatus =
  | 'QUEUED'
  | 'BUILDING'
  | 'DEPLOYING'
  | 'RUNNING'
  | 'IN_PROGRESS'
  | 'COLLECTING'
  | 'SCORING'
  | 'COMPLETE'
  | 'BUILD_FAILED'
  | 'BUILD_TIMEOUT'
  | 'SANDBOX_CRASH'
  | 'NO_ENDPOINTS'
  | 'PROVISIONING_TIMEOUT'
  | 'INSUFFICIENT_CAPACITY'
  | 'COLLECTION_TIMEOUT'
  | 'NETWORK_SETUP_FAILED'
  | 'FAILED';

/** A single entry in the leaderboard. */
export interface LeaderboardEntry {
  rank: number;
  contestant_handle: string;
  benchmark_run_id: string;
  composite_score: number;
  speed_score: number;
  stability_score: number;
  accuracy_score: number;
  p99_latency_ms: number;
  max_tps: number;
  fill_accuracy_pct: number;
  run_status: RunStatus;
}

/**
 * SCORE_UPDATE WebSocket push message.
 * Sent by the Leaderboard Service whenever scores change.
 */
export interface ScoreUpdate {
  type: 'SCORE_UPDATE';
  benchmark_run_id: string;
  contestant_handle: string;
  rank: number;
  composite_score: number;
  speed_score: number;
  stability_score: number;
  accuracy_score: number;
  p99_latency_ms: number;
  max_tps: number;
  fill_accuracy_pct: number;
  run_status: RunStatus;
  timestamp: string;
}

/**
 * Initial snapshot response from GET /api/v1/leaderboard.
 */
export interface LeaderboardSnapshot {
  entries: LeaderboardEntry[];
  total: number;
  computed_at: string;
}

/**
 * A single time-series metric data point for the per-contestant chart.
 */
export interface MetricDataPoint {
  timestamp: number; // ms since epoch
  p50_latency_ms: number;
  p90_latency_ms: number;
  p99_latency_ms: number;
  tps: number;
}
