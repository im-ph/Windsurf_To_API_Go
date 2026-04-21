<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { InputPassword, Button, Alert } from 'ant-design-vue';
import { LockOutlined } from '@ant-design/icons-vue';
import { useAuthStore } from '@/stores/auth';
import { toast } from '@/api/request';

const router = useRouter();
const route = useRoute();
const auth = useAuthStore();

const password = ref('');
const submitting = ref(false);
const errorMsg = ref('');

onMounted(async () => {
  if (!auth.ready) await auth.probe();
  if (!auth.required) {
    // Dashboard is open — skip login altogether.
    await redirect();
    return;
  }
  if (auth.authenticated) await redirect();
});

async function redirect(): Promise<void> {
  const target = typeof route.query.redirect === 'string' ? route.query.redirect : '/overview';
  await router.replace(target.startsWith('/') ? target : '/overview');
}

async function onSubmit(): Promise<void> {
  if (!password.value) {
    errorMsg.value = '请输入控制台密码';
    return;
  }
  submitting.value = true;
  errorMsg.value = '';
  try {
    const ok = await auth.login(password.value);
    if (!ok) {
      errorMsg.value = '密码错误，请重试';
      return;
    }
    await redirect();
  } catch (err) {
    toast(err, '登录失败');
    errorMsg.value = err instanceof Error ? err.message : '登录失败';
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="login-shell">
    <div class="login-card">
      <div class="mark">W</div>
      <h1>WindsurfAPI 控制台</h1>
      <p class="subtitle">请输入管理密码以继续</p>

      <Alert
        v-if="errorMsg"
        type="error"
        :message="errorMsg"
        show-icon
        style="margin-bottom: 16px"
      />

      <form class="form" @submit.prevent="onSubmit">
        <InputPassword
          v-model:value="password"
          size="large"
          placeholder="Dashboard 密码"
          autocomplete="current-password"
          allow-clear
        >
          <template #prefix>
            <LockOutlined />
          </template>
        </InputPassword>
        <Button
          type="primary"
          html-type="submit"
          size="large"
          block
          :loading="submitting"
          class="submit"
        >
          登 录
        </Button>
      </form>

      <p class="hint">
        密码由服务端 <code>DASHBOARD_PASSWORD</code> 环境变量决定；未设置时与 <code>API_KEY</code> 一致。
      </p>
    </div>
  </div>
</template>

<style scoped>
.login-shell {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: linear-gradient(180deg, #ffffff 0%, #f9fafb 100%);
}
.login-card {
  width: 100%;
  max-width: 400px;
  padding: 36px 32px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
}
.mark {
  width: 56px;
  height: 56px;
  margin: 0 auto 18px;
  border-radius: 14px;
  background: linear-gradient(135deg, #4f46e5 0%, #7c3aed 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
  font-weight: 800;
  color: #fff;
  box-shadow: 0 10px 30px rgba(79, 70, 229, 0.35);
}
h1 {
  font-size: 18px;
  text-align: center;
  margin: 0 0 8px;
  font-weight: 700;
  color: var(--color-text);
}
.subtitle {
  font-size: 13px;
  color: var(--color-text-muted);
  text-align: center;
  margin: 0 0 22px;
}
.form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.submit {
  font-weight: 600;
}
.hint {
  margin-top: 22px;
  font-size: 12px;
  color: var(--color-text-dim);
  line-height: 1.55;
}
code {
  font-family: var(--font-mono);
  background: var(--color-surface-alt);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
}
</style>
