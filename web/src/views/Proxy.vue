<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import {
  Button,
  Card,
  Input,
  InputPassword,
  InputNumber,
  Select,
  SelectOption,
  Table,
  Popconfirm,
  message,
} from 'ant-design-vue';
import { ReloadOutlined, CheckCircleOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import {
  clearAccountProxy,
  clearGlobalProxy,
  getProxy,
  setAccountProxy,
  setGlobalProxy,
  testProxy,
} from '@/api/proxy';
import { listAccounts } from '@/api/accounts';
import type { AccountRow, ProxyConfig, ProxySpec } from '@/api/types';
import { toast } from '@/api/request';

const cfg = ref<ProxyConfig>({ accounts: {} });
const accounts = ref<AccountRow[]>([]);
const loading = ref(false);
const testing = ref(false);
const testResult = ref('');

// If the operator pasted "host.example.com:21281" into the host field and
// left the port field blank, split once at submit / test time so they don't
// have to move the port by hand. Mutates the inputs in place.
function splitHostPort(spec: { host?: string; port?: number }): void {
  if (!spec.host) return;
  if (typeof spec.port === 'number' && spec.port > 0) return;
  const m = spec.host.trim().match(/^(.+):(\d{1,5})$/);
  if (!m) return;
  spec.host = m[1];
  spec.port = Number(m[2]);
}

const form = reactive<ProxySpec>({
  type: 'http',
  host: '',
  port: undefined,
  user: '',
  pass: '',
});

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [c, a] = await Promise.all([getProxy(), listAccounts()]);
    cfg.value = c;
    accounts.value = a.accounts ?? [];
    const g = c.global ?? {};
    form.type = g.type ?? 'http';
    form.host = g.host ?? '';
    form.port = g.port;
    form.user = g.user ?? '';
    form.pass = g.pass ?? '';
  } catch (err) {
    toast(err, '加载失败');
  } finally {
    loading.value = false;
  }
}

async function onSave(): Promise<void> {
  if (!form.host?.trim()) {
    message.warning('请输入代理主机');
    return;
  }
  splitHostPort(form);
  if (!form.port) {
    message.warning('请填写端口，或把 :端口 附在主机后面');
    return;
  }
  try {
    const r = await setGlobalProxy({
      type: form.type,
      host: form.host!.trim(),
      port: form.port,
      user: form.user?.trim() || undefined,
      pass: form.pass || undefined,
    });
    cfg.value = r.config;
    message.success('已保存全局代理');
  } catch (err) {
    toast(err, '保存失败');
  }
}

async function onClearGlobal(): Promise<void> {
  try {
    await clearGlobalProxy();
    await load();
    message.success('已清除全局代理');
  } catch (err) {
    toast(err, '操作失败');
  }
}

async function onTest(): Promise<void> {
  splitHostPort(form);
  if (!form.host?.trim() || !form.port) {
    testResult.value = '请先填写主机和端口（或把 :端口 附在主机后面）';
    return;
  }
  testing.value = true;
  testResult.value = '';
  try {
    const r = await testProxy({
      type: form.type,
      host: form.host.trim(),
      port: form.port,
      username: form.user?.trim() || undefined,
      password: form.pass || undefined,
    });
    if (r.ok) {
      testResult.value = `✓ 出口 IP: ${r.egressIp ?? '-'}${r.latencyMs ? ` (${r.latencyMs}ms)` : ''}`;
    } else {
      testResult.value = `✗ ${r.error ?? '测试失败'}`;
    }
  } catch (err) {
    testResult.value = err instanceof Error ? err.message : '测试失败';
  } finally {
    testing.value = false;
  }
}

async function onClearAccount(id: string): Promise<void> {
  try {
    await clearAccountProxy(id);
    await load();
    message.success('已清除');
  } catch (err) {
    toast(err, '操作失败');
  }
}

const perAccount = reactive<{ id: string; type: 'http' | 'https' | 'socks5'; host: string; port?: number; user: string; pass: string }>({
  id: '',
  type: 'http',
  host: '',
  port: undefined,
  user: '',
  pass: '',
});
const perAccountTesting = ref(false);
const perAccountResult = ref('');

