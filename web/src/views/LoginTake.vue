<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Input,
  InputNumber,
  InputPassword,
  Select,
  SelectOption,
  Table,
  Tag,
  message,
} from 'ant-design-vue';
import {
  GoogleOutlined,
  GithubOutlined,
  LoginOutlined,
  CheckCircleOutlined,
} from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import { oauthLogin, windsurfLogin } from '@/api/login';
import { testProxy } from '@/api/proxy';
import { signInWithOAuth } from '@/composables/useFirebaseOAuth';
import type { ProxySpec, WindsurfLoginResult } from '@/api/types';
import { ApiError, toast } from '@/api/request';

// Business failures (wrong password, IP block, etc.) come back as HTTP 400
// with the structured body {error, isAuthFail, firebaseCode}. The axios
// interceptor turns non-2xx into ApiError(payload). Recover the body so the
// caller can still render + record the failure the same way as a 200 body.
function asLoginResult(err: unknown, fallbackMsg: string): WindsurfLoginResult {
  if (err instanceof ApiError) {
    if (err.payload && typeof err.payload === 'object') {
      return { success: false, ...(err.payload as Partial<WindsurfLoginResult>) };
    }
    return { success: false, error: err.message || fallbackMsg };
  }
  if (err instanceof Error) return { success: false, error: err.message };
  return { success: false, error: fallbackMsg };
}

const HISTORY_KEY = 'windsurfapi:login-history';

interface HistoryRow {
  ts: number;
  email: string;
  ok: boolean;
  proxy?: string;
  error?: string;
  method: string;
}

const form = reactive({
  email: '',
  password: '',
  autoAdd: true,
});

const proxy = reactive<ProxySpec>({ type: 'http', host: '', port: undefined, user: '', pass: '' });
const proxyResult = ref('');
const testing = ref(false);
const submitting = ref(false);
const oauthBusy = ref<'google' | 'github' | null>(null);
const result = ref<WindsurfLoginResult | null>(null);
const history = ref<HistoryRow[]>([]);

function loadHistory(): void {
  try {
    history.value = JSON.parse(localStorage.getItem(HISTORY_KEY) ?? '[]') as HistoryRow[];
  } catch {
    history.value = [];
  }
}
function saveHistory(): void {
  localStorage.setItem(HISTORY_KEY, JSON.stringify(history.value.slice(-50)));
}
function recordHistory(r: HistoryRow): void {
  history.value = [r, ...history.value].slice(0, 50);
  saveHistory();
}

function currentProxy(): ProxySpec | null {
  if (!proxy.host?.trim()) return null;
  return {
    type: proxy.type,
    host: proxy.host.trim(),
    port: proxy.port,
    user: proxy.user?.trim() || undefined,
    pass: proxy.pass || undefined,
  };
}

function describeProxy(p: ProxySpec | null): string {
  if (!p) return '';
  return `${p.type}://${p.host}:${p.port ?? ''}`;
}

async function onTestProxy(): Promise<void> {
  testing.value = true;
  proxyResult.value = '';
  try {
    // Accept "host:port" in the host field when port is empty.
    if (proxy.host && (!proxy.port || proxy.port === 0)) {
      const m = proxy.host.trim().match(/^(.+):(\d{1,5})$/);
      if (m) {
        proxy.host = m[1];
        proxy.port = Number(m[2]);
      }
    }
    if (!proxy.host?.trim() || !proxy.port) {
      proxyResult.value = '请先填写主机和端口（或把 :端口 附在主机后面）';
      return;
    }
    const r = await testProxy({
      type: proxy.type,
      host: proxy.host.trim(),
      port: proxy.port,
      username: proxy.user?.trim() || undefined,
      password: proxy.pass || undefined,
    });
    proxyResult.value = r.ok
      ? `✓ 出口 IP ${r.egressIp ?? '-'}${r.latencyMs ? ` · ${r.latencyMs}ms` : ''}`
      : `✗ ${r.error ?? '失败'}`;
  } catch (err) {
    proxyResult.value = err instanceof Error ? err.message : '测试失败';
  } finally {
    testing.value = false;
  }
}

async function onEmailLogin(): Promise<void> {
  const email = form.email.trim();
  if (!email || !form.password) {
    message.warning('请输入邮箱与密码');
    return;
  }
  submitting.value = true;
  const currentProxySpec = currentProxy();
  let r: WindsurfLoginResult;
  try {
    r = await windsurfLogin({
      email,
      password: form.password,
      autoAdd: form.autoAdd,
      proxy: currentProxySpec,
    });
  } catch (err) {
    r = asLoginResult(err, '登录失败');
  } finally {
    submitting.value = false;
  }
  result.value = r;
  recordHistory({
    ts: Date.now(),
    email,
    ok: !!r.success,
    method: 'email',
    proxy: describeProxy(currentProxySpec),
    error: r.error,
  });
  if (r.success) {
    message.success('登录成功');
    form.password = '';
  } else {
    message.error(r.error ?? '登录失败');
  }
}

