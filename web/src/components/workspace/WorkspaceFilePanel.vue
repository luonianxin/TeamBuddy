<template>
  <aside
    class="workspace-panel"
    :aria-label="t('workflows.task.workspaceFiles.title')"
  >
    <header class="workspace-header">
      <div class="workspace-heading">
        <FolderOpenOutlined aria-hidden="true" />
        <strong>{{ t('workflows.task.workspaceFiles.title') }}</strong>
      </div>
      <div class="workspace-actions">
        <button
          type="button"
          class="icon-button"
          :disabled="loadingTree || !selectedRootId"
          :title="t('workflows.task.workspaceFiles.refresh')"
          :aria-label="t('workflows.task.workspaceFiles.refresh')"
          @click="refresh"
        >
          <ReloadOutlined :spin="loadingTree" />
        </button>
        <button
          type="button"
          class="icon-button"
          :title="t('workflows.task.workspaceFiles.collapse')"
          :aria-label="t('workflows.task.workspaceFiles.collapse')"
          @click="emit('close')"
        >
          <CloseOutlined />
        </button>
      </div>
    </header>

    <div class="workspace-controls">
      <a-select
        :value="selectedRootId"
        class="root-select"
        :loading="loadingRoots"
        :disabled="!roots.length"
        :placeholder="t('workflows.task.workspaceFiles.chooseRoot')"
        @change="handleRootChange"
      >
        <a-select-option
          v-for="root in roots"
          :key="root.id"
          :value="root.id"
          :disabled="!root.available"
        >
          <span :title="root.path"
            >{{ root.name
            }}{{ root.primary ? ` · ${t('workflows.task.workspaceFiles.primary')}` : '' }}</span
          >
        </a-select-option>
      </a-select>
      <a-input
        v-model:value="searchQuery"
        allow-clear
        :disabled="!selectedRoot?.available"
        :placeholder="t('workflows.task.workspaceFiles.search')"
        @press-enter="runSearch"
      >
        <template #prefix><SearchOutlined aria-hidden="true" /></template>
      </a-input>
      <label class="ignored-toggle">
        <a-checkbox v-model:checked="showIgnored" />
        <span>{{ t('workflows.task.workspaceFiles.showIgnored') }}</span>
      </label>
    </div>

    <div class="workspace-tree scrollbar--subtle">
      <div
        v-if="loadingRoots || loadingTree"
        class="panel-state"
      >
        <a-spin />
      </div>
      <div
        v-else-if="rootsError || treeError"
        class="panel-state panel-error"
      >
        <a-alert
          type="error"
          show-icon
          :message="rootsError || treeError"
        />
        <a-button
          size="small"
          @click="initialize"
          >{{ t('common.actions.retry') }}</a-button
        >
      </div>
      <a-empty
        v-else-if="!roots.length"
        :image="false"
        :description="t('workflows.task.workspaceFiles.noRoots')"
      />
      <a-empty
        v-else-if="!selectedRoot?.available"
        :image="false"
        :description="t('workflows.task.workspaceFiles.rootUnavailable')"
      />
      <a-empty
        v-else-if="!treeData.length"
        :image="false"
        :description="
          searchQuery.trim()
            ? t('workflows.task.workspaceFiles.noSearchResults')
            : t('workflows.task.workspaceFiles.emptyDirectory')
        "
      />
      <a-tree
        v-else
        block-node
        :tree-data="treeData"
        :expanded-keys="expandedKeys"
        :selected-keys="selectedKeys"
        :load-data="loadTreeNode"
        @expand="handleExpand"
        @select="handleSelect"
      >
        <template #title="{ title, entry, searchPath }">
          <div class="workspace-node">
            <FolderOutlined
              v-if="entry?.is_dir"
              class="node-icon folder-icon"
            />
            <FileImageOutlined
              v-else-if="isImage(entry?.file_type)"
              class="node-icon image-icon"
            />
            <FileMarkdownOutlined
              v-else-if="isMarkdown(entry?.file_type)"
              class="node-icon markdown-icon"
            />
            <FileTextOutlined
              v-else
              class="node-icon"
            />
            <span class="node-copy">
              <span
                class="node-name"
                :title="entry?.path || title"
                >{{ title }}</span
              >
              <small v-if="searchPath">{{ searchPath }}</small>
            </span>
          </div>
        </template>
      </a-tree>
    </div>

    <footer class="workspace-footer">
      <LockOutlined aria-hidden="true" />
      <span>{{ t('workflows.task.workspaceFiles.readOnlyHint') }}</span>
    </footer>

    <WorkspaceFilePreview
      :open="previewOpen"
      :loading="contentLoading"
      :error="contentError"
      :content="selectedContent"
      :file-name="selectedFileName"
      @close="closePreview"
    />
  </aside>
</template>

