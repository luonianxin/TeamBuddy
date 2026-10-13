<script setup lang="ts">
import { ConfigProvider } from 'ant-design-vue'
import { defineAsyncComponent, onMounted, onUnmounted } from 'vue'
import { themeConfig } from '@/config/theme'
import { stopLocaleSync } from '@/i18n'
import { useUpdateStore } from '@/stores/update'
import { useLocale } from '@/composables/useLocale'
import { isWindowsDesktopRuntime } from '@/composables/useDesktop'

const { antLocale } = useLocale()
const updateStore = useUpdateStore()
const DesktopUpdateModal = defineAsyncComponent(() => import('@/components/DesktopUpdateModal.vue'))

// Windows 原生标题栏按钮位于 Web 内容之上；给所有 Portal 弹层提供统一安全区标记。
onMounted(() => {
  document.body.classList.toggle('goteams-windows-desktop', isWindowsDesktopRuntime())
  void updateStore.check(false).catch(() => {})
})

onUnmounted(() => {
  document.body.classList.remove('goteams-windows-desktop')
  stopLocaleSync()
  updateStore.dispose()
})
</script>

<template>
  <ConfigProvider :locale="antLocale" :theme="themeConfig">
    <RouterView />
    <DesktopUpdateModal v-if="updateStore.modalOpen" />
  </ConfigProvider>
</template>
