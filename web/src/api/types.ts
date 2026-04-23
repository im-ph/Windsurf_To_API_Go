// API DTOs — 1:1 with go/internal/dashapi types. int64 identifiers are
// represented as `number` since they remain under 2^53.

export interface AuthProbe {
  required: boolean;
  valid?: boolean;
}

export interface HealthInfo {
  status: string;
  version: string;
  commit?: string;
  commitMessage?: string;
  commitDate?: string;
  branch?: string;
  uptime?: number;
}

export interface AccountCounts {
  total: number;
  active: number;
  error: number;
  expired?: number;
  invalid?: number;
}

export interface LangServerInstance {
  key: string;
  proxy?: string;
  running: boolean;
}

export interface LangServerSnapshot {
  running: boolean;
  port: number;
  instances: LangServerInstance[];
}

export interface CacheSnapshot {
  size: number;
  maxSize: number;
  hits: number;
  misses: number;
  hitRate: string;
}

export interface SystemSnapshot {
  os: string;
  cpu: { percent: number; cores: number };
  memory: {
    totalBytes: number;
    usedBytes: number;
    availableBytes: number;
    percent: number;
  };
  swap: { totalBytes: number; usedBytes: number; percent: number };
  network: {
    rxBytesPerSec: number;
    txBytesPerSec: number;
    rxBytesTotal: number;
    txBytesTotal: number;
  };
  load: { min1: number; min5: number; min15: number };
}

export interface TokenTotals {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  costUsd: number;
}

export interface ModelAccessSummary {
  total: number;
  allowed: number;
  mode: ModelAccessMode;
}

export interface OverviewPayload {
  uptime: number;
  startedAt: number;
  accounts: AccountCounts;
  authenticated: boolean;
  langServer: LangServerSnapshot;
  totalRequests: number;
  successCount: number;
  errorCount: number;
  successRate: string;
  cache: CacheSnapshot;
  system: SystemSnapshot;
  tokens: TokenTotals;
  // Histogram of upstream HTTP status codes. "0" = transport/no-status bucket.
  upstreamStatus: Record<string, number>;
  modelAccess: ModelAccessSummary;
  version: string;
}

export type AccountTier = 'pro' | 'free' | 'expired' | 'unknown' | '';
export type AccountStatus = 'active' | 'error' | 'expired' | 'invalid' | 'disabled';

export interface AccountCapability {
  modelKey: string;
  ok: boolean;
  latency?: number;
  error?: string;
}

// Credits mirrors internal/auth/pool.go `type Credits`. Not usage counters —
// GetUserStatus returns percentage-consumed for the daily + weekly quotas,
// plus the unix epoch seconds when each resets.
export interface AccountCredits {
  planName?: string;
  dailyPercent?: number;
  weeklyPercent?: number;
  dailyResetAt?: number;
  weeklyResetAt?: number;
  fetchedAt?: number;
  lastError?: string;
}

export interface AccountRow {
  id: string;
  email: string;
  method: string;
  status: AccountStatus;
  addedAt: string;
  keyPrefix: string;
  tier: AccountTier;
  tierManual?: boolean;
  capabilities?: AccountCapability[];
  lastProbed?: number;
  credits?: AccountCredits;
  blockedModels: string[];
  // Server side used to inline availableModels / tierModels on every row;
  // with 30+ accounts that produces ~7k duplicate model strings per fetch.
  // Now we ship only counts, and the actual lists live under the shared
  // tierModels index on the response root (AccountsResponse).
  availableCount: number;
  tierModelCount: number;
  rpmUsed: number;
  rpmLimit: number;
  rateLimited: boolean;
  rateLimitedUntil: number;
  rateLimitedStarted?: number;
  // rateLimitedModels maps model key → unix-ms deadline. Populated when the
  // pool has an active per-model rate-limit on this account; backend filters
  // out expired entries before serialisation. `rateLimitedModelStarts` is the
  // matching map of when each window opened so the UI can render the full
  // "封禁开始 — 解除" span across restarts.
  rateLimitedModels?: Record<string, number>;
  rateLimitedModelStarts?: Record<string, number>;
}

export interface AccountsResponse {
  accounts: AccountRow[];
  // Tier → full model list, shared by every account in `accounts`. When a
  // page needs the actual names it looks up by row.tier and subtracts
  // row.blockedModels locally.
  tierModels: Record<string, string[]>;
}

export interface AccountPatch {
  status?: AccountStatus;
  label?: string;
  resetErrors?: boolean;
  blockedModels?: string[];
  tier?: AccountTier;
}

export interface AddAccountBody {
  api_key?: string;
  token?: string;
  label?: string;
}

export interface TierAccess {
  free: string[];
  pro: string[];
  unknown: string[];
  expired: string[];
  allModels: string[];
}

