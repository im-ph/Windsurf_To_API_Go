<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue';
import {
  Button,
  Card,
  Input,
  Select,
  SelectOption,
  Table,
  Tag,
  Popconfirm,
  Space,
  message,
} from 'ant-design-vue';
import {
  ReloadOutlined,
  SearchOutlined,
  DeleteOutlined,
  KeyOutlined,
  PlusOutlined,
  SyncOutlined,
  ThunderboltOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  WalletOutlined,
} from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import {
  addAccount,
  checkRateLimit,
  listAccounts,
  patchAccount,
  probeAll,
  probeOne,
  refreshAllCredits,
  refreshAccountToken,
  refreshCredits,
  removeAccount,
} from '@/api/accounts';
import { Modal } from 'ant-design-vue';
import type { AccountRow, AccountTier } from '@/api/types';
import { toast } from '@/api/request';

const loading = ref(false);
const accounts = ref<AccountRow[]>([]);

const form = reactive({
  type: 'token' as 'token' | 'api_key',
  value: '',
  label: '',
});

async function load(): Promise<void> {
  loading.value = true;
  try {
    const r = await listAccounts();
    accounts.value = r.accounts ?? [];
  } catch (err) {
    toast(err, '加载账号失败');
  } finally {
    loading.value = false;
  }
}

async function onAdd(): Promise<void> {
  const val = form.value.trim();
  if (!val) {
    message.warning('请输入 Key 或 Token');
    return;
  }
  try {
    await addAccount({
      [form.type]: val,
      label: form.label.trim() || undefined,
    });
    message.success('已添加账号');
    form.value = '';
    form.label = '';
    await load();
  } catch (err) {
    toast(err, '添加失败');
  }
}

async function onRemove(id: string): Promise<void> {
  try {
    await removeAccount(id);
    message.success('已删除');
    await load();
  } catch (err) {
    toast(err, '删除失败');
  }
}

async function onProbeAll(): Promise<void> {
  try {
    const r = await probeAll();
    message.success(`已探测 ${r.results?.length ?? 0} 个账号`);
    await load();
  } catch (err) {
    toast(err, '探测失败');
  }
}

async function onProbeOne(id: string): Promise<void> {
  try {
    await probeOne(id);
    message.success('探测完成');
    await load();
  } catch (err) {
    toast(err, '探测失败');
  }
}

async function onRefreshAllCredits(): Promise<void> {
  try {
    await refreshAllCredits();
    message.success('已刷新全部余额');
    await load();
  } catch (err) {
    toast(err, '刷新失败');
  }
}

async function onRefreshCredits(id: string): Promise<void> {
  try {
    await refreshCredits(id);
    message.success('已刷新余额');
    await load();
  } catch (err) {
    toast(err, '刷新失败');
  }
}

async function onRefreshTok(id: string): Promise<void> {
  try {
    const r = await refreshAccountToken(id);
    message.success(`已刷新 token${r.keyChanged ? '（API Key 已更新）' : ''}`);
    await load();
  } catch (err) {
    toast(err, '刷新失败');
  }
}

async function onSetTier(id: string, tier: AccountTier): Promise<void> {
  try {
    await patchAccount(id, { tier });
    message.success('层级已更新');
    await load();
  } catch (err) {
    toast(err, '更新失败');
  }
}

async function onToggleDisabled(id: string, currentStatus: string): Promise<void> {
  const disabling = currentStatus !== 'disabled';
  try {
    await patchAccount(id, disabling
      ? { status: 'disabled' }
      : { status: 'active', resetErrors: true });
    message.success(disabling ? '已停用' : '已启用');
    await load();
  } catch (err) {
    toast(err, '操作失败');
  }
}

async function onRateLimit(id: string, email: string): Promise<void> {
  try {
    const r = await checkRateLimit(id);
    // Codeium's CheckMessageRateLimit returns -1 / -1 for Pro accounts —
    // they're not subject to the per-5-minute hard cap that gates Free.
    // Rendering the sentinel literally as "-1 / -1" just confuses users.
    const unlimited = r.messagesRemaining < 0 && r.maxMessages < 0;
    let body: string;
    if (unlimited) {
      body = r.hasCapacity
        ? '✓ 无消息数限流（Pro 套餐不受每 5 分钟硬上限限制）'
        : '✗ 当前被临时限流';
    } else {
      body = r.hasCapacity
        ? `✓ 仍有余量：${r.messagesRemaining} / ${r.maxMessages}`
        : `✗ 已被限流：${r.messagesRemaining} / ${r.maxMessages}`;
    }
    Modal.info({ title: `限流额度 · ${email}`, content: body });
  } catch (err) {
    toast(err, '查询失败');
  }
}

// GetUserStatus reports each quota window as *remaining* percent — direct
// from Codeium's DailyQuotaRemainingPercent / WeeklyQuotaRemainingPercent
// (see go/internal/cloud/cloud.go). 93% means 93% of the window is still
// available, not 93% consumed.

interface CreditsView {
  daily: number | null;
  weekly: number | null;
  hasError: boolean;
  hasData: boolean;
}

