<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import {
  Button,
  Card,
  Popconfirm,
  Switch,
  Textarea,
  message,
  Spin,
} from 'ant-design-vue';
import { ReloadOutlined, RollbackOutlined } from '@ant-design/icons-vue';
import PageHeader from '@/components/PageHeader.vue';
import MetricCard from '@/components/MetricCard.vue';
import {
  clearConversationPool,
  getExperimental,
  getIdentityPrompts,
  patchExperimental,
  putIdentityPrompts,
  resetIdentityPrompt,
} from '@/api/experimental';
import type { ExperimentalFlags, ConversationPoolSnapshot, IdentityPromptMap } from '@/api/types';
import { toast } from '@/api/request';

const loading = ref(true);
const flags = reactive<ExperimentalFlags>({
  cascadeConversationReuse: false,
  modelIdentityPrompt: true,
});
const pool = ref<ConversationPoolSnapshot>({ size: 0 });
const prompts = ref<IdentityPromptMap>({});
const defaults = ref<IdentityPromptMap>({});
const editing = reactive<Record<string, string>>({});
const savingProvider = ref<string | null>(null);

async function load(): Promise<void> {
  loading.value = true;
  try {
    const [exp, id] = await Promise.all([getExperimental(), getIdentityPrompts()]);
    Object.assign(flags, exp.flags);
    pool.value = exp.conversationPool;
    prompts.value = id.prompts;
    defaults.value = id.defaults;
    // Seed editor state
    for (const k of Object.keys(defaults.value)) {
      editing[k] = id.prompts[k] ?? '';
    }
  } catch (err) {
    toast(err, '加载失败');
  } finally {
    loading.value = false;
  }
}

async function onToggle(key: keyof ExperimentalFlags, val: unknown): Promise<void> {
  try {
    const r = await patchExperimental({ [key]: !!val } as Partial<ExperimentalFlags>);
    Object.assign(flags, r.flags);
    message.success('已更新');
  } catch (err) {
    toast(err, '更新失败');
  }
}

async function onClearPool(): Promise<void> {
  try {
    const r = await clearConversationPool();
    pool.value = { size: 0 };
    message.success(`已清空 ${r.cleared} 条`);
  } catch (err) {
    toast(err, '操作失败');
  }
}

async function onSavePrompt(provider: string): Promise<void> {
  savingProvider.value = provider;
  try {
    const r = await putIdentityPrompts({ [provider]: editing[provider] });
    prompts.value = r.prompts;
    message.success('已保存');
  } catch (err) {
    toast(err, '保存失败');
  } finally {
    savingProvider.value = null;
  }
}

async function onResetPrompt(provider: string): Promise<void> {
  try {
    const r = await resetIdentityPrompt(provider);
    prompts.value = r.prompts;
    editing[provider] = r.prompts[provider] ?? defaults.value[provider] ?? '';
    message.success('已重置为默认');
  } catch (err) {
    toast(err, '操作失败');
  }
}

onMounted(load);
</script>

<template>
  <PageHeader title="实验性功能" subtitle="尚未稳定的优化项；异常时可随时关闭">
    <template #actions>
      <Button :loading="loading" @click="load">
        <template #icon><ReloadOutlined /></template>
        刷新
      </Button>
    </template>
  </PageHeader>

  <Spin :spinning="loading">
    <Card size="small" title="Cascade 对话复用" style="margin-bottom: 16px">
      <p class="desc">
        多轮对话时复用同一个 <code>cascade_id</code>，只把最新一条 user 消息发给 Windsurf。命中时可显著降低 TTFB 与上传体积；未命中会回退到新会话。<strong>需要客户端保留完整历史并按顺序追加</strong>（如 new-api / OpenWebUI）。
      </p>
      <div class="toggle-row">
        <Switch
          :checked="flags.cascadeConversationReuse"
          @change="(v: unknown) => onToggle('cascadeConversationReuse', v)"
        />
        <div>
          <div class="toggle-title">启用 Cascade 对话复用</div>
          <div class="text-muted">默认关闭。开启后对当前对话池立即生效</div>
        </div>
      </div>

      <div class="metrics-grid" style="margin-top: 20px">
        <MetricCard tone="info" label="对话池大小" :value="pool.size" />
        <MetricCard label="命中" :value="pool.hits ?? 0" />
        <MetricCard label="未命中" :value="pool.misses ?? 0" />
      </div>

      <div style="margin-top: 16px">
        <Popconfirm title="清空对话池？下一次请求将新建会话。" @confirm="onClearPool">
          <Button>清空对话池</Button>
        </Popconfirm>
      </div>
    </Card>

    <Card size="small" title="模型身份注入">
      <p class="desc">
        开启后在每个请求前注入一条系统提示，覆盖 Cascade 自带的 Windsurf 身份。下方每个厂商的模板可自定义，<code>{model}</code> 会被替换成请求模型名（如 <code>claude-opus-4.6</code>）。
      </p>
      <div class="toggle-row">
        <Switch
          :checked="flags.modelIdentityPrompt"
          @change="(v: unknown) => onToggle('modelIdentityPrompt', v)"
        />
        <div>
          <div class="toggle-title">启用模型身份注入</div>
          <div class="text-muted">默认开启；关闭后下面的模板不起作用</div>
        </div>
      </div>

      <div class="prompts">
        <div
          v-for="(def, provider) in defaults"
          :key="String(provider)"
          class="prompt-card"
        >
          <div class="prompt-head">
            <span class="prompt-provider">{{ String(provider) }}</span>
            <div class="prompt-actions">
              <Button size="small" @click="onResetPrompt(String(provider))">
                <template #icon><RollbackOutlined /></template>
                恢复默认
              </Button>
              <Button
                type="primary"
                size="small"
                :loading="savingProvider === String(provider)"
                @click="onSavePrompt(String(provider))"
              >
                保存
              </Button>
            </div>
          </div>
          <Textarea
            v-model:value="editing[String(provider)]"
            :rows="4"
            :placeholder="def"
            show-count
            :maxlength="2000"
          />
        </div>
      </div>
    </Card>
  </Spin>
</template>

<style scoped>
.desc {
  font-size: 13px;
  color: var(--color-text-muted);
  line-height: 1.6;
  margin: 0 0 16px;
}
.desc code {
  font-family: var(--font-mono);
  background: var(--color-surface-alt);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
}
.toggle-row {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 12px 16px;
  background: var(--color-surface-soft);
  border-radius: var(--radius);
}
.toggle-title {
  font-weight: 600;
  color: var(--color-text);
}
.prompts {
  display: grid;
  gap: 14px;
  margin-top: 18px;
}
.prompt-card {
  padding: 14px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  background: var(--color-surface);
}
.prompt-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.prompt-provider {
  font-weight: 600;
  font-size: 13px;
  text-transform: capitalize;
}
.prompt-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
</style>
