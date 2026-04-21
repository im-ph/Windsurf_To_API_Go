import { http } from './request';
import type { ProxyConfig, ProxySpec, TestProxyBody, TestProxyResult } from './types';

export function getProxy(): Promise<ProxyConfig> {
  return http.get<ProxyConfig>('/proxy');
}

export function setGlobalProxy(spec: ProxySpec): Promise<{ success: boolean; config: ProxyConfig }> {
  return http.put('/proxy/global', spec);
}

export function clearGlobalProxy(): Promise<{ success: boolean }> {
  return http.delete('/proxy/global');
}

export function setAccountProxy(id: string, spec: ProxySpec): Promise<{ success: boolean }> {
  return http.put(`/proxy/accounts/${encodeURIComponent(id)}`, spec);
}

export function clearAccountProxy(id: string): Promise<{ success: boolean }> {
  return http.delete(`/proxy/accounts/${encodeURIComponent(id)}`);
}

export function testProxy(body: TestProxyBody): Promise<TestProxyResult> {
  return http.post('/test-proxy', body);
}