function creditsView(row: { credits?: AccountRow['credits'] }): CreditsView {
  const c = row.credits;
  const hasData = !!c && !c.lastError &&
    (typeof c.dailyPercent === 'number' || typeof c.weeklyPercent === 'number');
  return {
    daily: typeof c?.dailyPercent === 'number' ? c.dailyPercent : null,
    weekly: typeof c?.weeklyPercent === 'number' ? c.weeklyPercent : null,
    hasError: !!c?.lastError,
    hasData,
  };
}

// Colour the operator's attention to low remaining: ≤10% red, ≤30% amber,
// otherwise fine. Matches the mental model "how soon do I run out".
function pctClass(p: number | null): string {
  if (p === null) return 'pct-muted';
  if (p <= 10) return 'pct-danger';
  if (p <= 30) return 'pct-warn';
  return 'pct-ok';
}

function creditsTitle(row: { credits?: AccountRow['credits'] }): string {
  const c = row.credits;
  if (!c) return '尚未刷新';
  if (c.lastError) return `刷新失败：${c.lastError}`;
  const parts: string[] = [];
  if (c.planName) parts.push(`套餐：${c.planName}`);
  if (typeof c.dailyPercent === 'number') parts.push(`日额度：剩余 ${c.dailyPercent.toFixed(0)}%`);
  if (typeof c.weeklyPercent === 'number') parts.push(`周额度：剩余 ${c.weeklyPercent.toFixed(0)}%`);
  if (c.dailyResetAt) {
    const t = new Date(c.dailyResetAt * 1000).toLocaleString();
    parts.push(`日重置：${t}`);
  }
  if (c.weeklyResetAt) {
    const t = new Date(c.weeklyResetAt * 1000).toLocaleString();
    parts.push(`周重置：${t}`);
  }
  if (c.fetchedAt) {
    const t = new Date(c.fetchedAt).toLocaleString();
    parts.push(`取数时间：${t}`);
  }
  return parts.join(' · ');
}

function statusColor(status: string): string {
  switch (status) {
    case 'active':
      return 'success';
    case 'error':
      return 'error';
    case 'expired':
      return 'warning';
    default:
      return 'default';
  }
}

// 账号状态中文化。底层保留英文常量（API 契约），展示层统一走这里。
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

const columns = computed(() => [
  { title: 'ID', dataIndex: 'idShort', key: 'idShort', width: 90 },
  { title: '邮箱 / 标签', dataIndex: 'email', key: 'email', ellipsis: true },
  { title: '层级', dataIndex: 'tier', key: 'tier', width: 110 },
  { title: 'RPM', dataIndex: 'rpm', key: 'rpm', width: 90 },
  { title: '余额', dataIndex: 'credits', key: 'credits', width: 96 },
  { title: '可用模型', dataIndex: 'availableModels', key: 'availableModels', width: 110 },
  { title: '状态', dataIndex: 'status', key: 'status', width: 110 },
  { title: '最近探测', dataIndex: 'lastProbed', key: 'lastProbed', width: 150 },
  { title: 'Key', dataIndex: 'keyPrefix', key: 'keyPrefix', width: 120 },
  { title: '操作', key: 'actions', width: 300, fixed: 'right' as const },
]);

const displayRows = computed(() =>
  accounts.value.map((a) => ({ ...a, idShort: a.id.slice(0, 8) })),
);

function handleTierChange(id: string, value: unknown): void {
  if (value === 'pro' || value === 'free' || value === 'expired' || value === 'unknown') {
    onSetTier(id, value);
  }
}

// Reload on mount + soft poll so tier / capability probes and credit refreshes
// kicked off by other operators land here without requiring the user to hit
// the 刷新 button. Poll paused while the tab is hidden to keep noise down.
const pollTimer = ref<number | null>(null);
onMounted(() => {
  load();
  pollTimer.value = window.setInterval(() => {
    if (!document.hidden) load();
  }, 10_000);
});
onUnmounted(() => {
  if (pollTimer.value) window.clearInterval(pollTimer.value);
});
</script>

