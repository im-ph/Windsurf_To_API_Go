import { http } from './request';
import type { StatsPayload } from './types';

export function getStats(): Promise<StatsPayload> {
  return http.get<StatsPayload>('/stats');
}

export function resetStats(): Promise<{ success: boolean }> {
  return http.delete('/stats');
}