// Firebase's web SDK only accepts sign-in popups from origins whitelisted on
// the exa2-fb170 project (windsurf.com + localhost). Self-hosted instances
// on arbitrary IPs trigger "auth/requests-from-referer-blocked". The user
// can't whitelist the IP themselves — it's Windsurf's Firebase project. So
// the only working paths are: (a) access via localhost (SSH tunnel), (b) use
// email+password through server (which bypasses the SDK, hitting Firebase's
// REST endpoint from the server's egress IP — and that has its own IP risk
// control, see loginFailureHint below).
const oauthAvailable = computed(() => {
  const host = window.location.hostname;
  return host === 'localhost' || host === '127.0.0.1' || host.endsWith('windsurf.com');
});

const serverIp = computed(() => window.location.hostname);

// When Firebase rejects the login from the service's egress IP (common on
// AWS / Azure data-center ranges), isAuthFail is surfaced by the backend.
// In that case the failure mode usually isn't the user's password — it's
// Firebase's abuse protection. Give the user an actionable hint instead of
// just "邮箱或密码错误".
const loginFailureHint = computed(() => {
  if (!result.value || result.value.success) return '';
  if (result.value.isAuthFail && !proxy.host?.trim()) {
    return '若确认账号/密码无误，当前服务器出口 IP 可能被 Firebase 风控（典型现象：浏览器能登、服务端登不了）。请在下方「登录代理」里配一个住宅代理或 OAuth 登录后重试。';
  }
  return '';
});

async function onOAuth(provider: 'google' | 'github'): Promise<void> {
  oauthBusy.value = provider;
  let oauthEmail = '';
  let r: WindsurfLoginResult;
  try {
    const payload = await signInWithOAuth(provider);
    oauthEmail = payload.email;
    try {
      r = await oauthLogin({
        idToken: payload.idToken,
        refreshToken: payload.refreshToken,
        email: payload.email,
        provider,
        autoAdd: form.autoAdd,
      });
    } catch (err) {
      r = asLoginResult(err, 'OAuth 登录失败');
    }
  } catch (err) {
    // Firebase popup failures (user cancelled, popup blocked, etc.) — record
    // so the user can see why no account was added.
    toast(err, 'OAuth 登录失败');
    r = { success: false, error: err instanceof Error ? err.message : 'OAuth 登录失败' };
  } finally {
    oauthBusy.value = null;
  }
  result.value = r;
  recordHistory({
    ts: Date.now(),
    email: oauthEmail || '(unknown)',
    ok: !!r.success,
    method: provider,
    error: r.error,
  });
  if (r.success) message.success('登录成功');
  else message.error(r.error ?? 'OAuth 登录失败');
}

function fmtTs(ts: number): string {
  return new Date(ts).toLocaleString('zh-CN', { hour12: false });
}

onMounted(loadHistory);

const historyColumns = [
  { title: '时间', dataIndex: 'ts', width: 180 },
  { title: '邮箱', dataIndex: 'email', ellipsis: true },
  { title: '方式', dataIndex: 'method', width: 90 },
  { title: '状态', dataIndex: 'ok', width: 100 },
  { title: '代理', dataIndex: 'proxy', ellipsis: true },
  { title: '备注', dataIndex: 'error', ellipsis: true },
];
</script>