<template>
  <PageHeader title="账号管理" subtitle="维护账号池，探测订阅层级与可用模型">
    <template #actions>
      <Button @click="onRefreshAllCredits">
        <template #icon><SyncOutlined /></template>
        刷新余额
      </Button>
      <Button @click="onProbeAll">
        <template #icon><SearchOutlined /></template>
        全部探测
      </Button>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
    </template>
  </PageHeader>

  <Card size="small" title="添加账号" style="margin-bottom: 16px">
    <div class="add-row">
      <Select v-model:value="form.type" style="width: 150px">
        <SelectOption value="token">Auth Token</SelectOption>
        <SelectOption value="api_key">API Key</SelectOption>
      </Select>
      <Input
        v-model:value="form.value"
        placeholder="粘贴 Token 或 API Key"
        allow-clear
      />
      <Input
        v-model:value="form.label"
        placeholder="标签（可选）"
        style="width: 200px"
        allow-clear
      />
      <Button type="primary" @click="onAdd">
        <template #icon><PlusOutlined /></template>
        添加
      </Button>
    </div>
    <p class="hint">
      推荐 Token 方式。登录后从
      <a href="https://windsurf.com/show-auth-token" target="_blank" rel="noreferrer">
        windsurf.com/show-auth-token
      </a>
      复制 Token 粘贴到上方。
    </p>
  </Card>

  <Card size="small" :body-style="{ padding: 0 }">
    <template #title>账号列表 ({{ accounts.length }})</template>
    <Table
      :data-source="displayRows"
      :columns="columns"
      :loading="loading"
      :pagination="false"
      size="middle"
      row-key="id"
      :scroll="{ x: 1300 }"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'tier'">
          <Select
            :value="record.tier"
            size="small"
            style="width: 96px"
            @change="(v: unknown) => handleTierChange(record.id, v)"
          >
            <SelectOption value="pro">Pro</SelectOption>
            <SelectOption value="free">Free</SelectOption>
            <SelectOption value="expired">Expired</SelectOption>
            <SelectOption value="unknown">Unknown</SelectOption>
          </Select>
        </template>
        <template v-else-if="column.key === 'idShort'">
          <code class="text-mono">{{ record.idShort }}</code>
        </template>
        <template v-else-if="column.key === 'rpm'">
          <span class="text-mono">{{ record.rpmUsed }}/{{ record.rpmLimit }}</span>
        </template>
        <template v-else-if="column.key === 'credits'">
          <div class="credits-cell" :title="creditsTitle(record)">
            <template v-if="creditsView(record).hasError">
              <span class="pct-danger">!</span>
            </template>
            <template v-else-if="!creditsView(record).hasData">
              <span class="text-dim">—</span>
            </template>
            <template v-else>
              <span class="credits-line"
                ><span class="credits-label">日</span
                ><span :class="pctClass(creditsView(record).daily)">{{
                  creditsView(record).daily !== null ? `${creditsView(record).daily!.toFixed(0)}%` : '—'
                }}</span></span>
              <span class="credits-line"
                ><span class="credits-label">周</span
                ><span :class="pctClass(creditsView(record).weekly)">{{
                  creditsView(record).weekly !== null ? `${creditsView(record).weekly!.toFixed(0)}%` : '—'
                }}</span></span>
            </template>
          </div>
        </template>
        <template v-else-if="column.key === 'availableModels'">
          {{ record.availableCount ?? 0 }} / {{ record.tierModelCount ?? 0 }}
        </template>
        <template v-else-if="column.key === 'status'">
          <Tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</Tag>
        </template>
        <template v-else-if="column.key === 'lastProbed'">
          <span class="text-mono">
            {{ record.lastProbed ? new Date(record.lastProbed).toLocaleString() : '—' }}
          </span>
        </template>
        <template v-else-if="column.key === 'keyPrefix'">
          <code>{{ record.keyPrefix }}</code>
        </template>
        <template v-else-if="column.key === 'actions'">
          <Space size="small" wrap>
            <Button size="small" @click="onProbeOne(record.id)">
              <template #icon><SearchOutlined /></template>
              探测
            </Button>
            <Button size="small" @click="onRefreshCredits(record.id)">
              <template #icon><WalletOutlined /></template>
              余额
            </Button>
            <Button size="small" @click="onRateLimit(record.id, record.email)">
              <template #icon><ThunderboltOutlined /></template>
              限流
            </Button>
            <Button size="small" @click="onRefreshTok(record.id)">
              <template #icon><KeyOutlined /></template>
              刷新
            </Button>
            <Popconfirm
              :title="record.status === 'disabled' ? '启用该账号进入轮询？' : '停用后该账号不再被调用，但保留配置。确认停用？'"
              @confirm="onToggleDisabled(record.id, record.status)"
            >
              <Button size="small">
                <template #icon>
                  <PlayCircleOutlined v-if="record.status === 'disabled'" />
                  <PauseCircleOutlined v-else />
                </template>
                {{ record.status === 'disabled' ? '启用' : '停用' }}
              </Button>
            </Popconfirm>
            <Popconfirm title="确认删除该账号？" @confirm="onRemove(record.id)">
              <Button size="small" danger>
                <template #icon><DeleteOutlined /></template>
              </Button>
            </Popconfirm>
          </Space>
        </template>
      </template>
    </Table>
  </Card>
</template>

<style scoped>
.add-row {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
}
.add-row :deep(.ant-input),
.add-row :deep(.ant-select) {
  flex: 1;
  min-width: 200px;
}
.hint {
  margin: 12px 0 0;
  font-size: 12px;
  color: var(--color-text-muted);
}

.credits-cell {
  display: inline-flex;
  flex-direction: column;
  line-height: 1.15;
  font-family: var(--font-mono);
  font-size: 11.5px;
}
.credits-line {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  white-space: nowrap;
}
.credits-label {
  color: var(--color-text-dim);
  font-size: 10px;
}
.pct-ok {
  color: var(--color-text);
}
.pct-warn {
  color: var(--color-warning);
  font-weight: 600;
}
.pct-danger {
  color: var(--color-danger);
  font-weight: 600;
}
.pct-muted {
  color: var(--color-text-dim);
}
</style>
