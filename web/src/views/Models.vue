<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import {
  Button,
  Card,
  Input,
  Radio,
  RadioGroup,
  Select,
  SelectOption,
  Tag,
  message,
  Empty,
} from 'ant-design-vue';
import { SearchOutlined, ReloadOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import {
  addModelAccess,
  getModelAccess,
  listModels,
  putModelAccess,
  removeModelAccess,
} from '@/api/models';
import type { ModelAccessConfig, ModelAccessMode, ModelInfo } from '@/api/types';
import { toast } from '@/api/request';

const models = ref<ModelInfo[]>([]);
const config = ref<ModelAccessConfig>({ mode: 'all', list: [] });
const loading = ref(false);
const search = ref('');
const providerFilter = ref<string>('');

const providers = computed(() => {
  const set = new Set<string>();
  models.value.forEach((m) => set.add(m.provider));
  return Array.from(set).sort();
});

const filtered = computed(() => {
  const s = search.value.trim().toLowerCase();
  return models.value.filter((m) => {
    if (providerFilter.value && m.provider !== providerFilter.value) return false;
    if (!s) return true;
    return m.id.toLowerCase().includes(s) || m.name.toLowerCase().includes(s);
  });
});

const grouped = computed(() => {
  const g: Record<string, ModelInfo[]> = {};
  filtered.value.forEach((m) => {
    if (!g[m.provider]) g[m.provider] = [];
    g[m.provider].push(m);
  });
  return g;
});

// Early Go builds persisted model-access.json without initialising the list
// to an empty slice, so GET /model-access can return `list: null`. Normalise
// so the template can call `.length` without a runtime error (which silently
// aborts the <Card v-if> render and explains "white/black-list shows nothing").
function normalise(cfg: ModelAccessConfig): ModelAccessConfig {
  return { mode: cfg.mode, list: Array.isArray(cfg.list) ? cfg.list : [] };
}

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [ms, cfg] = await Promise.all([listModels(), getModelAccess()]);
    models.value = ms.models ?? [];
    config.value = normalise(cfg);
  } catch (err) {
    toast(err, '加载失败');
  } finally {
    loading.value = false;
  }
}

async function onModeChange(mode: ModelAccessMode): Promise<void> {
  try {
    const r = await putModelAccess({ mode, list: config.value.list });
    config.value = normalise(r.config);
    message.success('已更新');
  } catch (err) {
    toast(err, '更新失败');
  }
}

function handleModeChange(e: { target: { value?: unknown } }): void {
  const v = e.target.value;
  if (v === 'all' || v === 'allowlist' || v === 'blocklist') onModeChange(v);
}

function isSelected(id: string): boolean {
  return config.value.list.includes(id);
}

async function toggle(id: string): Promise<void> {
  try {
    const r = isSelected(id) ? await removeModelAccess(id) : await addModelAccess(id);
    config.value = normalise(r.config);
  } catch (err) {
    toast(err, '操作失败');
  }
}

onMounted(load);

const modeHint = computed(() => {
  switch (config.value.mode) {
    case 'allowlist':
      return '只有白名单中的模型可被调用';
    case 'blocklist':
      return '黑名单中的模型将被屏蔽';
    default:
      return '所有模型都可被调用';
  }
});
</script>

<template>
  <PageHeader title="模型控制" subtitle="配置模型访问策略">
    <template #actions>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
    </template>
  </PageHeader>

  <Card size="small" title="访问策略" style="margin-bottom: 16px">
    <RadioGroup
      :value="config.mode"
      button-style="solid"
      @change="handleModeChange"
    >
      <Radio value="all">全部允许</Radio>
      <Radio value="allowlist">白名单</Radio>
      <Radio value="blocklist">黑名单</Radio>
    </RadioGroup>
    <p class="hint">{{ modeHint }}</p>
  </Card>

  <Card
    v-if="config.mode !== 'all'"
    size="small"
    :title="config.mode === 'allowlist' ? '白名单' : '黑名单'"
  >
    <div class="filters">
      <Input
        v-model:value="search"
        placeholder="搜索模型"
        allow-clear
        style="max-width: 280px"
      >
        <template #prefix><SearchOutlined /></template>
      </Input>
      <Select v-model:value="providerFilter" style="width: 180px" allow-clear placeholder="全部供应商">
        <SelectOption v-for="p in providers" :key="p" :value="p">{{ p }}</SelectOption>
      </Select>
      <span class="counter">
        已选 <strong>{{ config.list.length }}</strong> / {{ models.length }}
      </span>
    </div>

    <Empty v-if="!filtered.length" description="没有匹配的模型" />

    <div v-for="(items, provider) in grouped" :key="provider" class="provider">
      <div class="provider-title">{{ provider }}</div>
      <div class="chips">
        <Tag
          v-for="m in items"
          :key="m.id"
          :color="isSelected(m.id) ? 'processing' : undefined"
          class="chip clickable"
          @click="toggle(m.id)"
        >
          {{ m.id }}
        </Tag>
      </div>
    </div>
  </Card>
</template>

<style scoped>
.hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--color-text-muted);
}
.filters {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  align-items: center;
  margin-bottom: 18px;
}
.counter {
  font-size: 12px;
  color: var(--color-text-muted);
}
.counter strong {
  color: var(--color-primary);
  font-weight: 700;
}
.provider {
  margin-bottom: 18px;
}
.provider-title {
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: var(--color-text-dim);
  font-weight: 600;
  margin-bottom: 8px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: 12px;
  padding: 4px 10px;
  transition: all 150ms ease;
}
.chip:hover {
  border-color: var(--color-primary);
}
</style>
