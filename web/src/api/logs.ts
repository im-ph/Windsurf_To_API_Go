import { http, getStoredPassword } from './request';
import type { LogsListResponse, LogEntry } from './types';

export function listLogs(params: { since?: number; level?: string } = {}): Promise<LogsListResponse> {
  const q = new URLSearchParams();
  if (params.since) q.set('since', String(params.since));
  if (params.level) q.set('level', params.level);
  const tail = q.toString();
  return http.get<LogsListResponse>(`/logs${tail ? `?${tail}` : ''}`);
}

export interface LogsStreamHandlers {
  onEntry: (e: LogEntry) => void;
  onError?: (ev: Event) => void;
  onOpen?: (ev: Event) => void;
}

export function openLogsStream(handlers: LogsStreamHandlers): () => void {
  // EventSource can't set headers, so the password rides as a query param.
  // Server trusts dashboard-side TLS; for local/self-host this is acceptable.
  const pw = getStoredPassword();
  const url = `/dashboard/api/logs/stream${pw ? `?pw=${encodeURIComponent(pw)}` : ''}`;
  const es = new EventSource(url);
  es.onmessage = (ev) => {
    try {
      handlers.onEntry(JSON.parse(ev.data) as LogEntry);
    } catch {
      /* ignore malformed frame */
    }
  };
  if (handlers.onError) es.onerror = handlers.onError;
  if (handlers.onOpen) es.onopen = handlers.onOpen;
  return () => es.close();
}
