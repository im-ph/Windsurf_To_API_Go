// N19 — Minimal Vue 3 i18n composable.
//
// Why a hand-roll instead of vue-i18n? The dashboard SPA is single-static-
// binary embedded; vue-i18n adds ~30 KB gzipped and a Vue plugin install
// step that the existing main.ts bootstrap doesn't have. We only need
// flat-key lookup with a single fallback chain (zh-CN → en) and runtime
// locale switching, which is ~80 lines of Composition-API code.
//
// Locale files live in src/i18n/{locale}.json and are also served by the
// backend at GET /dashboard/api/i18n/:locale so the SPA can hot-swap
// languages without re-shipping the bundle (the en.json + zh-CN.json
// JSON files are also bundled at build time as the default fallback).
//
// Usage:
//   const { t, locale, setLocale } = useI18n();
//   <h1>{{ t('overview.title') }}</h1>
//   <button @click="setLocale('en')">English</button>
//
// Variable interpolation: t('errors.rateLimit', { model: 'opus-4.7', seconds: 30 })

import { computed, ref, type Ref, type ComputedRef } from 'vue';
import zhCN from '../i18n/zh-CN.json';
import en from '../i18n/en.json';

type LocaleData = Record<string, any>;
type SupportedLocale = 'zh-CN' | 'en';

const STORAGE_KEY = 'windsurfapi:locale';

const bundled: Record<SupportedLocale, LocaleData> = {
  'zh-CN': zhCN as LocaleData,
  en: en as LocaleData,
};

// Module-singleton state so every component sees the same locale change.
const _locale: Ref<SupportedLocale> = ref(detectInitialLocale());
const _override: Ref<LocaleData | null> = ref(null);

function detectInitialLocale(): SupportedLocale {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved === 'zh-CN' || saved === 'en') return saved;
  } catch { /* noop */ }
  // Browser fallback. Anything starting with `zh` → zh-CN; everything else → en.
  if (typeof navigator !== 'undefined') {
    const lang = (navigator.language || 'en').toLowerCase();
    if (lang.startsWith('zh')) return 'zh-CN';
  }
  return 'en';
}

// resolveKey walks "a.b.c" through the nested locale tree.
function resolveKey(data: LocaleData, key: string): unknown {
  const parts = key.split('.');
  let cur: any = data;
  for (const p of parts) {
    if (cur == null || typeof cur !== 'object') return undefined;
    cur = cur[p];
  }
  return cur;
}

// interpolate replaces {var} placeholders with values from params.
function interpolate(s: string, params?: Record<string, string | number>): string {
  if (!params) return s;
  return s.replace(/\{(\w+)\}/g, (_, k) => {
    const v = params[k];
    return v === undefined ? '{' + k + '}' : String(v);
  });
}

export interface I18nApi {
  locale: ComputedRef<SupportedLocale>;
  t: (key: string, params?: Record<string, string | number>) => string;
  setLocale: (locale: SupportedLocale) => void;
  // loadFromBackend fetches a fresh locale JSON from
  // /dashboard/api/i18n/:locale — used when an operator updates the
  // bundled locale files on disk and wants the running SPA to pick up
  // the change without a rebuild.
  loadFromBackend: (locale: SupportedLocale) => Promise<void>;
}

export function useI18n(): I18nApi {
  const locale = computed(() => _locale.value);

  const t = (key: string, params?: Record<string, string | number>): string => {
    // Override (loaded from backend) wins over bundled.
    if (_override.value) {
      const v = resolveKey(_override.value, key);
      if (typeof v === 'string') return interpolate(v, params);
    }
    const v = resolveKey(bundled[_locale.value], key);
    if (typeof v === 'string') return interpolate(v, params);
    // Fallback chain: zh-CN ↔ en.
    const fallbackLocale: SupportedLocale = _locale.value === 'zh-CN' ? 'en' : 'zh-CN';
    const fb = resolveKey(bundled[fallbackLocale], key);
    if (typeof fb === 'string') return interpolate(fb, params);
    // Last resort: return the key itself so missing strings are visible
    // in the UI for translator workflow.
    return key;
  };

  const setLocale = (l: SupportedLocale): void => {
    if (l !== 'zh-CN' && l !== 'en') return;
    _locale.value = l;
    _override.value = null;
    try { localStorage.setItem(STORAGE_KEY, l); } catch { /* noop */ }
  };

  const loadFromBackend = async (l: SupportedLocale): Promise<void> => {
    try {
      const resp = await fetch(`/dashboard/api/i18n/${l}`, {
        headers: { Accept: 'application/json' },
        credentials: 'omit',
      });
      if (!resp.ok) return;
      const data = (await resp.json()) as LocaleData;
      if (data && typeof data === 'object') {
        _override.value = data;
        _locale.value = l;
      }
    } catch {
      // Network error → keep bundled locale; lookup falls through to it.
    }
  };

  return { locale, t, setLocale, loadFromBackend };
}
