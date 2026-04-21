import { http, publicHttp } from './request';
import type { AuthProbe, HealthInfo } from './types';

export function probeAuth(): Promise<AuthProbe> {
  return http.get<AuthProbe>('/auth');
}

export function getHealth(): Promise<HealthInfo> {
  return publicHttp.get<HealthInfo>('/health');
}