<script setup lang="ts">
import { onBeforeUnmount, shallowRef, toRef, watch } from 'vue'
import {
  CloseOutlined,
  FileImageOutlined,
  FileMarkdownOutlined,
  FileTextOutlined,
  FolderOpenOutlined,
  FolderOutlined,
  LockOutlined,
  ReloadOutlined,
  SearchOutlined,
} from '@ant-design/icons-vue'
import { useWorkspaceFiles } from '@/composables/useWorkspaceFiles'
import { useAppI18n } from '@/i18n'
import type { WorkspaceFileContent, WorkspaceFileEntry } from '@/types/workspace-files'
import WorkspaceFilePreview from './WorkspaceFilePreview.vue'

interface WorkspaceTreeNode {
  key: string
  title: string
  isLeaf: boolean
  entry: WorkspaceFileEntry
  searchPath?: string
  children?: WorkspaceTreeNode[]
}

const props = defineProps<{ taskUuid: string }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useAppI18n()
const taskUuid = toRef(props, 'taskUuid')
const {
  roots,
  selectedRoot,
  selectedRootId,
  loadingRoots,
  rootsError,
  loadRoots,
  selectRoot,
  loadEntries,
  loadContent,
  search,
  invalidate,
} = useWorkspaceFiles(taskUuid)

const treeData = shallowRef<WorkspaceTreeNode[]>([])
const expandedKeys = shallowRef<string[]>([])
const selectedKeys = shallowRef<string[]>([])
const loadingTree = shallowRef(false)
const treeError = shallowRef('')
const searchQuery = shallowRef('')
const showIgnored = shallowRef(false)
const previewOpen = shallowRef(false)
const contentLoading = shallowRef(false)
const contentError = shallowRef('')
const selectedContent = shallowRef<WorkspaceFileContent>()
const selectedFileName = shallowRef('')
let searchTimer: ReturnType<typeof setTimeout> | undefined
let contentVersion = 0
let treeVersion = 0

watch(taskUuid, () => void initialize(), { immediate: true })
watch(searchQuery, (value) => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    if (!value.trim() || value.trim().length >= 2) void runSearch()
  }, 260)
})
watch(showIgnored, () => void refresh())

async function initialize() {
  closePreview()
  resetTree()
  await loadRoots()
  if (selectedRootId.value) await loadRoot()
}

async function handleRootChange(value: unknown) {
  selectRoot(String(value || ''))
  closePreview()
  resetTree()
  await loadRoot()
}

async function loadRoot() {
  if (!selectedRoot.value?.available) return
  const version = ++treeVersion
  loadingTree.value = true
  treeError.value = ''
  try {
    const entries = await loadEntries('', true, showIgnored.value)
    if (version !== treeVersion) return
    treeData.value = entries.map((entry) => mapEntry(entry))
    restoreExpandedKeys()
  } catch (error) {
    if (version !== treeVersion || isAbortError(error)) return
    treeError.value =
      error instanceof Error ? error.message : t('workflows.task.workspaceFiles.loadFailed')
  } finally {
    if (version === treeVersion) loadingTree.value = false
  }
}

async function loadTreeNode(rawNode: unknown) {
  const holder = rawNode as { dataRef?: WorkspaceTreeNode }
  const node = holder.dataRef || (rawNode as WorkspaceTreeNode)
  if (!node.entry?.is_dir || node.children) return
  try {
    const entries = await loadEntries(node.entry.path, false, showIgnored.value)
    node.children = entries.map((entry) => mapEntry(entry))
    treeData.value = [...treeData.value]
  } catch (error) {
    if (isAbortError(error)) return
    treeError.value =
      error instanceof Error ? error.message : t('workflows.task.workspaceFiles.loadFailed')
    throw error
  }
}

function mapEntry(entry: WorkspaceFileEntry, searchResult = false): WorkspaceTreeNode {
  return {
    key: entry.path,
    title: entry.name,
    isLeaf: !entry.is_dir || searchResult,
    entry,
    searchPath: searchResult ? entry.path : undefined,
  }
}

function handleExpand(keys: Array<string | number>) {
  expandedKeys.value = keys.map(String)
  persistExpandedKeys()
}

function handleSelect(
  keys: Array<string | number>,
  info: { node?: { dataRef?: WorkspaceTreeNode } },
) {
  selectedKeys.value = keys.map(String)
  const node = info.node?.dataRef || findNode(treeData.value, selectedKeys.value[0])
  if (!node?.entry || node.entry.is_dir) return
  void openFile(node.entry)
}

async function openFile(entry: WorkspaceFileEntry) {
  const version = ++contentVersion
  selectedFileName.value = entry.name
  selectedContent.value = undefined
  contentError.value = ''
  contentLoading.value = true
  previewOpen.value = true
  try {
    const content = await loadContent(entry.path)
    if (version === contentVersion) selectedContent.value = content
  } catch (error) {
    if (version === contentVersion) {
      contentError.value =
        error instanceof Error ? error.message : t('workflows.task.workspaceFiles.previewFailed')
    }
  } finally {
    if (version === contentVersion) contentLoading.value = false
  }
}

