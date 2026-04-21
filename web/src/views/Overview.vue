<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { Button, Card, Modal, Tag, Spin, Popconfirm, Descriptions, DescriptionsItem, message } from 'ant-design-vue';
import {
  ReloadOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  ClockCircleOutlined,
  DatabaseOutlined,
  HddOutlined,
  ClearOutlined,
  SettingOutlined,
  DashboardOutlined,
  AreaChartOutlined,
  CodeOutlined,
  NumberOutlined,
  DollarOutlined,
  SafetyOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  AppstoreOutlined,
  TagOutlined,
} from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import MetricCard from '@/components/MetricCard.vue';
import MarqueeText from '@/components/MarqueeText.vue';
import { getOverview, restartLanguageServer } from '@/api/overview';
import { clearCache, getConfig } from '@/api/selfUpdate';
import { listModelCatalog } from '@/api/models';
import { displayModelName } from '@/utils/modelName';
import type { CatalogGroup, OverviewPayload } from '@/api/types';
import { toast } from '@/api/request';

interface ServerConfig {
  port: number;
  defaultModel: string;
  maxTokens: number;
  logLevel: string;
  lsBinaryPath: string;
  lsPort: number;
  codeiumApiUrl: string;
  hasApiKey: boolean;
  hasDashboardPassword: boolean;
}

const loading = ref(true);
const data = ref<OverviewPayload | null>(null);
const config = ref<ServerConfig | null>(null);
const catalog = ref<CatalogGroup[]>([]);
const pollTimer = ref<number | null>(null);

async function load(): Promise<void> {
  try {
    data.value = await getOverview();
  } catch (err) {
    toast(err, '加载概览失败');
  } finally {
    loading.value = false;
  }
}

async function loadConfig(): Promise<void> {
  try {
    config.value = await getConfig();
  } catch {
    // Non-critical; config panel just stays hidden if the call fails.
  }
}

async function loadCatalog(): Promise<void> {
  try {
    const res = await listModelCatalog();
    catalog.value = res.groups;
  } catch {
    // Non-critical; the catalog card just stays empty.
  }
}

// Score → tone for the capability badge. Bands are deliberately coarse so
// reorderings within a family are visible without sharp colour flips.
function scoreTone(score: number): string {
  if (score >= 90) return 'primary';
  if (score >= 80) return 'success';
  if (score >= 70) return 'info';
  if (score >= 60) return 'warning';
  return 'muted';
}

async function onClearCache(): Promise<void> {
  try {
    await clearCache();
    await load();
    message.success('响应缓存已清空');
  } catch (err) {
    toast(err, '清空失败');
  }
}

function formatUptime(seconds: number): string {
  if (!seconds || seconds < 0) return '-';
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d) return `${d}天 ${h}小时`;
  if (h) return `${h}小时 ${m}分`;
  return `${m}分钟`;
}

