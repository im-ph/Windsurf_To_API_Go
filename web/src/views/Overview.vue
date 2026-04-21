<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue';
import { Button, Card, Modal, Tag, Alert, Spin, Popconfirm, Descriptions, DescriptionsItem } from 'ant-design-vue';
import {
  ReloadOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  ClockCircleOutlined,
  DatabaseOutlined,
  HddOutlined,
  CloudDownloadOutlined,
  CloudSyncOutlined,
  ClearOutlined,
  SettingOutlined,
} from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import MetricCard from '@/components/MetricCard.vue';
import { getOverview, restartLanguageServer } from '@/api/overview';
import { applyUpdate, checkUpdate, clearCache, getConfig } from '@/api/selfUpdate';
import type { OverviewPayload, SelfUpdateStatus } from '@/api/types';
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
const pollTimer = ref<number | null>(null);
const checking = ref(false);
const applying = ref(false);
const updateStatus = ref<SelfUpdateStatus | null>(null);
const updateError = ref('');

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

async function onClearCache(): Promise<void> {
  try {
    await clearCache();
    await load();
    const { message: m } = await import('ant-design-vue');
    m.success('响应缓存已清空');
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

async function onCheckUpdate(): Promise<void> {
  checking.value = true;
  updateError.value = '';
  try {
    updateStatus.value = await checkUpdate();
  } catch (err) {
    updateError.value = err instanceof Error ? err.message : '检查失败';
  } finally {
    checking.value = false;
  }
}

async function onApplyUpdate(): Promise<void> {
  Modal.confirm({
    title: '一键更新并重启',
    content: '将执行 git pull 并重启服务。重启期间 dashboard 会短暂不可用（约 5-10 秒）。',
    okText: '更新并重启',
    okType: 'danger',
    async onOk() {
      applying.value = true;
      try {
        let result = await applyUpdate(false);
        if (result.dirty) {
          await new Promise<void>((resolve, reject) => {
            Modal.confirm({
              title: '工作区有本地修改',
              content: `以下文件已被修改但未提交：${(result.dirtyFiles ?? []).slice(0, 10).join('、')}。强制覆盖后将按远程版本拉取。`,
              okText: '强制覆盖并更新',
              okType: 'danger',
              onOk: async () => {
                result = await applyUpdate(true);
                resolve();
              },
              onCancel: () => reject(new Error('已取消')),
            });
          });
        }
        if (!result.ok) throw new Error(result.error ?? '更新失败');
        if (result.changed) {
          updateStatus.value = { ok: true, behind: false, commit: result.after };
          setTimeout(() => location.reload(), 8000);
        }
      } catch (err) {
        toast(err, '更新失败');
      } finally {
        applying.value = false;
      }
    },
  });
}

async function onRestartLS(): Promise<void> {
  Modal.confirm({
    title: '重启 Language Server',
    content: '将停止当前所有 LS 实例并重新拉起。进行中的请求会断开重试。',
    okText: '确认重启',
    okType: 'danger',
    async onOk() {
      try {
        await restartLanguageServer();
        toast({ message: '已触发重启' }, '');
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
  pollTimer.value = window.setInterval(load, 8000);
});
onUnmounted(() => {
  if (pollTimer.value) window.clearInterval(pollTimer.value);
});
</script>

<template>
  <PageHeader title="仪表盘" subtitle="系统运行状态与关键指标概览">
    <template #actions>
      <Button :loading="checking" @click="onCheckUpdate">
        <template #icon><CloudDownloadOutlined /></template>
        检查更新
      </Button>
      <Button
        v-if="updateStatus?.behind"
        type="primary"
        :loading="applying"
        @click="onApplyUpdate"
      >
        <template #icon><CloudSyncOutlined /></template>
        一键更新并重启
      </Button>
    </template>
  </PageHeader>

  <Alert
    v-if="updateError"
    type="error"
    :message="updateError"
    show-icon
    closable
    style="margin-bottom: 16px"
  />
  <Alert
    v-else-if="updateStatus && !updateStatus.behind && updateStatus.ok"
    type="success"
    :message="`已是最新版本${updateStatus.commit ? ` (${updateStatus.commit})` : ''}`"
    show-icon
    closable
    style="margin-bottom: 16px"
  />
  <Alert
    v-else-if="updateStatus?.behind"
    type="info"
    show-icon
    closable
    style="margin-bottom: 16px"
  >
    <template #message>
      发现新版本：<code>{{ updateStatus.commit }}</code> → <code>{{ updateStatus.remoteCommit }}</code>
    </template>
    <template #description>
      {{ updateStatus.remoteMessage }}
    </template>
  </Alert>

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
        <code>{{ config.defaultModel }}</code>
      </DescriptionsItem>
      <DescriptionsItem label="最大 tokens">
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
</style>
