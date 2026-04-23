<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { Button, Card, Popconfirm, RadioGroup, Table, Empty, Spin, message } from 'ant-design-vue';
import { ReloadOutlined, ClearOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import MetricCard from '@/components/MetricCard.vue';
import { getStats, resetStats } from '@/api/stats';
import { listAccounts } from '@/api/accounts';
import type { StatsPayload, AccountRow } from '@/api/types';
import { toast } from '@/api/request';
import { displayModelName } from '@/utils/modelName';

const loading = ref(true);
const data = ref<StatsPayload | null>(null);
const accounts = ref<AccountRow[]>([]);
const range = ref<6 | 24 | 72>(24);
const timer = ref<number | null>(null);

async function load(): Promise<void> {
  try {
    // Fetch stats + accounts in parallel. We join on account id so the
    // account-dimension table can show emails instead of opaque 8-char
    // hex — previously the column was useless to operators.
    const [s, a] = await Promise.all([getStats(), listAccounts().catch(() => ({ accounts: [] }))]);
    data.value = s;
    accounts.value = a.accounts ?? [];
  } catch (err) {
    toast(err, '加载统计失败');
  } finally {
    loading.value = false;
  }
}

async function onReset(): Promise<void> {
  try {
    await resetStats();
    message.success('统计已重置');
    await load();
  } catch (err) {
    toast(err, '重置失败');
  }
}

interface BucketRow {
  key: string;
  label: string;
  requests: number;
  errors: number;
  success: number;
}

const buckets = computed<BucketRow[]>(() => {
  const all = data.value?.hourlyBuckets ?? [];
  return all.slice(-range.value).map((b) => ({
    key: b.hour,
    label: hourLabel(b.hour),
    requests: b.requests,
    errors: b.errors,
    success: Math.max(0, b.requests - b.errors),
  }));
});

const maxCount = computed(() => Math.max(1, ...buckets.value.map((b) => b.requests)));

const successRate = computed(() => {
  const total = data.value?.totalRequests ?? 0;
  if (!total) return '0.0';
  return (((data.value?.successCount ?? 0) / total) * 100).toFixed(1);
});

// ms 值渲染成 "1.23 s / 456 ms" —— 后端 avgMs/p50Ms/p95Ms 单位都是毫秒，
// 但裸数字（`1234`）完全看不出量纲；秒级的数据格式化成 s，亚秒级保留 ms。
function fmtMs(n: number | null | undefined): string {
  if (n === null || n === undefined || n < 0) return '—';
  if (!n) return '0 ms';
  if (n >= 10_000) return `${(n / 1000).toFixed(1)} s`;
  if (n >= 1_000) return `${(n / 1000).toFixed(2)} s`;
  return `${Math.round(n)} ms`;
}

// unix-ms → 本地时区 "MM-DD HH:mm"，空值显示 "—"
function fmtTs(n: number | null | undefined): string {
  if (!n || n <= 0) return '—';
  const d = new Date(n);
  const pad = (v: number) => v.toString().padStart(2, '0');
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// 延迟列的分位数解释：
//   中位延迟  = p50，50% 的请求比这个值快
//   尾部延迟  = p95，仅有 5% 的请求比这个值慢（用来发现拖尾慢请求）
const modelColumns = [
  { title: '模型', dataIndex: 'model', ellipsis: true, fixed: 'left' as const },
  {
    title: '首次请求时间',
    dataIndex: 'firstAt',
    width: 120,
    customRender: ({ value }: { value: number }) => fmtTs(value),
  },
  {
    title: '首次请求耗时',
    dataIndex: 'firstMs',
    width: 120,
    customRender: ({ value }: { value: number }) => fmtMs(value),
  },
  {
    title: '末次请求时间',
    dataIndex: 'lastAt',
    width: 120,
    customRender: ({ value }: { value: number }) => fmtTs(value),
  },
  {
    title: '末次请求耗时',
    dataIndex: 'lastMs',
    width: 120,
    customRender: ({ value }: { value: number }) => fmtMs(value),
  },
  { title: '请求', dataIndex: 'requests', width: 80 },
  { title: '成功', dataIndex: 'success', width: 80 },
  { title: '错误', dataIndex: 'errors', width: 80 },
  { title: '成功率', dataIndex: 'rate', width: 90 },
  {
    title: '平均耗时',
    dataIndex: 'avgMs',
    width: 110,
    customRender: ({ value }: { value: number }) => fmtMs(value),
  },
  {
    title: '中位延迟',
    dataIndex: 'p50Ms',
    width: 110,
    customHeaderCell: () => ({ title: 'p50 · 50% 的请求不超过此耗时' }),
    customRender: ({ value }: { value: number }) => fmtMs(value),
  },
  {
    title: '尾部延迟',
    dataIndex: 'p95Ms',
    width: 110,
    customHeaderCell: () => ({ title: 'p95 · 95% 的请求不超过此耗时（反映慢请求）' }),
    customRender: ({ value }: { value: number }) => fmtMs(value),
  },
];

const modelRows = computed(() =>
  Object.entries(data.value?.modelCounts ?? {})
    .map(([id, v]) => ({
      model: displayModelName(id),
      modelId: id,
      firstAt: v.firstAt ?? 0,
      firstMs: v.firstMs ?? 0,
      lastAt: v.lastAt ?? 0,
      lastMs: v.lastMs ?? 0,
      requests: v.requests,
      success: v.success,
      errors: v.errors,
      avgMs: v.avgMs,
      p50Ms: v.p50Ms,
      p95Ms: v.p95Ms,
      rate: v.requests > 0 ? `${((v.success / v.requests) * 100).toFixed(1)}%` : '—',
    }))
    .sort((a, b) => b.requests - a.requests),
);

const accountColumns = [
  { title: '账号 ID', dataIndex: 'id', width: 120 },
  { title: '邮箱 / 标签', dataIndex: 'email', ellipsis: true },
  { title: '请求', dataIndex: 'requests', width: 100 },
  { title: '成功', dataIndex: 'success', width: 100 },
  { title: '错误', dataIndex: 'errors', width: 100 },
  { title: '成功率', dataIndex: 'rate', width: 120 },
];

// Account IDs are 8-char hex generated by auth.newID(); stats.go records
// under acct.ID (not acct.APIKey), so the key here is the real ID shown
// in 账号管理. Join against the accounts list to surface email / tier —
// operators care about "which account was most active", not the opaque
// hex. Entries with no matching account (rare — only if an account was
// deleted since the stats were recorded) render the bare ID + "—".
const accountRows = computed(() => {
  const byId: Record<string, AccountRow> = {};
  for (const a of accounts.value) byId[a.id] = a;
  return Object.entries(data.value?.accountCounts ?? {})
    .map(([id, v]) => {
      const a = byId[id];
      return {
        id,
        email: a?.email ?? '—（账号已删除）',
        requests: v.requests,
        success: v.success,
        errors: v.errors,
        rate: v.requests > 0 ? `${((v.success / v.requests) * 100).toFixed(1)}%` : '—',
      };
    })
    .sort((x, y) => y.requests - x.requests);
});

// Bucket "hour" keys are RFC3339 UTC strings (e.g. "2026-04-21T01:00:00Z"
// — server uses time.Time.Format(time.RFC3339)). Render as the viewer's
// local hour so "06:00" means 06:00 to them, not UTC 06:00.
function hourLabel(h: string): string {
  if (!h) return '';
  const d = new Date(h);
  if (isNaN(d.getTime())) return h;
  return `${String(d.getHours()).padStart(2, '0')}:00`;
}

onMounted(() => {
  load();
  timer.value = window.setInterval(load, 10000);
});
onUnmounted(() => {
  if (timer.value) window.clearInterval(timer.value);
});
</script>

<template>
  <PageHeader title="统计分析" subtitle="请求量、成功率、延迟分位数与账号/模型维度统计">
    <template #actions>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
      <Popconfirm title="重置全部统计数据？" @confirm="onReset">
        <Button danger>
          <template #icon><ClearOutlined /></template>
          重置
        </Button>
      </Popconfirm>
    </template>
  </PageHeader>

  <Spin :spinning="loading && !data">
    <div class="metrics-grid">
      <MetricCard
        tone="info"
        label="总请求"
        :value="data?.totalRequests ?? 0"
        :description="`成功率 ${successRate}%`"
      />
      <MetricCard tone="success" label="成功" :value="data?.successCount ?? 0" />
      <MetricCard tone="danger" label="错误" :value="data?.errorCount ?? 0" />
      <MetricCard
        label="监控窗口"
        :value="`${buckets.length} 小时`"
        :description="range === 72 ? '最长 72 小时滚动窗口' : '1 小时粒度'"
      />
    </div>

    <Card size="small" style="margin-bottom: 16px">
      <template #title>请求量时间序列</template>
      <template #extra>
        <RadioGroup
          v-model:value="range"
          size="small"
          option-type="button"
          button-style="solid"
          :options="[
            { label: '近 6 小时', value: 6 },
            { label: '近 24 小时', value: 24 },
            { label: '近 72 小时', value: 72 },
          ]"
        />
      </template>

      <Empty v-if="!buckets.length" description="暂无数据" />
      <div v-else class="bars">
        <div
          v-for="b in buckets"
          :key="b.key"
          class="bar-col"
          :title="`${b.label} · 请求 ${b.requests} · 错误 ${b.errors}`"
        >
          <div
            class="bar"
            :class="{ danger: b.errors > 0 }"
            :style="{ height: `${(b.requests / maxCount) * 100}%` }"
          />
          <div class="bar-label">{{ b.label }}</div>
        </div>
      </div>
    </Card>

    <Card size="small" :body-style="{ padding: 0 }" style="margin-bottom: 16px">
      <template #title>模型使用统计</template>
      <Table
        :data-source="modelRows"
        :columns="modelColumns"
        :pagination="false"
        row-key="modelId"
        size="middle"
      />
    </Card>

    <Card size="small" :body-style="{ padding: 0 }">
      <template #title>账号维度统计</template>
      <Table
        :data-source="accountRows"
        :columns="accountColumns"
        :pagination="false"
        row-key="id"
        size="middle"
      />
    </Card>
  </Spin>
</template>

<style scoped>
.bars {
  display: flex;
  align-items: flex-end;
  gap: 4px;
  height: 180px;
  padding-top: 16px;
}
.bar-col {
  flex: 1;
  min-width: 10px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-end;
  height: 100%;
  gap: 6px;
}
.bar {
  width: 100%;
  min-height: 2px;
  background: linear-gradient(to top, #4f46e5, #818cf8);
  border-radius: 4px 4px 0 0;
  transition: height 400ms cubic-bezier(0.4, 0, 0.2, 1);
}
.bar.danger {
  background: linear-gradient(to top, #b91c1c, #f87171);
}
.bar-col:hover .bar {
  filter: brightness(1.1);
}
.bar-label {
  font-size: 10px;
  color: var(--color-text-dim);
  font-family: var(--font-mono);
}
</style>