<template>
  <PageHeader title="登录取号" subtitle="支持 Google / GitHub / 邮箱密码 三种方式获取 Windsurf API Key" />

  <Card size="small" title="快捷登录（推荐）" style="margin-bottom: 16px">
    <Alert
      v-if="!oauthAvailable"
      type="warning"
      show-icon
      style="margin-bottom: 14px"
    >
      <template #message>当前域名未在 Firebase 白名单内，OAuth 登录会被 <code>auth/requests-from-referer-blocked</code> 拦下</template>
      <template #description>
        该 Firebase 项目由 Windsurf 官方持有，只允许 <code>windsurf.com</code> 与 <code>localhost</code> 两个来源。
        若要在本服务器上用 OAuth 取号，请在你自己的电脑上做一次 SSH 隧道再访问：
        <code>ssh -L 3003:localhost:3003 root@{{ serverIp }}</code>
        ，然后浏览器打开 <code>http://localhost:3003/dashboard</code>；<br>
        或者退回到下方的「邮箱密码登录」。
      </template>
    </Alert>
    <div class="oauth-row">
      <Button
        size="large"
        class="oauth-btn google"
        :loading="oauthBusy === 'google'"
        :disabled="!oauthAvailable"
        @click="onOAuth('google')"
      >
        <GoogleOutlined />
        Google 登录
      </Button>
      <Button
        size="large"
        class="oauth-btn github"
        :loading="oauthBusy === 'github'"
        :disabled="!oauthAvailable"
        @click="onOAuth('github')"
      >
        <GithubOutlined />
        GitHub 登录
      </Button>
      <Checkbox v-model:checked="form.autoAdd" class="auto-add">登录后自动加入账号池</Checkbox>
    </div>
  </Card>

  <Card size="small" title="邮箱密码登录" style="margin-bottom: 16px">
    <div class="form-row">
      <div class="field">
        <label>邮箱</label>
        <Input v-model:value="form.email" placeholder="your-email@example.com" allow-clear />
      </div>
      <div class="field">
        <label>密码</label>
        <InputPassword v-model:value="form.password" placeholder="••••••••" @press-enter="onEmailLogin" />
      </div>
    </div>
    <div class="actions">
      <Button type="primary" :loading="submitting" @click="onEmailLogin">
        <template #icon><LoginOutlined /></template>
        登录
      </Button>
      <Checkbox v-model:checked="form.autoAdd">登录后自动加入账号池</Checkbox>
    </div>
    <p class="hint">仅限邮箱+密码注册的账号；第三方登录请用上方按钮。</p>
  </Card>

  <Card size="small" title="登录代理（可选）" style="margin-bottom: 16px">
    <div class="form-row">
      <div class="field" style="max-width: 140px">
        <label>类型</label>
        <Select v-model:value="proxy.type">
          <SelectOption value="http">HTTP</SelectOption>
          <SelectOption value="https">HTTPS</SelectOption>
          <SelectOption value="socks5">SOCKS5</SelectOption>
        </Select>
      </div>
      <div class="field" style="flex: 2">
        <label>主机</label>
        <Input v-model:value="proxy.host" placeholder="留空=使用全局" allow-clear />
      </div>
      <div class="field" style="max-width: 130px">
        <label>端口</label>
        <InputNumber v-model:value="proxy.port" :min="1" :max="65535" style="width: 100%" />
      </div>
      <div class="field">
        <label>用户名</label>
        <Input v-model:value="proxy.user" placeholder="可选" allow-clear />
      </div>
      <div class="field">
        <label>密码</label>
        <InputPassword v-model:value="proxy.pass" placeholder="可选" />
      </div>
    </div>
    <div class="actions">
      <Button :loading="testing" @click="onTestProxy">
        <template #icon><CheckCircleOutlined /></template>
        测试代理
      </Button>
      <span class="text-muted">{{ proxyResult }}</span>
    </div>
  </Card>

  <Alert
    v-if="result?.success && result.apiKey"
    type="success"
    show-icon
    style="margin-bottom: 16px"
  >
    <template #message>
      登录成功：{{ result.email }}
      <span v-if="result.name">（{{ result.name }}）</span>
    </template>
    <template #description>
      <code>{{ result.apiKey?.slice(0, 12) }}…</code> 已{{ form.autoAdd ? '自动加入账号池' : '返回，未加入账号池' }}
    </template>
  </Alert>
  <Alert
    v-else-if="result && !result.success"
    type="error"
    show-icon
    style="margin-bottom: 16px"
  >
    <template #message>{{ result.error ?? '登录失败' }}</template>
    <template v-if="loginFailureHint" #description>{{ loginFailureHint }}</template>
  </Alert>

  <Card size="small" title="登录历史" :body-style="{ padding: 0 }">
    <Table
      :data-source="history"
      :columns="historyColumns"
      :pagination="false"
      size="middle"
      :row-key="(r: HistoryRow) => `${r.ts}-${r.email}`"
    >
      <template #bodyCell="{ column, record }">
        <template v-if="column.dataIndex === 'ts'">
          <span class="text-mono">{{ fmtTs(record.ts) }}</span>
        </template>
        <template v-else-if="column.dataIndex === 'ok'">
          <Tag :color="record.ok ? 'success' : 'error'">{{ record.ok ? '成功' : '失败' }}</Tag>
        </template>
      </template>
    </Table>
  </Card>
</template>

<style scoped>
.oauth-row {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
}
.oauth-btn {
  font-weight: 600;
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.oauth-btn.google {
  background: #fff;
  border-color: #dadce0;
  color: #3c4043;
}
.oauth-btn.github {
  background: #24292e;
  border-color: #24292e;
  color: #fff;
}
.oauth-btn.github:hover {
  background: #1f2328 !important;
  color: #fff !important;
}
.auto-add {
  margin-left: auto;
}
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
  min-width: 140px;
}
.field label {
  font-size: 12px;
  color: var(--color-text-muted);
  font-weight: 500;
}
.actions {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  margin-top: 14px;
}
.hint {
  margin: 12px 0 0;
  font-size: 12px;
  color: var(--color-text-muted);
}
code {
  font-family: var(--font-mono);
  background: var(--color-surface-alt);
  padding: 2px 6px;
  border-radius: 4px;
}
</style>
