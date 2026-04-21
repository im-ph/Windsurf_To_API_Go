<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  Layout,
  LayoutSider,
  LayoutContent,
  Menu,
  MenuItem,
  MenuItemGroup,
  Drawer,
  Button,
} from 'ant-design-vue';
import {
  DashboardOutlined,
  BarChartOutlined,
  LoginOutlined,
  TeamOutlined,
  AlertOutlined,
  DatabaseOutlined,
  SwapOutlined,
  FileTextOutlined,
  ExperimentOutlined,
  LogoutOutlined,
  MenuOutlined,
} from '@ant-design/icons-vue';
import { useAuthStore } from '@/stores/auth';
import { getHealth } from '@/api/auth';

const router = useRouter();
const route = useRoute();
const auth = useAuthStore();

const selectedKeys = computed(() => [route.name?.toString() ?? 'Overview']);
const version = ref('—');
const versionTitle = ref('');
const isNarrow = ref(false);
const drawerOpen = ref(false);
const currentTitle = computed(() => (route.meta.title as string) ?? '仪表盘');

// Narrow-viewport threshold. Covers phone + iPad portrait (typ. 768–834 CSS
// px). Tablet landscape + desktop stay on the full sider.
const NARROW_QUERY = '(max-width: 899.98px)';
let mql: MediaQueryList | null = null;
function syncViewport(e: MediaQueryList | MediaQueryListEvent): void {
  isNarrow.value = e.matches;
  if (!e.matches) drawerOpen.value = false;
}

onMounted(async () => {
  mql = window.matchMedia(NARROW_QUERY);
  syncViewport(mql);
  mql.addEventListener('change', syncViewport);
  try {
    const h = await getHealth();
    version.value = h.commit ? `v${h.version} · ${h.commit.slice(0, 7)}` : `v${h.version}`;
    versionTitle.value = h.commitMessage
      ? `${h.commitMessage}${h.branch ? ` (${h.branch})` : ''}`
      : `版本 ${h.version}`;
  } catch {
    version.value = '—';
  }
});
onUnmounted(() => {
  mql?.removeEventListener('change', syncViewport);
});

// Close the drawer after every navigation so it never lingers on mobile.
watch(
  () => route.fullPath,
  () => {
    drawerOpen.value = false;
  },
);

function onSelect(info: { key: string | number }) {
  router.push({ name: String(info.key) });
  drawerOpen.value = false;
}

function onLogout() {
  auth.logout();
  router.replace({ name: 'Login' });
}
</script>

