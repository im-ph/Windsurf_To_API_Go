import { http } from './request';
import type {
  AccountPatch,
  AccountRow,
  AccountsResponse,
  AddAccountBody,
  ProbeResult,
  RateLimitResult,
  RefreshCreditsResult,
  TierAccess,
} from './types';

export function listAccounts(): Promise<AccountsResponse> {
  return http.get<AccountsResponse>('/accounts');
}

export function addAccount(body: AddAccountBody): Promise<{
  success: boolean;
  account: { id: string; email: string; method: string; status: AccountRow['status'] };
}> {
  return http.post('/accounts', body);
}

export function removeAccount(id: string): Promise<{ success: boolean }> {
  return http.delete(`/accounts/${encodeURIComponent(id)}`);
}

export function patchAccount(id: string, body: AccountPatch): Promise<{ success: boolean }> {
  return http.patch(`/accounts/${encodeURIComponent(id)}`, body);
}

export function probeAll(): Promise<{ success: boolean; results: ProbeResult[] }> {
  return http.post('/accounts/probe-all');
}

export function probeOne(id: string): Promise<{ success: boolean; tier?: string }> {
  return http.post(`/accounts/${encodeURIComponent(id)}/probe`);
}

export function refreshAllCredits(): Promise<{ success: boolean; results: RefreshCreditsResult[] }> {
  return http.post('/accounts/refresh-credits');
}

export function refreshCredits(id: string): Promise<RefreshCreditsResult> {
  return http.post(`/accounts/${encodeURIComponent(id)}/refresh-credits`);
}

export function checkRateLimit(id: string): Promise<RateLimitResult> {
  return http.post(`/accounts/${encodeURIComponent(id)}/rate-limit`);
}

export function refreshAccountToken(id: string): Promise<{
  success: boolean;
  keyChanged: boolean;
  email: string;
}> {
  return http.post(`/accounts/${encodeURIComponent(id)}/refresh-token`);
}

export function getTierAccess(): Promise<TierAccess> {
  return http.get<TierAccess>('/tier-access');
}