async function onAddAccountProxy(): Promise<void> {
  if (!perAccount.id) {
    message.warning('请先选择账号');
    return;
  }
  if (!perAccount.host?.trim()) {
    message.warning('请填写代理主机');
    return;
  }
  splitHostPort(perAccount);
  if (!perAccount.port) {
    message.warning('请填写端口，或把 :端口 附在主机后面');
    return;
  }
  try {
    await setAccountProxy(perAccount.id, {
      type: perAccount.type,
      host: perAccount.host.trim(),
      port: perAccount.port,
      user: perAccount.user?.trim() || undefined,
      pass: perAccount.pass || undefined,
    });
    message.success('已保存账号级代理');
    perAccount.id = '';
    perAccount.host = '';
    perAccount.port = undefined;
    perAccount.user = '';
    perAccount.pass = '';
    await load();
  } catch (err) {
    toast(err, '保存失败');
  }
}

async function onTestAccountProxy(): Promise<void> {
  splitHostPort(perAccount);
  if (!perAccount.host?.trim() || !perAccount.port) {
    perAccountResult.value = '请先填写主机和端口（或把 :端口 附在主机后面）';
    return;
  }
  perAccountTesting.value = true;
  perAccountResult.value = '';
  try {
    const r = await testProxy({
      type: perAccount.type,
      host: perAccount.host.trim(),
      port: perAccount.port,
      username: perAccount.user?.trim() || undefined,
      password: perAccount.pass || undefined,
    });
    perAccountResult.value = r.ok
      ? `✓ 出口 IP ${r.egressIp ?? '-'}${r.latencyMs ? ` · ${r.latencyMs}ms` : ''}`
      : `✗ ${r.error ?? '失败'}`;
  } catch (err) {
    perAccountResult.value = err instanceof Error ? err.message : '测试失败';
  } finally {
    perAccountTesting.value = false;
  }
}

// Accounts that already have a dedicated proxy are shown in the list below;
// offer the rest in the add-form dropdown so you can't accidentally overwrite
// an existing binding from here.
const unboundAccounts = computed(() =>
  accounts.value.filter((a) => !cfg.value.accounts?.[a.id]),
);

const accountSelectOptions = computed(() =>
  unboundAccounts.value.map((a) => ({
    value: a.id,
    label: `${a.email} · ${a.id.slice(0, 8)}`,
  })),
);

function filterAccountOption(input: string, opt: { label?: string; value?: string }): boolean {
  const needle = input.toLowerCase();
  return (opt.label ?? '').toLowerCase().includes(needle) ||
    (opt.value ?? '').toLowerCase().includes(needle);
}

function describeProxy(p?: ProxySpec | null): string {
  if (!p || !p.host) return '—';
  return `${p.type ?? 'http'}://${p.user ? `${p.user}@` : ''}${p.host}:${p.port ?? ''}`;
}

const accountRows = computed(() => {
  const entries = Object.entries(cfg.value.accounts ?? {});
  return entries.map(([id, spec]) => {
    const acc = accounts.value.find((a) => a.id === id);
    return {
      id,
      email: acc?.email ?? id.slice(0, 12),
      spec,
    };
  });
});

const accountColumns = [
  { title: '账号', dataIndex: 'email', key: 'email', ellipsis: true },
  { title: '代理', key: 'proxy' },
  { title: '操作', key: 'actions', width: 140 },
];

onMounted(load);
</script>

