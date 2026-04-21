import { http } from './request';
import type { SelfUpdateResult, SelfUpdateStatus } from './types';

export function checkUpdate(): Promise<SelfUpdateStatus> {
  return http.get<SelfUpdateStatus>('/self-update/check');
}

export function applyUpdate(forceReset = false): Promise<SelfUpdateResult> {
  return http.post('/self-update', { forceReset });
}

export function getCache(): Promise<{
  size: number;
  maxSize: number;
  hits: number;
  misses: number;
  hitRate: string;
}> {
  return http.get('/cache');
}

export function clearCache(): Promise<{ success: boolean }> {
  return http.delete('/cache');
}

export function getConfig(): Promise<{
  port: number;
  defaultModel: string;
  maxTokens: number;
  logLevel: string;
  lsBinaryPath: string;
  lsPort: number;
  codeiumApiUrl: string;
  hasApiKey: boolean;
  hasDashboardPassword: boolean;
}> {
  return http.get('/config');
}
