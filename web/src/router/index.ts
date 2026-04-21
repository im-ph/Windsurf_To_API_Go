import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import { onUnauthorized } from '@/api/request';

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/Login.vue'),
    meta: { title: '控制台登录', public: true },
  },
  {
    path: '/',
    component: () => import('@/layouts/BasicLayout.vue'),
    redirect: '/overview',
    children: [
      {
        path: 'overview',
        name: 'Overview',
        component: () => import('@/views/Overview.vue'),
        meta: { title: '仪表盘' },
      },
      {
        path: 'stats',
        name: 'Stats',
        component: () => import('@/views/Stats.vue'),
        meta: { title: '统计分析' },
      },
      {
        path: 'login-take',
        name: 'LoginTake',
        component: () => import('@/views/LoginTake.vue'),
        meta: { title: '登录取号' },
      },
      {
        path: 'accounts',
        name: 'Accounts',
        component: () => import('@/views/Accounts.vue'),
        meta: { title: '账号管理' },
      },
      {
        path: 'bans',
        name: 'Bans',
        component: () => import('@/views/Bans.vue'),
        meta: { title: '异常监测' },
      },
      {
        path: 'models',
        name: 'Models',
        component: () => import('@/views/Models.vue'),
        meta: { title: '模型控制' },
      },
      {
        path: 'proxy',
        name: 'Proxy',
        component: () => import('@/views/Proxy.vue'),
        meta: { title: '代理配置' },
      },
      {
        path: 'logs',
        name: 'Logs',
        component: () => import('@/views/Logs.vue'),
        meta: { title: '运行日志' },
      },
      {
        path: 'experimental',
        name: 'Experimental',
        component: () => import('@/views/Experimental.vue'),
        meta: { title: '实验性功能' },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/overview' },
];

const router = createRouter({
  history: createWebHistory('/dashboard/'),
  routes,
});

router.beforeEach(async (to) => {
  const auth = useAuthStore();
  if (!auth.ready) {
    try {
      await auth.probe();
    } catch {
      // On network error fall through; downstream API calls will surface it.
    }
  }
  if (to.meta.public) return true;
  if (!auth.authenticated) return { name: 'Login', query: { redirect: to.fullPath } };
  return true;
});

router.afterEach((to) => {
  const base = 'WindsurfAPI 控制台';
  document.title = to.meta.title ? `${to.meta.title as string} · ${base}` : base;
});

onUnauthorized(() => {
  const auth = useAuthStore();
  auth.authenticated = false;
  if (router.currentRoute.value.name !== 'Login') {
    router.replace({ name: 'Login', query: { redirect: router.currentRoute.value.fullPath } });
  }
});

export default router;
