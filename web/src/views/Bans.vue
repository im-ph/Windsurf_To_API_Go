<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { Button, Card, Table, Tag, Popconfirm, message } from 'ant-design-vue';
import { ReloadOutlined, ThunderboltOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import MetricCard from '@/components/MetricCard.vue';
import { listAccounts, patchAccount } from '@/api/accounts';
import type { AccountRow } from '@/api/types';
import { toast } from '@/api/request';

const accounts = ref<AccountRow[]>([]);
const loading = ref(false);
const tick = ref(0);
let tickTimer: number | null = null;

async function load(): Promise<void> {
  loading.value = true;
  try {
    const r = await listAccounts();
    accounts.value = r.accounts ?? [];
  } catch (err) {
    toast(err, '加载失败');
  } finally {
    loading.value = false;
  }
}

// 1-second tick drives the countdown text; 10-second refetch lets the
// server's automatic lift (when the rate-limit window expires inside
// Acquire) reach this view without the user hitting 刷新 themselves.
onMounted(() => {
  load();
  tickTimer = window.setInterval(() => {
    tick.value++;
    if (tick.value % 10 === 0 && !document.hidden) load();
  }, 1000);
});
onUnmounted(() => {
  if (tickTimer) window.clearInterval(tickTimer);
});

// Severity — problem rows bubble up before healthy ones.
function severityScore(a: AccountRow): number {
  if (a.status === 'error') return 50;
  if (a.status === 'expired') return 40;
  if (a.status === 'invalid') return 35;
  if (a.rateLimited) return 30;
  if (a.status === 'disabled') return 10;
  return 0;
}

const sortedAccounts = computed(() =>
  [...accounts.value].sort((x, y) => severityScore(y) - severityScore(x)),
);

const counts = computed(() => ({
  error: accounts.value.filter((a) => a.status === 'error').length,
  expired: accounts.value.filter((a) => a.status === 'expired').length,
  rateLimited: accounts.value.filter((a) => a.rateLimited).length,
  throttled: accounts.value.filter(
    (a) => Object.keys(a.rateLimitedModels ?? {}).length > 0,
  ).length,
  disabled: accounts.value.filter((a) => a.status === 'disabled').length,
}));

async function onReset(id: string): Promise<void> {
  try {
    await patchAccount(id, { status: 'active', resetErrors: true });
    message.success('已恢复');
    await load();
  } catch (err) {
    toast(err, '操作失败');
  }
}

// ─── Display helpers ──────────────────────────────────────

function statusColor(status: string): string {
  switch (status) {
    case 'active':
      return 'success';
    case 'error':
      return 'error';
    case 'expired':
      return 'warning';
    case 'disabled':
      return 'default';
    default:
      return 'default';
  }
}

// 账号状态中文化。保留英文 API 常量，仅展示层翻译。
function statusLabel(status: string): string {
  switch (status) {
    case 'active':
      return '正常';
    case 'error':
      return '错误';
    case 'expired':
      return '已过期';
    case 'invalid':
      return '无效';
    case 'disabled':
      return '已停用';
    default:
      return status || '—';
  }
}

// When a model is officially throttled we override the visible status to
// "限速" so the operator doesn't read "正常" for a row that's actually
// sitting out of the pool for that one model.
function displayStatus(a: { rateLimited?: boolean; status?: string }): { label: string; color: string } {
  const status = a.status ?? '';
  if (a.rateLimited && status === 'active') return { label: '限速', color: 'warning' };
  return { label: statusLabel(status), color: statusColor(status) };
}

// Render rate-limit release time in Beijing time regardless of viewer
// locale — the account's server clock is what matters, not the browser's.
function fmtBeijing(ts: number): string {
  if (!ts) return '';
  return new Date(ts).toLocaleString('zh-CN', {
    timeZone: 'Asia/Shanghai',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

function countdown(ts: number): string {
  void tick.value; // force 1s re-eval via template dependency
  const remain = ts - Date.now();
  if (remain <= 0) return '即将解除';
  const mins = Math.floor(remain / 60_000);
  const secs = Math.floor((remain % 60_000) / 1000);
  if (mins >= 1) return `剩余 ${mins}m${secs.toString().padStart(2, '0')}s`;
  return `剩余 ${secs}s`;
}

interface RateEntry {
  kind: 'account' | 'model';
  model?: string;
  started: number;
  until: number;
}

function rateEntries(a: {
  rateLimitedUntil?: number;
  rateLimitedStarted?: number;
  rateLimitedModels?: Record<string, number>;
  rateLimitedModelStarts?: Record<string, number>;
}): RateEntry[] {
  const out: RateEntry[] = [];
  if (a.rateLimitedUntil && a.rateLimitedUntil > Date.now()) {
    out.push({
      kind: 'account',
      started: a.rateLimitedStarted ?? 0,
      until: a.rateLimitedUntil,
    });
  }
  const byModel = a.rateLimitedModels ?? {};
  const startedByModel = a.rateLimitedModelStarts ?? {};
  for (const [model, until] of Object.entries(byModel)) {
    if (until > Date.now()) {
      out.push({
        kind: 'model',
        model,
        started: startedByModel[model] ?? 0,
        until,
      });
    }
  }
  out.sort((x, y) => x.until - y.until);
  return out;
}

// Columns — 限流 is the original "rate-limited (any)" summary flag, kept
// unchanged. 限速 is new, listing per-model official throttles with their
// release times so the operator can see exactly what's sitting out.
const columns = [
  { title: '账号', dataIndex: 'email', ellipsis: true },
  { title: '状态', dataIndex: 'status', width: 110 },
  { title: '层级', dataIndex: 'tier', width: 80 },
  { title: '限流', dataIndex: 'rateLimited', width: 90 },
  { title: '限速', dataIndex: 'rateLimit', width: 300 },
  { title: '最近探测', dataIndex: 'lastProbed', width: 170 },
  { title: '操作', dataIndex: 'actions', width: 380 },
];
</script>

<template>
  <PageHeader title="异常监测" subtitle="追踪错误账号与异常状态">
    <template #actions>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
    </template>
  </PageHeader>

  <div class="metrics-grid">
    <MetricCard tone="danger" label="错误账号" :value="counts.error" />
    <MetricCard tone="warning" label="过期账号" :value="counts.expired" />
    <MetricCard tone="warning" label="限流中" :value="counts.rateLimited" />
    <MetricCard tone="warning" label="限速中" :value="counts.throttled" />
    <MetricCard tone="default" label="已停用" :value="counts.disabled" />
  </div>

  <Card size="small" :body-style="{ padding: 0 }">
    <template #title>
      账号健康状况
      <span class="count-hint">共 {{ accounts.length }} 个 · 有问题的在前</span>
    </template>
    <Table
      :data-source="sortedAccounts"
      :columns="columns"
      :loading="loading"
      :pagination="false"
      row-key="id"
      size="middle"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.dataIndex === 'status'">
          <Tag :color="displayStatus(record).color">{{ displayStatus(record).label }}</Tag>
        </template>
        <template v-else-if="column.dataIndex === 'rateLimited'">
          <Tag v-if="record.rateLimited" color="warning">
            <ThunderboltOutlined />
            限流
          </Tag>
          <span v-else class="text-dim">—</span>
        </template>
        <template v-else-if="column.dataIndex === 'rateLimit'">
          <template v-if="rateEntries(record).length === 0">
            <span class="text-dim">—</span>
          </template>
          <div v-else class="rate-stack">
            <div v-for="(e, i) in rateEntries(record)" :key="i" class="rate-pill">
              <ThunderboltOutlined class="rate-icon" />
              <span v-if="e.kind === 'account'" class="rate-scope account">全账号</span>
              <code v-else class="rate-scope model">{{ e.model }}</code>
              <span class="rate-count">{{ countdown(e.until) }}</span>
            </div>
          </div>
        </template>
        <template v-else-if="column.dataIndex === 'lastProbed'">
          <span class="text-mono">
            {{ record.lastProbed ? new Date(record.lastProbed).toLocaleString() : '—' }}
          </span>
        </template>
        <template v-else-if="column.dataIndex === 'actions'">
          <template v-if="rateEntries(record).length > 0">
            <div class="rate-note">
              <div v-for="(e, i) in rateEntries(record)" :key="i">
                <template v-if="e.kind === 'model'">
                  <code>{{ e.model }}</code> 被官方限速，
                  <span v-if="e.started" class="when">
                    北京时间 {{ fmtBeijing(e.started) }}
                  </span>
                  <template v-if="e.started">开始，</template>
                  <span class="when">北京时间 {{ fmtBeijing(e.until) }}</span>
                  解除。
                </template>
                <template v-else>
                  账号级限速，
                  <span v-if="e.started" class="when">
                    北京时间 {{ fmtBeijing(e.started) }}
                  </span>
                  <template v-if="e.started">开始，</template>
                  <span class="when">北京时间 {{ fmtBeijing(e.until) }}</span>
                  解除。
                </template>
              </div>
              <div class="rate-note-hint">到期后自动放回号池，无需手动操作。</div>
            </div>
          </template>
          <Popconfirm
            v-else-if="record.status !== 'active'"
            title="将账号状态重置为 active？"
            @confirm="onReset(record.id)"
          >
            <Button size="small">恢复</Button>
          </Popconfirm>
          <span v-else class="text-dim">—</span>
        </template>
      </template>
    </Table>
  </Card>
</template>

<style scoped>
.count-hint {
  font-size: 12px;
  font-weight: 400;
  color: var(--color-text-muted);
  margin-left: 10px;
}

.rate-stack {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.rate-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: var(--color-warning-soft);
  border: 1px solid rgba(180, 83, 9, 0.2);
  border-radius: var(--radius-sm);
  padding: 2px 8px;
  font-size: 12px;
  line-height: 1.5;
  width: fit-content;
  max-width: 100%;
}
.rate-icon {
  color: var(--color-warning);
  font-size: 11px;
}
.rate-scope {
  font-weight: 600;
}
.rate-scope.account {
  color: var(--color-danger);
}
.rate-scope.model {
  color: var(--color-warning);
  font-family: var(--font-mono);
  font-size: 11px;
  background: transparent;
  padding: 0;
  border: none;
}
.rate-count {
  color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}

.rate-note {
  font-size: 12px;
  line-height: 1.6;
  color: var(--color-text);
}
.rate-note code {
  font-family: var(--font-mono);
  background: var(--color-surface-alt);
  padding: 1px 5px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--color-warning);
  font-weight: 600;
}
.rate-note .when {
  font-weight: 600;
  color: var(--color-text);
}
.rate-note-hint {
  color: var(--color-text-dim);
  font-size: 11px;
  margin-top: 4px;
}
</style>
