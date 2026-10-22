import { computed, shallowRef, type Ref } from 'vue'
import apiClient from '@/api/client'
import type {
  WorkspaceEntriesResponse,
  WorkspaceFileContent,
  WorkspaceFileEntry,
  WorkspaceRoot,
  WorkspaceRootsResponse,
  WorkspaceSearchResponse,
} from '@/types/workspace-files'

const selectionKey = (taskUuid: string) => `teambuddy.workspace-root.${taskUuid}`

export function useWorkspaceFiles(taskUuid: Ref<string>) {
  const roots = shallowRef<WorkspaceRoot[]>([])
  const selectedRootId = shallowRef('')
  const loadingRoots = shallowRef(false)
  const rootsError = shallowRef('')
  const directoryCache = shallowRef(new Map<string, WorkspaceFileEntry[]>())
  const activeControllers = new Set<AbortController>()
  let requestVersion = 0

  const selectedRoot = computed(() => roots.value.find((root) => root.id === selectedRootId.value))

  async function loadRoots() {
    abortAll()
    const uuid = taskUuid.value
    const version = ++requestVersion
    roots.value = []
    selectedRootId.value = ''
    directoryCache.value = new Map()
    rootsError.value = ''
    if (!uuid) return
    loadingRoots.value = true
    try {
      const response = await getWithAbort<WorkspaceRootsResponse>(
        `/tasks/${encodeURIComponent(uuid)}/workspace/roots`,
      )
      if (version !== requestVersion || uuid !== taskUuid.value) return
      roots.value = response.roots || []
      const remembered = localStorage.getItem(selectionKey(uuid)) || ''
      const available = roots.value.filter((root) => root.available)
      selectedRootId.value =
        available.find((root) => root.id === remembered)?.id ||
        available.find((root) => root.primary)?.id ||
        available[0]?.id ||
        roots.value.find((root) => root.primary)?.id ||
        roots.value[0]?.id ||
        ''
    } catch (error) {
      if (version !== requestVersion) return
      rootsError.value = error instanceof Error ? error.message : ''
    } finally {
      if (version === requestVersion) loadingRoots.value = false
    }
  }

  function selectRoot(rootId: string) {
    abortAll()
    selectedRootId.value = rootId
    directoryCache.value = new Map()
    if (taskUuid.value && rootId) localStorage.setItem(selectionKey(taskUuid.value), rootId)
  }

  async function loadEntries(
    path = '',
    force = false,
    showIgnored = false,
  ): Promise<WorkspaceFileEntry[]> {
    const uuid = taskUuid.value
    const rootId = selectedRootId.value
    if (!uuid || !rootId) return []
    const cacheKey = `${rootId}:${showIgnored}:${path}`
    if (!force && directoryCache.value.has(cacheKey)) {
      return directoryCache.value.get(cacheKey) || []
    }
    const response = await getWithAbort<WorkspaceEntriesResponse>(
      `/tasks/${encodeURIComponent(uuid)}/workspace/entries`,
      { root_id: rootId, path, show_ignored: showIgnored },
    )
    if (uuid !== taskUuid.value || rootId !== selectedRootId.value) return []
    const next = new Map(directoryCache.value)
    next.set(cacheKey, response.entries || [])
    directoryCache.value = next
    return response.entries || []
  }

  async function loadContent(path: string): Promise<WorkspaceFileContent> {
    return getWithAbort<WorkspaceFileContent>(
      `/tasks/${encodeURIComponent(taskUuid.value)}/workspace/content`,
      { root_id: selectedRootId.value, path },
    )
  }

  async function search(query: string, showIgnored = false): Promise<WorkspaceFileEntry[]> {
    const response = await getWithAbort<WorkspaceSearchResponse>(
      `/tasks/${encodeURIComponent(taskUuid.value)}/workspace/search`,
      { root_id: selectedRootId.value, q: query, show_ignored: showIgnored, limit: 100 },
    )
    return response.entries || []
  }

  function invalidate() {
    requestVersion += 1
    abortAll()
    directoryCache.value = new Map()
  }

  async function getWithAbort<T>(
    path: string,
    params?: Record<string, string | number | boolean | undefined>,
  ) {
    const controller = new AbortController()
    activeControllers.add(controller)
    try {
      return await apiClient.get<T>(path, params, controller.signal)
    } finally {
      activeControllers.delete(controller)
    }
  }

  function abortAll() {
    for (const controller of activeControllers) controller.abort()
    activeControllers.clear()
  }

  return {
    roots,
    selectedRoot,
    selectedRootId,
    loadingRoots,
    rootsError,
    directoryCache,
    loadRoots,
    selectRoot,
    loadEntries,
    loadContent,
    search,
    invalidate,
  }
}
