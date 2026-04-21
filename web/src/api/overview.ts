import { http } from './request';
import type { OverviewPayload } from './types';

export function getOverview(): Promise<OverviewPayload> {
  return http.get<OverviewPayload>('/overview');
}

export function restartLanguageServer(): Promise<{ success: boolean; message?: string }> {
  return http.post('/langserver/restart', { confirm: true });
}