<template>
  <PageHeader title="代理配置" subtitle="全局或按账号配置出口代理；独立代理将启动独立的 LS 实例">
    <template #actions>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
    </template>
  </PageHeader>

  <Card size="small" title="全局代理" style="margin-bottom: 16px">
    <div class="form-row">
      <div class="field" style="max-width: 140px">
        <label>类型</label>
        <Select v-model:value="form.type">
          <SelectOption value="http">HTTP</SelectOption>
          <SelectOption value="https">HTTPS</SelectOption>
          <SelectOption value="socks5">SOCKS5</SelectOption>
        </Select>
      </div>
      <div class="field" style="flex: 2">
        <label>主机</label>
        <Input v-model:value="form.host" placeholder="proxy.example.com" allow-clear />
      </div>
      <div class="field" style="max-width: 140px">
        <label>端口</label>
        <InputNumber v-model:value="form.port" :min="1" :max="65535" style="width: 100%" />
      </div>
    </div>
    <div class="form-row" style="margin-top: 12px">
      <div class="field">
        <label>用户名（可选）</label>
        <Input v-model:value="form.user" placeholder="代理认证用户名" allow-clear />
      </div>
      <div class="field">
        <label>密码（可选）</label>
        <InputPassword v-model:value="form.pass" placeholder="代理认证密码" />
      </div>
    </div>
    <div class="actions">
      <Button type="primary" @click="onSave">保存</Button>
      <Button @click="onClearGlobal">清除</Button>
      <Button :loading="testing" @click="onTest">
        <template #icon><CheckCircleOutlined /></template>
        测试
      </Button>
      <span class="test-result">{{ testResult }}</span>
    </div>
    <div v-if="cfg.global?.host" class="current">
      当前生效：<code>{{ describeProxy(cfg.global) }}</code>
    </div>
  </Card>

  <Card size="small" title="新增账号级代理" style="margin-bottom: 16px">
    <p class="desc">
      为特定账号指定独立代理；每个独立代理会启动独立的 LS 实例。留空账号则改用全局代理。
    </p>
    <div class="form-row">
      <div class="field" style="flex: 2; min-width: 240px">
        <label>账号</label>
        <Select
          v-model:value="perAccount.id"
          placeholder="选择账号"
          show-search
          allow-clear
          :filter-option="filterAccountOption"
          :options="accountSelectOptions"
        />
      </div>
      <div class="field" style="max-width: 140px">
        <label>类型</label>
        <Select v-model:value="perAccount.type">
          <SelectOption value="http">HTTP</SelectOption>
          <SelectOption value="https">HTTPS</SelectOption>
          <SelectOption value="socks5">SOCKS5</SelectOption>
        </Select>
      </div>
      <div class="field" style="flex: 2">
        <label>主机</label>
        <Input v-model:value="perAccount.host" placeholder="proxy.example.com" allow-clear />
      </div>
      <div class="field" style="max-width: 130px">
        <label>端口</label>
        <InputNumber v-model:value="perAccount.port" :min="1" :max="65535" style="width: 100%" />
      </div>
    </div>
    <div class="form-row" style="margin-top: 10px">
      <div class="field">
        <label>用户名（可选）</label>
        <Input v-model:value="perAccount.user" placeholder="代理认证用户名" allow-clear />
      </div>
      <div class="field">
        <label>密码（可选）</label>
        <InputPassword v-model:value="perAccount.pass" placeholder="代理认证密码" />
      </div>
    </div>
    <div class="actions">
      <Button type="primary" @click="onAddAccountProxy">
        <template #icon><PlusOutlined /></template>
        保存
      </Button>
      <Button :loading="perAccountTesting" @click="onTestAccountProxy">
        <template #icon><CheckCircleOutlined /></template>
        测试
      </Button>
      <span class="text-muted">{{ perAccountResult }}</span>
    </div>
  </Card>

  <Card size="small" title="账号独立代理" :body-style="{ padding: 0 }">
    <Table
      :data-source="accountRows"
      :columns="accountColumns"
      :loading="loading"
      :pagination="false"
      row-key="id"
      size="middle"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'proxy'">
          <code>{{ describeProxy(record.spec) }}</code>
        </template>
        <template v-else-if="column.key === 'actions'">
          <Popconfirm title="清除该账号的独立代理？" @confirm="onClearAccount(record.id)">
            <Button size="small" danger>
              <template #icon><DeleteOutlined /></template>
              清除
            </Button>
          </Popconfirm>
        </template>
      </template>
    </Table>
  </Card>
</template>

<style scoped>
.form-row {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.field {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 160px;
}
.field label {
  font-size: 12px;
  color: var(--color-text-muted);
  font-weight: 500;
}
.actions {
  margin-top: 16px;
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
}
.test-result {
  font-size: 12px;
  color: var(--color-text-muted);
  margin-left: 8px;
}
.current {
  margin-top: 10px;
  font-size: 12px;
  color: var(--color-text-muted);
}
.desc {
  font-size: 12px;
  color: var(--color-text-muted);
  margin: 0 0 14px;
}
code {
  font-family: var(--font-mono);
  background: var(--color-surface-alt);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 12px;
}
</style>