async function runSearch() {
  const version = ++treeVersion
  const query = searchQuery.value.trim()
  treeError.value = ''
  if (!query) {
    await loadRoot()
    return
  }
  if (query.length < 2 || !selectedRootId.value) return
  loadingTree.value = true
  try {
    const entries = await search(query, showIgnored.value)
    if (version !== treeVersion) return
    treeData.value = entries.map((entry) => mapEntry(entry, true))
    expandedKeys.value = []
  } catch (error) {
    if (version !== treeVersion || isAbortError(error)) return
    treeError.value =
      error instanceof Error ? error.message : t('workflows.task.workspaceFiles.searchFailed')
  } finally {
    if (version === treeVersion) loadingTree.value = false
  }
}

async function refresh() {
  closePreview()
  invalidate()
  treeError.value = ''
  if (searchQuery.value.trim()) await runSearch()
  else await loadRoot()
}

function resetTree() {
  treeData.value = []
  expandedKeys.value = []
  selectedKeys.value = []
  searchQuery.value = ''
  treeError.value = ''
}

function closePreview() {
  contentVersion += 1
  previewOpen.value = false
  contentLoading.value = false
  contentError.value = ''
  selectedContent.value = undefined
  selectedFileName.value = ''
}

function expandedStorageKey() {
  return `teambuddy.workspace-expanded.${taskUuid.value}.${selectedRootId.value}`
}

function restoreExpandedKeys() {
  try {
    const parsed = JSON.parse(localStorage.getItem(expandedStorageKey()) || '[]')
    expandedKeys.value = Array.isArray(parsed) ? parsed.map(String) : []
  } catch {
    expandedKeys.value = []
  }
}

function persistExpandedKeys() {
  if (!taskUuid.value || !selectedRootId.value) return
  localStorage.setItem(expandedStorageKey(), JSON.stringify(expandedKeys.value))
}

function findNode(nodes: WorkspaceTreeNode[], key?: string): WorkspaceTreeNode | undefined {
  if (!key) return undefined
  for (const node of nodes) {
    if (node.key === key) return node
    const nested = node.children ? findNode(node.children, key) : undefined
    if (nested) return nested
  }
  return undefined
}

function isAbortError(error: unknown) {
  return error instanceof DOMException && error.name === 'AbortError'
}

function isImage(type?: string) {
  return ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico'].includes(type || '')
}

function isMarkdown(type?: string) {
  return type === 'md' || type === 'markdown'
}

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  contentVersion += 1
})
</script>

<style scoped>
.workspace-panel {
  display: flex;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  border-left: 1px solid #f0f0f0;
  background: #fff;
}

.workspace-header {
  display: flex;
  min-height: 44px;
  flex: 0 0 44px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 10px 0 14px;
  border-bottom: 1px solid #f0f0f0;
}

.workspace-heading,
.workspace-actions,
.workspace-node {
  display: flex;
  min-width: 0;
  align-items: center;
}

.workspace-heading {
  gap: 8px;
}
.workspace-heading strong {
  font-size: 14px;
  font-weight: 600;
}
.workspace-actions {
  gap: 2px;
}

.icon-button {
  display: inline-flex;
  width: 30px;
  height: 30px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  color: #595959;
  background: transparent;
  cursor: pointer;
}

.icon-button:hover:not(:disabled) {
  background: #f5f5f5;
}
.icon-button:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 1px;
}
.icon-button:disabled {
  cursor: not-allowed;
  opacity: 0.4;
}

.workspace-controls {
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid #f0f0f0;
}

.root-select {
  width: 100%;
}
.ignored-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #8c8c8c;
  font-size: 12px;
}

.workspace-tree {
  min-height: 0;
  flex: 1;
  overflow: auto;
  padding: 8px 6px 16px;
}

.workspace-tree :deep(.ant-tree-node-content-wrapper) {
  min-width: 0;
  overflow: hidden;
}
.workspace-tree :deep(.ant-tree-title) {
  display: block;
  min-width: 0;
}
.workspace-node {
  gap: 6px;
}
.node-icon {
  flex: 0 0 auto;
  color: #8c8c8c;
}
.folder-icon {
  color: #d4a72c;
}
.image-icon {
  color: #16a34a;
}
.markdown-icon {
  color: #3157e2;
}
.node-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
}
.node-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node-copy small {
  overflow: hidden;
  color: #a3a3a3;
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.panel-state {
  display: flex;
  min-height: 180px;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
}
.panel-error :deep(.ant-alert) {
  width: 100%;
}

.workspace-footer {
  display: flex;
  min-height: 34px;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  padding: 0 12px;
  border-top: 1px solid #f0f0f0;
  color: #8c8c8c;
  font-size: 11px;
}
</style>
