import axios, { AxiosError, type AxiosInstance, type AxiosRequestConfig } from 'axios';
import { message } from 'ant-design-vue';

const DASHBOARD_PASSWORD_KEY = 'windsurfapi:dashboard-password';

export function getStoredPassword(): string {
  return localStorage.getItem(DASHBOARD_PASSWORD_KEY) ?? '';
}

export function setStoredPassword(pw: string): void {
  if (pw) {
    localStorage.setItem(DASHBOARD_PASSWORD_KEY, pw);
  } else {
    localStorage.removeItem(DASHBOARD_PASSWORD_KEY);
  }
}

export class ApiError extends Error {
  readonly status: number;
  readonly payload: unknown;

  constructor(msg: string, status: number, payload?: unknown) {
    super(msg);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload;
  }
}

type UnauthorizedHandler = () => void;
let unauthorizedHandler: UnauthorizedHandler | null = null;

export function onUnauthorized(handler: UnauthorizedHandler): void {
  unauthorizedHandler = handler;
}

function createClient(baseURL: string): AxiosInstance {
  const client = axios.create({
    baseURL,
    timeout: 30_000,
    headers: { 'Content-Type': 'application/json' },
  });

  client.interceptors.request.use((config) => {
    const pw = getStoredPassword();
    if (pw) {
      config.headers.set('X-Dashboard-Password', pw);
    }
    return config;
  });

  client.interceptors.response.use(
    (resp) => resp,
    (err: AxiosError<{ error?: string }>) => {
      if (err.response?.status === 401) {
        unauthorizedHandler?.();
        return Promise.reject(
          new ApiError('未登录或密码错误', 401, err.response?.data),
        );
      }
      const payload = err.response?.data;
      const msg = payload?.error ?? err.message ?? '请求失败';
      return Promise.reject(new ApiError(msg, err.response?.status ?? 0, payload));
    },
  );

  return client;
}

const dashboardClient = createClient('/dashboard/api');
const publicClient = createClient('');

async function request<T>(
  client: AxiosInstance,
  method: AxiosRequestConfig['method'],
  path: string,
  body?: unknown,
  config?: AxiosRequestConfig,
): Promise<T> {
  const resp = await client.request<T>({
    method,
    url: path,
    data: body,
    ...config,
  });
  return resp.data;
}

export const http = {
  get<T>(path: string, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(dashboardClient, 'GET', path, undefined, config);
  },
  post<T, B = unknown>(path: string, body?: B, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(dashboardClient, 'POST', path, body, config);
  },
  put<T, B = unknown>(path: string, body?: B, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(dashboardClient, 'PUT', path, body, config);
  },
  patch<T, B = unknown>(path: string, body?: B, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(dashboardClient, 'PATCH', path, body, config);
  },
  delete<T>(path: string, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(dashboardClient, 'DELETE', path, undefined, config);
  },
};

export const publicHttp = {
  get<T>(path: string, config?: AxiosRequestConfig): Promise<T> {
    return request<T>(publicClient, 'GET', path, undefined, config);
  },
};

export function toast(err: unknown, fallback = '操作失败'): void {
  if (err instanceof ApiError) {
    message.error(err.message || fallback);
    return;
  }
  if (err instanceof Error) {
    message.error(err.message || fallback);
    return;
  }
  message.error(fallback);
}
