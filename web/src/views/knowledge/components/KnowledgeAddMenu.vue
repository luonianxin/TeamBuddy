<template>
  <a-menu @click="emit('select')">
    <a-menu-item key="md-document" @click="emit('create-document')">
      <img :src="knowledgeAddDocumentIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
      {{ t('knowledge.addMdDocument') }}
    </a-menu-item>
    <a-menu-item key="folder" :disabled="folderCreating" @click="emit('create-folder')">
      <img :src="knowledgeAddFolderIcon" alt="" aria-hidden="true" class="knowledge-menu-icon" />
      {{ t('knowledge.addFolder') }}
    </a-menu-item>
  </a-menu>
</template>

<script setup lang="ts">
import knowledgeAddDocumentIcon from '@/assets/icons/knowledge-add-document.svg'
import knowledgeAddFolderIcon from '@/assets/icons/knowledge-add-folder.svg'
import { useAppI18n } from '@/i18n'

defineProps<{ folderCreating?: boolean }>()
const emit = defineEmits<{
  'create-document': []
  'create-folder': []
  select: []
}>()
const { t } = useAppI18n()
</script>

<style scoped>
.knowledge-menu-icon {
  display: inline-block;
  margin-right: 8px;
  vertical-align: -3px;
}
</style>
