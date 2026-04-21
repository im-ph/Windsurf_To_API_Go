import { http } from './request';
import type {
  ExperimentalFlags,
  ExperimentalResponse,
  IdentityPromptsResponse,
} from './types';

export function getExperimental(): Promise<ExperimentalResponse> {
  return http.get<ExperimentalResponse>('/experimental');
}

export function patchExperimental(patch: Partial<ExperimentalFlags>): Promise<{
  success: boolean;
  flags: ExperimentalFlags;
}> {
  return http.put('/experimental', patch);
}

export function clearConversationPool(): Promise<{ success: boolean; cleared: number }> {
  return http.delete('/experimental/conversation-pool');
}

export function getIdentityPrompts(): Promise<IdentityPromptsResponse> {
  return http.get<IdentityPromptsResponse>('/identity-prompts');
}

export function putIdentityPrompts(patch: Record<string, string>): Promise<{
  success: boolean;
  prompts: Record<string, string>;
}> {
  return http.put('/identity-prompts', patch);
}

export function resetIdentityPrompt(provider: string): Promise<{
  success: boolean;
  prompts: Record<string, string>;
}> {
  return http.delete(`/identity-prompts/${encodeURIComponent(provider)}`);
}
