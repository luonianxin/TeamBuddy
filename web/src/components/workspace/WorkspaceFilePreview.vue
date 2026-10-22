<template>
  <a-drawer
    :open="open"
    :width="drawerWidth"
    :closable="false"
    :body-style="{ padding: 0, overflow: 'hidden' }"
    root-class-name="workspace-preview-drawer"
    @close="emit('close')"
  >
    <div class="preview-shell">
      <header class="preview-header">
        <div class="preview-title">
          <FileOutlined aria-hidden="true" />
          <span :title="content?.path || fileName">{{ fileName }}</span>
          <em>{{ t('workflows.task.workspaceFiles.readOnly') }}</em>
        </div>
        <button
          type="button"
          class="icon-button"
          :title="t('workflows.task.workspaceFiles.closePreview')"
          :aria-label="t('workflows.task.workspaceFiles.closePreview')"
          @click="emit('close')"
        >
          <CloseOutlined />
        </button>
      </header>
      <main class="preview-content">
        <div
          v-if="loading"
          class="preview-state"
        >
          <a-spin />
        </div>
        <div
          v-else-if="error"
          class="preview-state"
        >
          <a-alert
            type="error"
            show-icon
            :message="error"
          />
        </div>
        <a-empty
          v-else-if="!content"
          :description="t('workflows.task.workspaceFiles.chooseFile')"
        />
        <div
          v-else-if="content.encoding === 'base64'"
          class="image-preview"
        >
          <img
            :src="imageDataUrl"
            :alt="content.name"
          />
        </div>
        <MarkdownPreview
          v-else-if="isMarkdown"
          :content="content.content"
          :empty-text="t('workflows.task.workspaceFiles.emptyFile')"
        />
        <pre
          v-else
          class="text-preview"
        ><code>{{ content.content }}</code></pre>
      </main>
    </div>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { CloseOutlined, FileOutlined } from '@ant-design/icons-vue'
import MarkdownPreview from '@/components/MarkdownPreview.vue'
import { useAppI18n } from '@/i18n'
import type { WorkspaceFileContent } from '@/types/workspace-files'

const props = defineProps<{
  open: boolean
  loading: boolean
  error: string
  content?: WorkspaceFileContent
  fileName?: string
}>()
const emit = defineEmits<{ close: [] }>()
const { t } = useAppI18n()

const drawerWidth = computed(() => Math.min(960, Math.max(320, window.innerWidth - 48)))
const isMarkdown = computed(() => ['md', 'markdown'].includes(props.content?.file_type || ''))
const imageDataUrl = computed(() => {
  if (!props.content || props.content.encoding !== 'base64') return ''
  return `data:${props.content.mime_type || 'application/octet-stream'};base64,${props.content.content}`
})
</script>

<style scoped>
.preview-shell {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  background: #fff;
}

.preview-header {
  display: flex;
  min-height: 48px;
  flex: 0 0 48px;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 0 16px;
  border-bottom: 1px solid #f0f0f0;
}

.preview-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.preview-title span {
  overflow: hidden;
  color: #262626;
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.preview-title em {
  flex: 0 0 auto;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  padding: 0 5px;
  color: #8c8c8c;
  font-size: 11px;
  font-style: normal;
  line-height: 18px;
}

.icon-button {
  display: inline-flex;
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  color: #595959;
  background: transparent;
  cursor: pointer;
}

.icon-button:hover {
  background: #f5f5f5;
}
.icon-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.preview-content {
  min-height: 0;
  flex: 1;
  overflow: auto;
}

.preview-state {
  display: flex;
  min-height: 240px;
  align-items: center;
  justify-content: center;
  padding: 24px;
}

.preview-state :deep(.ant-alert) {
  width: min(560px, 100%);
}

.text-preview {
  min-width: max-content;
  min-height: 100%;
  box-sizing: border-box;
  margin: 0;
  padding: 20px 24px 48px;
  color: #262626;
  background: #fff;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', monospace;
  font-size: 13px;
  line-height: 1.65;
  tab-size: 2;
  white-space: pre;
}

.image-preview {
  display: flex;
  min-height: 100%;
  align-items: flex-start;
  justify-content: center;
  padding: 24px;
  background: #f5f6f8;
}

.image-preview img {
  max-width: 100%;
  height: auto;
  border: 1px solid #e5e7eb;
  background: #fff;
  object-fit: contain;
}
</style>
