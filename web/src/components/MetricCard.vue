<script setup lang="ts">
type Tone = 'default' | 'primary' | 'success' | 'warning' | 'danger' | 'info';

defineProps<{
  label: string;
  value: string | number;
  description?: string;
  tone?: Tone;
}>();
</script>

<template>
  <div class="metric-card" :data-tone="tone ?? 'default'">
    <div class="metric-head">
      <span class="metric-label">{{ label }}</span>
      <span class="metric-icon">
        <slot name="icon" />
      </span>
    </div>
    <div class="metric-value">{{ value }}</div>
    <div v-if="description" class="metric-desc">{{ description }}</div>
  </div>
</template>

<style scoped>
.metric-card {
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  padding: 18px 20px;
  transition: border-color 150ms ease, box-shadow 150ms ease;
}
.metric-card:hover {
  border-color: var(--color-border-strong);
  box-shadow: var(--shadow-sm);
}
.metric-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}
.metric-label {
  font-size: 12px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--color-text-muted);
}
.metric-icon {
  color: var(--color-text-dim);
  display: inline-flex;
  align-items: center;
}
.metric-value {
  font-size: 26px;
  font-weight: 700;
  letter-spacing: -0.02em;
  line-height: 1.15;
  color: var(--color-text);
}
.metric-desc {
  font-size: 12px;
  color: var(--color-text-muted);
  margin-top: 6px;
}

.metric-card[data-tone='primary'] .metric-value {
  color: var(--color-primary);
}
.metric-card[data-tone='success'] .metric-value {
  color: var(--color-success);
}
.metric-card[data-tone='warning'] .metric-value {
  color: var(--color-warning);
}
.metric-card[data-tone='danger'] .metric-value {
  color: var(--color-danger);
}
.metric-card[data-tone='info'] .metric-value {
  color: var(--color-info);
}
</style>