<template>
  <Layout class="shell">
    <LayoutSider
      v-if="!isNarrow"
      class="sider"
      :width="236"
      theme="light"
    >
      <div class="sider-inner">
        <div class="brand">
          <div class="brand-mark">W</div>
          <div class="brand-meta">
            <div class="brand-name">WindsurfAPI</div>
            <div class="brand-sub">管理控制台</div>
          </div>
        </div>

        <Menu
          mode="inline"
          theme="light"
          :selected-keys="selectedKeys"
          class="side-menu"
          @click="onSelect"
        >
          <MenuItemGroup title="概览">
            <MenuItem key="Overview">
              <template #icon><DashboardOutlined /></template>
              仪表盘
            </MenuItem>
            <MenuItem key="Stats">
              <template #icon><BarChartOutlined /></template>
              统计分析
            </MenuItem>
          </MenuItemGroup>

          <MenuItemGroup title="账号">
            <MenuItem key="LoginTake">
              <template #icon><LoginOutlined /></template>
              登录取号
            </MenuItem>
            <MenuItem key="Accounts">
              <template #icon><TeamOutlined /></template>
              账号管理
            </MenuItem>
            <MenuItem key="Bans">
              <template #icon><AlertOutlined /></template>
              异常监测
            </MenuItem>
          </MenuItemGroup>

          <MenuItemGroup title="系统">
            <MenuItem key="Models">
              <template #icon><DatabaseOutlined /></template>
              模型控制
            </MenuItem>
            <MenuItem key="Proxy">
              <template #icon><SwapOutlined /></template>
              代理配置
            </MenuItem>
            <MenuItem key="Logs">
              <template #icon><FileTextOutlined /></template>
              运行日志
            </MenuItem>
            <MenuItem key="Experimental">
              <template #icon><ExperimentOutlined /></template>
              实验性功能
            </MenuItem>
          </MenuItemGroup>
        </Menu>

        <div class="sider-footer">
          <span class="version" :title="versionTitle">{{ version }}</span>
          <button class="logout-btn" type="button" @click="onLogout">
            <LogoutOutlined />
            <span>退出</span>
          </button>
        </div>
      </div>
    </LayoutSider>

    <Drawer
      v-if="isNarrow"
      v-model:open="drawerOpen"
      placement="left"
      :width="260"
      :closable="false"
      :body-style="{ padding: 0 }"
      :header-style="{ display: 'none' }"
      class="mobile-drawer"
    >
      <div class="sider-inner">
        <div class="brand">
          <div class="brand-mark">W</div>
          <div class="brand-meta">
            <div class="brand-name">WindsurfAPI</div>
            <div class="brand-sub">管理控制台</div>
          </div>
        </div>

        <Menu
          mode="inline"
          theme="light"
          :selected-keys="selectedKeys"
          class="side-menu"
          @click="onSelect"
        >
          <MenuItemGroup title="概览">
            <MenuItem key="Overview">
              <template #icon><DashboardOutlined /></template>
              仪表盘
            </MenuItem>
            <MenuItem key="Stats">
              <template #icon><BarChartOutlined /></template>
              统计分析
            </MenuItem>
          </MenuItemGroup>
          <MenuItemGroup title="账号">
            <MenuItem key="LoginTake">
              <template #icon><LoginOutlined /></template>
              登录取号
            </MenuItem>
            <MenuItem key="Accounts">
              <template #icon><TeamOutlined /></template>
              账号管理
            </MenuItem>
            <MenuItem key="Bans">
              <template #icon><AlertOutlined /></template>
              异常监测
            </MenuItem>
          </MenuItemGroup>
          <MenuItemGroup title="系统">
            <MenuItem key="Models">
              <template #icon><DatabaseOutlined /></template>
              模型控制
            </MenuItem>
            <MenuItem key="Proxy">
              <template #icon><SwapOutlined /></template>
              代理配置
            </MenuItem>
            <MenuItem key="Logs">
              <template #icon><FileTextOutlined /></template>
              运行日志
            </MenuItem>
            <MenuItem key="Experimental">
              <template #icon><ExperimentOutlined /></template>
              实验性功能
            </MenuItem>
          </MenuItemGroup>
        </Menu>

        <div class="sider-footer">
          <span class="version" :title="versionTitle">{{ version }}</span>
          <button class="logout-btn" type="button" @click="onLogout">
            <LogoutOutlined />
            <span>退出</span>
          </button>
        </div>
      </div>
    </Drawer>

    <Layout class="main-layout">
      <div v-if="isNarrow" class="mobile-topbar">
        <Button
          type="text"
          shape="circle"
          class="mobile-menu-btn"
          aria-label="打开菜单"
          @click="drawerOpen = true"
        >
          <template #icon><MenuOutlined /></template>
        </Button>
        <span class="mobile-title">{{ currentTitle }}</span>
        <div class="mobile-mark">W</div>
      </div>

      <LayoutContent class="content">
        <RouterView />
      </LayoutContent>
    </Layout>
  </Layout>
</template>

<style scoped>
.shell {
  min-height: 100vh;
  background: var(--color-bg);
}
.sider {
  border-right: 1px solid var(--color-border);
  position: sticky;
  top: 0;
  height: 100vh;
}
.sider-inner {
  display: flex;
  flex-direction: column;
  height: 100%;
}
:deep(.ant-layout-sider-children) {
  display: flex;
  flex-direction: column;
  height: 100%;
}
.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 20px 18px 16px;
  border-bottom: 1px solid var(--color-border);
}
.brand-mark {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  background: linear-gradient(135deg, #4f46e5 0%, #7c3aed 100%);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 800;
  font-size: 16px;
  box-shadow: 0 4px 12px rgba(79, 70, 229, 0.25);
}
.brand-name {
  font-size: 15px;
  font-weight: 700;
  letter-spacing: -0.01em;
  color: var(--color-text);
}
.brand-sub {
  font-size: 12px;
  color: var(--color-text-dim);
  margin-top: 1px;
}
.side-menu {
  flex: 1;
  overflow-y: auto;
  padding: 8px 4px 16px;
}
.sider-footer {
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
  color: var(--color-text-dim);
}
.version {
  font-family: var(--font-mono);
}
.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  font-size: 12px;
  cursor: pointer;
  transition: all 150ms ease;
}
.logout-btn:hover {
  color: var(--color-text);
  border-color: var(--color-border-strong);
  background: var(--color-surface-soft);
}

.main-layout {
  min-width: 0;
}

.content {
  padding: 28px clamp(16px, 3vw, 40px) 40px;
  width: 100%;
  min-height: calc(100vh - 0px);
}

.mobile-topbar {
  position: sticky;
  top: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  gap: 10px;
  height: 52px;
  padding: 0 12px 0 8px;
  background: rgba(255, 255, 255, 0.85);
  backdrop-filter: saturate(1.2) blur(8px);
  border-bottom: 1px solid var(--color-border);
}
.mobile-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--color-text);
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mobile-mark {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  background: linear-gradient(135deg, #4f46e5 0%, #7c3aed 100%);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 800;
  font-size: 13px;
}

@media (max-width: 899.98px) {
  .content {
    padding: 16px 14px 28px;
  }
  .mobile-topbar {
    padding-top: env(safe-area-inset-top, 0);
    height: calc(52px + env(safe-area-inset-top, 0));
  }
}

:deep(.mobile-drawer .ant-drawer-body) {
  background: var(--color-bg);
}
</style>