function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)} ${units[i]}`;
}

function fmtBps(n: number): string {
  return `${fmtBytes(n)}/s`;
}

function fmtTokens(n: number): string {
  if (!n) return '0';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function fmtUSD(n: number): string {
  if (!n) return '$0.00';
  if (n < 0.01) return '<$0.01';
  if (n < 1) return `$${n.toFixed(4)}`;
  if (n < 1000) return `$${n.toFixed(2)}`;
  return `$${(n / 1000).toFixed(2)}K`;
}

// Percent → metric tone. High CPU/mem/swap paints the card red so the
// operator notices before the host falls over.
function usageTone(p: number | undefined): 'default' | 'success' | 'warning' | 'danger' {
  if (p === undefined || p === null) return 'default';
  if (p >= 90) return 'danger';
  if (p >= 70) return 'warning';
  return 'success';
}

// 上游 HTTP 状态码 → 中文原因短语。只列出真正从 Windsurf 上游观测到的代码；
// 范围兜底按 4xx/5xx 类别给一个通用标签，避免 dashboard 上出现一串裸数字。
const statusReason: Record<string, string> = {
  '0': '传输错误',
  '200': '正常',
  '201': '已创建',
  '204': '无内容',
  '301': '永久重定向',
  '302': '临时重定向',
  '304': '未修改',
  '400': '请求错误',
  '401': '未授权',
  '403': '拒绝访问',
  '404': '未找到',
  '408': '请求超时',
  '409': '资源冲突',
  '413': '负载过大',
  '422': '参数无效',
  '429': '限流',
  '499': '客户端中断',
  '500': '上游内部错误',
  '501': '未实现',
  '502': '网关错误',
  '503': '服务不可用',
  '504': '连接超时',
  '520': 'CDN 未知错误',
  '521': '上游离线',
  '522': '连接超时',
  '524': '上游响应超时',
};

function reasonFor(code: string): string {
  if (statusReason[code]) return statusReason[code];
  const n = Number(code);
  if (!Number.isFinite(n)) return '未知';
  if (n >= 200 && n < 300) return '成功';
  if (n >= 300 && n < 400) return '重定向';
  if (n >= 400 && n < 500) return '客户端错误';
  if (n >= 500 && n < 600) return '服务端错误';
  return '未知';
}

// Sorted + labelled view of the upstream status code histogram. Success codes
// (2xx) render green, 4xx amber, 5xx red, "0" (transport) grey.
interface StatusRow {
  code: string;
  count: number;
  label: string;
  tone: string;
}
const statusRows = computed<StatusRow[]>(() => {
  const raw = data.value?.upstreamStatus ?? {};
  const rows: StatusRow[] = Object.entries(raw).map(([code, count]) => {
    let tone = 'default';
    if (code === '0') {
      tone = 'default';
    } else {
      const n = Number(code);
      if (n >= 200 && n < 300) tone = 'success';
      else if (n >= 300 && n < 400) tone = 'info';
      else if (n >= 400 && n < 500) tone = 'warning';
      else if (n >= 500) tone = 'danger';
    }
    return { code, count, label: reasonFor(code), tone };
  });
  rows.sort((a, b) => b.count - a.count);
  return rows;
});

const totalStatusCount = computed(() =>
  statusRows.value.reduce((sum, r) => sum + r.count, 0),
);

const modelAccessDescription = computed(() => {
  const mode = data.value?.modelAccess?.mode;
  if (mode === 'allowlist') return '允许清单模式';
  if (mode === 'blocklist') return '封锁清单模式';
  return '全部放行';
});

async function onRestartLS(): Promise<void> {
  Modal.confirm({
    title: '重启 Language Server',
    content: '将停止当前所有 LS 实例并重新拉起。进行中的请求会断开重试。',
    okText: '确认重启',
    okType: 'danger',
    async onOk() {
      try {
        await restartLanguageServer();
        message.success('已触发重启');
        setTimeout(load, 2000);
      } catch (err) {
        toast(err, '重启失败');
      }
    },
  });
}

onMounted(() => {
  load();
  loadConfig();
  loadCatalog();
  pollTimer.value = window.setInterval(load, 8000);
});
onUnmounted(() => {
  if (pollTimer.value) window.clearInterval(pollTimer.value);
});
</script>

<template>
  <PageHeader title="仪表盘" subtitle="系统运行状态与关键指标概览" />

  <Spin :spinning="loading && !data">
    <div class="metrics-grid">
      <MetricCard
        tone="success"
        label="活跃账号"
        :value="data?.accounts?.active ?? 0"
        :description="`${data?.accounts?.total ?? 0} 个总账号 · ${data?.accounts?.error ?? 0} 异常`"
      >
        <template #icon><TeamOutlined /></template>
      </MetricCard>
      <MetricCard
        tone="info"
        label="总请求数"
        :value="data?.totalRequests ?? 0"
        :description="`成功率 ${data?.successRate ?? '0.0'}%`"
      >
        <template #icon><ThunderboltOutlined /></template>
      </MetricCard>
      <MetricCard
        label="运行时间"
        :value="formatUptime(data?.uptime ?? 0)"
        :description="
          data?.startedAt
            ? `启动于 ${new Date(data.startedAt).toLocaleString()}`
            : '—'
        "
      >
        <template #icon><ClockCircleOutlined /></template>
      </MetricCard>
      <MetricCard
        :tone="data?.langServer?.running ? 'success' : 'danger'"
        label="Language Server"
        :value="data?.langServer?.running ? '运行中' : '已停止'"
        :description="`${data?.langServer?.instances?.length ?? 0} 个实例 · 端口 ${data?.langServer?.port ?? '-'}`"
      >
        <template #icon><HddOutlined /></template>
      </MetricCard>
      <MetricCard
        tone="info"
        label="响应缓存"
        :value="`${data?.cache?.hitRate ?? '0.0'}%`"
        :description="`${data?.cache?.hits ?? 0} 命中 / ${data?.cache?.misses ?? 0} 未命中 · ${data?.cache?.size ?? 0} 条`"
      >
        <template #icon><DatabaseOutlined /></template>
      </MetricCard>

      <!-- 系统层：CPU / 内存 / SWAP / 网络 / 负载 -->
      <MetricCard
        :tone="usageTone(data?.system?.cpu?.percent)"
        label="CPU 使用率"
        :value="`${(data?.system?.cpu?.percent ?? 0).toFixed(1)}%`"
        :description="`${data?.system?.cpu?.cores ?? 0} 核心`"
      >
        <template #icon><DashboardOutlined /></template>
      </MetricCard>
      <MetricCard
        :tone="usageTone(data?.system?.memory?.percent)"
        label="内存使用率"
        :value="`${(data?.system?.memory?.percent ?? 0).toFixed(1)}%`"
        :description="`${fmtBytes(data?.system?.memory?.usedBytes ?? 0)} / ${fmtBytes(data?.system?.memory?.totalBytes ?? 0)}`"
      >
        <template #icon><AreaChartOutlined /></template>
      </MetricCard>
      <MetricCard
        :tone="data?.system?.swap?.totalBytes ? usageTone(data?.system?.swap?.percent) : 'default'"
        label="SWAP 使用率"
        :value="data?.system?.swap?.totalBytes ? `${(data?.system?.swap?.percent ?? 0).toFixed(1)}%` : '未启用'"
        :description="data?.system?.swap?.totalBytes
          ? `${fmtBytes(data.system.swap.usedBytes)} / ${fmtBytes(data.system.swap.totalBytes)}`
          : '—'"
      >
        <template #icon><CodeOutlined /></template>
      </MetricCard>
      <MetricCard
        label="下行带宽"
        :value="fmtBps(data?.system?.network?.rxBytesPerSec ?? 0)"
        :description="`累计 ${fmtBytes(data?.system?.network?.rxBytesTotal ?? 0)}`"
      >
        <template #icon><ArrowDownOutlined /></template>
      </MetricCard>
      <MetricCard
        label="上行带宽"
        :value="fmtBps(data?.system?.network?.txBytesPerSec ?? 0)"
        :description="`累计 ${fmtBytes(data?.system?.network?.txBytesTotal ?? 0)}`"
      >
        <template #icon><ArrowUpOutlined /></template>
      </MetricCard>
      <MetricCard
        :tone="(data?.system?.load?.min1 ?? 0) > (data?.system?.cpu?.cores ?? 1) ? 'warning' : 'default'"
        label="系统负载"
        :value="(data?.system?.load?.min1 ?? 0).toFixed(2)"
        :description="`5分钟 ${(data?.system?.load?.min5 ?? 0).toFixed(2)} · 15分钟 ${(data?.system?.load?.min15 ?? 0).toFixed(2)}`"
      >
        <template #icon><SafetyOutlined /></template>
      </MetricCard>

      <!-- 业务层：Token + 等价费用 -->
      <MetricCard
        tone="info"
        label="总 Token 消耗"
        :value="fmtTokens(data?.tokens?.totalTokens ?? 0)"
        :description="`输入 ${fmtTokens(data?.tokens?.inputTokens ?? 0)} · 输出 ${fmtTokens(data?.tokens?.outputTokens ?? 0)}`"
      >
        <template #icon><NumberOutlined /></template>
      </MetricCard>
      <MetricCard
        tone="primary"
        label="等价总费用"
        :value="fmtUSD(data?.tokens?.costUsd ?? 0)"
        description="按各模型公开定价折算（仅参考）"
      >
        <template #icon><DollarOutlined /></template>
      </MetricCard>
      <MetricCard
        tone="info"
        label="可用模型数"
        :value="`${data?.modelAccess?.allowed ?? 0} / ${data?.modelAccess?.total ?? 0}`"
        :description="modelAccessDescription"
      >
        <template #icon><AppstoreOutlined /></template>
      </MetricCard>
      <MetricCard
        label="版本号"
        :value="data?.version ?? '—'"
        description="当前运行的服务版本"
      >
        <template #icon><TagOutlined /></template>
      </MetricCard>
    </div>
  </Spin>

  <Card size="small" style="margin-bottom: 16px">
    <template #title>Language Server 实例</template>
    <template #extra>
      <Button size="small" @click="onRestartLS">
        <template #icon><ReloadOutlined /></template>
        重启
      </Button>
    </template>

    <div v-if="!data?.langServer?.instances?.length" class="empty">
      <span class="text-muted">尚无 LS 实例</span>
    </div>
    <div v-else class="ls-grid">
      <div v-for="inst in data.langServer.instances" :key="inst.key" class="ls-card">
        <div class="ls-head">
          <Tag :color="inst.running ? 'success' : 'error'">
            {{ inst.running ? '运行中' : '停止' }}
          </Tag>
          <span class="ls-key">
            {{ inst.key === 'default' ? '默认实例' : inst.key.replace(/^px_/, '') }}
          </span>
        </div>
        <div class="ls-meta">{{ inst.proxy || '无代理' }}</div>
      </div>
    </div>

    <!-- 上游状态码统计 -->
    <div class="status-section">
      <div class="status-header">
        <span class="status-title">上游状态码统计</span>
        <span class="status-hint">{{ totalStatusCount }} 次请求到达上游</span>
      </div>
      <div v-if="!statusRows.length" class="status-empty">
        <span class="text-dim">暂无数据</span>
      </div>
      <div v-else class="status-chips">
        <div
          v-for="row in statusRows"
          :key="row.code"
          class="status-chip"
          :data-tone="row.tone"
        >
          <span class="status-code">{{ row.code === '0' ? '——' : row.code }}</span>
          <span class="status-label">{{ row.label }}</span>
          <span class="status-count">{{ row.count }} 次</span>
        </div>
      </div>
    </div>
  </Card>

  <Card v-if="config" size="small" style="margin-bottom: 16px">
    <template #title>
      <SettingOutlined style="margin-right: 6px" />
      服务端配置
    </template>
    <template #extra>
      <Popconfirm title="清空响应缓存？下一次命中的请求会重新回源。" @confirm="onClearCache">
        <Button size="small" danger>
          <template #icon><ClearOutlined /></template>
          清空响应缓存
        </Button>
      </Popconfirm>
    </template>

    <Descriptions :column="{ xs: 1, sm: 2, md: 3 }" size="small" bordered>
      <DescriptionsItem label="监听端口">
        <code>{{ config.port }}</code>
      </DescriptionsItem>
      <DescriptionsItem label="默认模型">
        {{ displayModelName(config.defaultModel) }}
      </DescriptionsItem>
      <DescriptionsItem label="最大 Token">
        {{ config.maxTokens }}
      </DescriptionsItem>
      <DescriptionsItem label="日志级别">
        <Tag>{{ config.logLevel }}</Tag>
      </DescriptionsItem>
      <DescriptionsItem label="LS 端口">
        <code>{{ config.lsPort }}</code>
      </DescriptionsItem>
      <DescriptionsItem label="LS 二进制">
        <code style="font-size: 11px; word-break: break-all">{{ config.lsBinaryPath }}</code>
      </DescriptionsItem>
      <DescriptionsItem label="API Key 保护">
        <Tag :color="config.hasApiKey ? 'success' : 'warning'">
          {{ config.hasApiKey ? '已启用' : '未启用' }}
        </Tag>
      </DescriptionsItem>
      <DescriptionsItem label="Dashboard 密码">
        <Tag :color="config.hasDashboardPassword ? 'success' : 'warning'">
          {{ config.hasDashboardPassword ? '已启用' : '未启用' }}
        </Tag>
      </DescriptionsItem>
      <DescriptionsItem label="Codeium API">
        <code style="font-size: 11px; word-break: break-all">{{ config.codeiumApiUrl }}</code>
      </DescriptionsItem>
    </Descriptions>
  </Card>

  <Card v-if="catalog.length" size="small" style="margin-bottom: 16px">
    <template #title>
      <AppstoreOutlined style="margin-right: 6px" />
      模型清单
    </template>
    <template #extra>
      <span class="text-muted" style="font-size: 12px">
        按厂商分组 · 按能力总分排序
      </span>
    </template>

    <div class="catalog-grid">
      <section v-for="group in catalog" :key="group.name" class="catalog-group">
        <header class="catalog-group-head">
          <span class="catalog-vendor">{{ group.name }}</span>
          <span class="catalog-meta">
            <span class="catalog-count">{{ group.count }} 款</span>
            <span class="catalog-top" :data-tone="scoreTone(group.topScore)">
              最高 {{ group.topScore }}
            </span>
          </span>
        </header>
        <div class="catalog-rows">
          <div v-for="m in group.models" :key="m.id" class="catalog-row">
            <div class="catalog-names">
              <MarqueeText class="catalog-display" :text="m.display" />
              <MarqueeText class="catalog-id" :text="m.id" />
            </div>
            <span class="catalog-score" :data-tone="scoreTone(m.score)">{{ m.score }}</span>
          </div>
        </div>
      </section>
    </div>
  </Card>
</template>

<style scoped>
.empty {
  padding: 12px 0;
  text-align: center;
}
.ls-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}
.ls-card {
  padding: 12px 14px;
  background: var(--color-surface-soft);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}
.ls-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
.ls-key {
  font-weight: 600;
  font-size: 13px;
}
.ls-meta {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-dim);
}

.status-section {
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--color-border);
}
.status-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  margin-bottom: 10px;
}
.status-title {
  font-weight: 600;
  font-size: 13px;
  color: var(--color-text);
}
.status-hint {
  font-size: 12px;
  color: var(--color-text-muted);
}
.status-empty {
  padding: 12px 0;
  text-align: center;
}
.status-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.status-chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 5px 11px;
  border-radius: var(--radius-sm);
  font-size: 12px;
  line-height: 1.4;
  border: 1px solid var(--color-border);
  background: var(--color-surface);
}
.status-chip[data-tone='success'] {
  background: var(--color-success-soft);
  border-color: rgba(21, 128, 61, 0.2);
}
.status-chip[data-tone='warning'] {
  background: var(--color-warning-soft);
  border-color: rgba(180, 83, 9, 0.2);
}
.status-chip[data-tone='danger'] {
  background: var(--color-danger-soft);
  border-color: rgba(185, 28, 28, 0.2);
}
.status-chip[data-tone='info'] {
  background: var(--color-info-soft);
  border-color: rgba(29, 78, 216, 0.2);
}
.status-code {
  font-family: var(--font-mono);
  font-weight: 700;
}
.status-label {
  color: var(--color-text-muted);
}
.status-count {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  color: var(--color-text);
}

.catalog-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 14px;
}
.catalog-group {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-soft);
  padding: 12px 14px;
}
.catalog-group-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  padding-bottom: 10px;
  margin-bottom: 10px;
  border-bottom: 1px solid var(--color-border);
}
.catalog-vendor {
  font-weight: 700;
  font-size: 14px;
  color: var(--color-text);
}
.catalog-meta {
  display: inline-flex;
  align-items: baseline;
  gap: 10px;
  font-size: 12px;
}
.catalog-count {
  color: var(--color-text-muted);
}
.catalog-top {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  color: var(--color-text);
}
.catalog-rows {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.catalog-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-surface);
  border: 1px solid var(--color-border);
}
.catalog-names {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1;
}
.catalog-display {
  font-weight: 600;
  font-size: 13px;
  color: var(--color-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.catalog-id {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.catalog-score {
  flex-shrink: 0;
  font-weight: 700;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  min-width: 42px;
  text-align: center;
  padding: 3px 10px;
  border-radius: 999px;
  border: 1px solid var(--color-border);
  background: var(--color-surface-soft);
}
.catalog-score[data-tone='primary'],
.catalog-top[data-tone='primary'] {
  background: var(--color-primary-soft, rgba(79, 70, 229, 0.12));
  border-color: rgba(79, 70, 229, 0.3);
  color: var(--color-primary, #4f46e5);
}
.catalog-score[data-tone='success'],
.catalog-top[data-tone='success'] {
  background: var(--color-success-soft);
  border-color: rgba(21, 128, 61, 0.3);
  color: var(--color-success, #15803d);
}
.catalog-score[data-tone='info'],
.catalog-top[data-tone='info'] {
  background: var(--color-info-soft);
  border-color: rgba(29, 78, 216, 0.3);
  color: var(--color-info, #1d4ed8);
}
.catalog-score[data-tone='warning'],
.catalog-top[data-tone='warning'] {
  background: var(--color-warning-soft);
  border-color: rgba(180, 83, 9, 0.3);
  color: var(--color-warning, #b45309);
}
</style>