export interface ModelInfo {
  id: string;
  name: string;
  provider: string;
}

export interface CatalogModel {
  id: string;
  name: string;
  display: string;
  score: number;
}

export interface CatalogGroup {
  name: string;
  count: number;
  topScore: number;
  models: CatalogModel[];
}

export interface CatalogResponse {
  groups: CatalogGroup[];
}

export type ModelAccessMode = 'all' | 'allowlist' | 'blocklist';

export interface ModelAccessConfig {
  mode: ModelAccessMode;
  list: string[];
}

export interface ProxySpec {
  type?: 'http' | 'https' | 'socks5';
  host?: string;
  port?: number;
  user?: string;
  pass?: string;
}

export interface ProxyConfig {
  global?: ProxySpec | null;
  accounts: Record<string, ProxySpec>;
}

// Matches internal/stats/stats.go Snapshot verbatim. Buckets are keyed by
// "YYYY-MM-DDTHH" strings; model/account breakdowns are maps rather than
// arrays. Success counts are derived (requests - errors) since the backend
// only tracks requests + errors at the bucket level.

export interface StatsHourBucket {
  hour: string;
  requests: number;
  errors: number;
}

export interface StatsModelCounts {
  requests: number;
  success: number;
  errors: number;
  totalMs: number;
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
  // First / last request telemetry — `*At` are unix-ms timestamps; `*Ms`
  // are that specific call's duration in milliseconds. Zero means "not yet
  // recorded" (fresh stats or model never used).
  firstAt?: number;
  firstMs?: number;
  lastAt?: number;
  lastMs?: number;
}

export interface StatsAccountCounts {
  requests: number;
  success: number;
  errors: number;
}

export interface StatsPayload {
  startedAt: number;
  totalRequests: number;
  successCount: number;
  errorCount: number;
  modelCounts: Record<string, StatsModelCounts>;
  accountCounts: Record<string, StatsAccountCounts>;
  hourlyBuckets: StatsHourBucket[];
}

export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

export interface LogEntry {
  ts: number;
  level: LogLevel;
  msg: string;
}

export interface LogsListResponse {
  logs: LogEntry[];
}

export interface ExperimentalFlags {
  cascadeConversationReuse: boolean;
  modelIdentityPrompt: boolean;
  preflightRateLimit?: boolean;
  [key: string]: boolean | undefined;
}

export interface ConversationPoolSnapshot {
  size: number;
  hits?: number;
  misses?: number;
}

export interface ExperimentalResponse {
  flags: ExperimentalFlags;
  conversationPool: ConversationPoolSnapshot;
}

export interface IdentityPromptMap {
  [provider: string]: string;
}

export interface IdentityPromptsResponse {
  prompts: IdentityPromptMap;
  defaults: IdentityPromptMap;
}

export interface SelfUpdateStatus {
  ok: boolean;
  behind?: boolean;
  commit?: string;
  remoteCommit?: string;
  branch?: string;
  localMessage?: string;
  remoteMessage?: string;
  error?: string;
}

export interface SelfUpdateResult {
  ok: boolean;
  changed?: boolean;
  dirty?: boolean;
  dirtyFiles?: string[];
  pullOutput?: string;
  before?: string;
  after?: string;
  error?: string;
}

export interface WindsurfLoginBody {
  email: string;
  password: string;
  autoAdd?: boolean;
  proxy?: ProxySpec | null;
}

export interface WindsurfLoginResult {
  success: boolean;
  apiKey?: string;
  email?: string;
  name?: string;
  apiServerUrl?: string;
  account?: { id: string; email: string; status: AccountStatus };
  error?: string;
  isAuthFail?: boolean;
  firebaseCode?: string;
}

export interface OAuthLoginBody {
  idToken: string;
  refreshToken?: string;
  email?: string;
  provider?: string;
  autoAdd?: boolean;
}

export interface ProbeResult {
  id: string;
  email: string;
  tier?: AccountTier;
  error?: string;
}

export interface RateLimitResult {
  success: boolean;
  account: string;
  hasCapacity: boolean;
  messagesRemaining: number;
  maxMessages: number;
}

export interface RefreshCreditsResult {
  id: string;
  email: string;
  OK: boolean;
  error?: string;
  credits?: AccountCredits;
}

// Matches go/internal/dashapi/dashapi.go testProxy() verbatim. Note
// the backend reads auth as `username` / `password` (not `user` / `pass`
// like the ProxySpec we persist on account records) and returns the result
// with `egressIp` + `latencyMs`.
export interface TestProxyBody {
  type?: string;
  host?: string;
  port?: number;
  username?: string;
  password?: string;
}

export interface TestProxyResult {
  ok: boolean;
  egressIp?: string;
  type?: string;
  latencyMs?: number;
  error?: string;
}
