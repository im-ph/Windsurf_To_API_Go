import { http } from './request';
import type { ModelAccessConfig, ModelInfo } from './types';

export function listModels(): Promise<{ models: ModelInfo[] }> {
  return http.get<{ models: ModelInfo[] }>('/models');
}

export function getModelAccess(): Promise<ModelAccessConfig> {
  return http.get<ModelAccessConfig>('/model-access');
}

export function putModelAccess(cfg: ModelAccessConfig): Promise<{ success: boolean; config: ModelAccessConfig }> {
  return http.put('/model-access', cfg);
}

export function addModelAccess(model: string): Promise<{ success: boolean; config: ModelAccessConfig }> {
  return http.post('/model-access/add', { model });
}

export function removeModelAccess(model: string): Promise<{ success: boolean; config: ModelAccessConfig }> {
  return http.post('/model-access/remove', { model });
}
