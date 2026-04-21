<script setup lang="ts">
import { theme, ConfigProvider } from 'ant-design-vue';
import zhCN from 'ant-design-vue/es/locale/zh_CN';
import 'dayjs/locale/zh-cn';

const themeConfig = {
  algorithm: theme.defaultAlgorithm,
  token: {
    colorPrimary: '#4F46E5',
    colorInfo: '#2563EB',
    colorSuccess: '#15803D',
    colorWarning: '#B45309',
    colorError: '#B91C1C',
    colorBgContainer: '#FFFFFF',
    colorBgLayout: '#FFFFFF',
    colorBgElevated: '#FFFFFF',
    colorText: '#111827',
    colorTextSecondary: '#4B5563',
    colorBorder: '#E5E7EB',
    colorBorderSecondary: '#F1F5F9',
    borderRadius: 8,
    borderRadiusLG: 12,
    borderRadiusSM: 6,
    controlHeight: 36,
    fontFamily:
      "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif",
    fontSize: 14,
    wireframe: false,
  },
  components: {
    Menu: {
      itemBg: '#FFFFFF',
      itemSelectedBg: '#EEF2FF',
      itemSelectedColor: '#4F46E5',
      itemHoverBg: '#F9FAFB',
      itemHeight: 40,
      itemMarginInline: 8,
      subMenuItemBg: '#FFFFFF',
    },
    Card: {
      colorBgContainer: '#FFFFFF',
      colorBorderSecondary: '#E5E7EB',
    },
    Button: {
      primaryShadow: 'none',
      defaultShadow: 'none',
      dangerShadow: 'none',
    },
    Table: {
      headerBg: '#F9FAFB',
      headerColor: '#4B5563',
      headerSplitColor: '#E5E7EB',
      rowHoverBg: '#F9FAFB',
    },
    Input: {
      activeShadow: '0 0 0 3px rgba(79, 70, 229, 0.15)',
    },
    Tag: {
      defaultBg: '#F3F4F6',
      defaultColor: '#374151',
    },
  },
};
</script>

<template>
  <ConfigProvider :theme="themeConfig" :locale="zhCN">
    <RouterView />
  </ConfigProvider>
</template>
