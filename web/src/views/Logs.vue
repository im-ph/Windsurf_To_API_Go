<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { Button, Input, Select, SelectOption, Switch, Tag } from 'ant-design-vue';
import { DeleteOutlined, SearchOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import { listLogs, openLogsStream } from '@/api/logs';
import type { LogEntry, LogLevel } from '@/api/types';
import { toast } from '@/api/request';

const entries = ref<LogEntry[]>([]);
const levelFilter = ref<LogLevel | ''>('');
const search = ref('');
const autoScroll = ref(true);
const maxBuffer = 1000;
const containerRef = ref<HTMLElement | null>(null);
const closeStream = ref<(() => void) | null>(null);

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase();
  return entries.value.filter((e) => {
    if (levelFilter.value && e.level !== levelFilter.value) return false;
    if (q && !e.msg.toLowerCase().includes(q)) return false;
    return true;
  });
});

function push(e: LogEntry): void {
  entries.value.push(e);
  if (entries.value.length > maxBuffer) {
    entries.value.splice(0, entries.value.length - maxBuffer);
  }
}

async function bootstrap(): Promise<void> {
  try {
    const r = await listLogs();
    entries.value = r.logs ?? [];
  } catch (err) {
    toast(err, '加载日志失败');
  }
  closeStream.value = openLogsStream({
    onEntry: push,
    onError: () => {
      // Let EventSource retry; no toast noise.
    },
  });
}

function clearView(): void {
  entries.value = [];
}

function fmtTs(ts: number): string {
  const d = new Date(ts);
  return d.toLocaleTimeString('zh-CN', { hour12: false }) +
    '.' +
    String(d.getMilliseconds()).padStart(3, '0');
}

function levelColor(lvl: LogLevel): string {
  switch (lvl) {
    case 'error':
      return 'error';
    case 'warn':
      return 'warning';
    case 'info':
      return 'processing';
    default:
      return 'default';
  }
}

watch(filtered, async () => {
  if (!autoScroll.value) return;
  await nextTick();
  const el = containerRef.value;
  if (el) el.scrollTop = el.scrollHeight;
});

onMounted(bootstrap);
onUnmounted(() => {
  closeStream.value?.();
});
</script>

<template>
  <PageHeader title="运行日志" subtitle="通过 SSE 实时流式接收服务端日志">
    <template #actions>
      <Button @click="clearView">
        <template #icon><DeleteOutlined /></template>
        清空视图
      </Button>
    </template>
  </PageHeader>

  <div class="toolbar">
    <Select v-model:value="levelFilter" style="width: 140px">
      <SelectOption value="">全部日志</SelectOption>
      <SelectOption value="debug">Debug</SelectOption>
      <SelectOption value="info">Info</SelectOption>
      <SelectOption value="warn">Warn</SelectOption>
      <SelectOption value="error">Error</SelectOption>
    </Select>
    <Input v-model:value="search" placeholder="搜索日志内容" allow-clear style="flex: 1; min-width: 220px">
      <template #prefix><SearchOutlined /></template>
    </Input>
    <div class="scroll-toggle">
      <Switch v-model:checked="autoScroll" size="small" />
      <span>自动滚动</span>
    </div>
    <span class="counter">{{ filtered.length }} / {{ entries.length }} 条</span>
  </div>

  <div ref="containerRef" class="log-container">
    <div v-if="!filtered.length" class="empty">等待日志…</div>
    <div
      v-for="(e, idx) in filtered"
      :key="idx"
      class="log-row"
      :class="`level-${e.level}`"
    >
      <span class="ts">{{ fmtTs(e.ts) }}</span>
      <Tag :color="levelColor(e.level)" class="level-tag">{{ e.level }}</Tag>
      <span class="msg">{{ e.msg }}</span>
    </div>
  </div>
</template>

<style scoped>
.toolbar {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: 14px;
}
.scroll-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--color-text-muted);
}
.counter {
  font-size: 12px;
  color: var(--color-text-muted);
  margin-left: auto;
}
.log-container {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  /* Fill the remaining viewport below the page header + filter toolbar
     (~200px used by those). Safari needs both height and -webkit. */
  height: calc(100vh - 200px);
  min-height: 360px;
  overflow-y: auto;
  padding: 8px 0;
}

@media (max-width: 899.98px) {
  .log-container {
    height: calc(100vh - 220px);
  }
}
.log-row {
  padding: 3px 16px;
  display: flex;
  gap: 10px;
  align-items: baseline;
  word-break: break-all;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.6;
}
.log-row:hover {
  background: var(--color-surface-soft);
}
.ts {
  color: var(--color-text-dim);
  flex-shrink: 0;
  font-size: 11px;
}
.level-tag {
  text-transform: uppercase;
  flex-shrink: 0;
  margin-right: 0;
  font-size: 10px;
}
.level-error {
  background: var(--color-danger-soft);
}
.msg {
  flex: 1;
  color: var(--color-text);
}
.empty {
  text-align: center;
  padding: 32px 0;
  color: var(--color-text-dim);
  font-size: 13px;
}
</style>
