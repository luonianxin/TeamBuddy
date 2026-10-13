<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { SettingOutlined } from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import { useLocalWS } from '@/composables/useLocalWebSocket'
import { useAppI18n } from '@/i18n'
import DirectoryPicker, { type DirectoryPickerCandidate } from '@/components/DirectoryPicker.vue'
import { useConversationStore, getLastDirSegment } from '@/stores/conversation'

type ChannelStatus = 'disconnected' | 'connecting' | 'connected'
type LoginStatus = 'waiting' | 'scanned' | 'verifying' | 'need_verifycode' | 'expired' | 'error' | 'connected'

// IN-001: WeChatStatus 不再返回 CLI 默认配置，CLI 配置走独立接口并行加载
interface WeChatStatus {
  status: ChannelStatus
  connected_at: number
  login_id: string
  last_error: string
  reply_message_id_missing: boolean
}

interface LoginAttempt {
  id: string
  status: LoginStatus
  qr_image: string
  error?: string
}

interface UncertainRun {
  message_id: string
  task_uuid: string
  task_title: string
  received_at: number
}

// S-UI001, IN-002: CLI 发现项接口
interface CLIDiscoveryItem {
  type: string
  name: string
  installed: boolean
  exec_path: string
  version: string
  models: string[]
}

// S-UI001, IN-002: CLI 配置接口
interface CLIConfig {
  cli_type: string
  cli_model: string
  workspace: string
  enabled: boolean
}

const { t, d } = useAppI18n()
const conversationStore = useConversationStore()
const status = ref<ChannelStatus>('disconnected')
const lastError = ref('')
const replyMessageIDMissing = ref(false)
const loading = ref(true)
const loadError = ref(false)
const loginOpen = ref(false)
const login = ref<LoginAttempt | null>(null)
const loginBusy = ref(false)
const verifyBusy = ref(false)
const verifyCode = ref('')
const uncertainRuns = ref<UncertainRun[]>([])
const uncertainLoading = ref(false)
const uncertainError = ref(false)
const acknowledgeBusy = ref<string | null>(null)

// S-UI001, IN-001, IN-002, IN-003, IN-004, IN-005: CLI 配置相关状态
const cliConfigOpen = ref(false)
const cliDiscoveryList = ref<CLIDiscoveryItem[]>([])
const cliDiscoveryLoading = ref(false)
const selectedCLItype = ref('')
const selectedCLImodel = ref('')
const selectedWorkspace = ref('')
const cliConfigEnabled = ref(true)
const cliModels = ref<string[]>([])
const cliConfigSaving = ref(false)
const cliConfigToggling = ref(false)

let loginTimer: ReturnType<typeof setInterval> | undefined
let statusTimer: ReturnType<typeof setInterval> | undefined
let uncertainRequestVersion = 0
let statusRequestVersion = 0
// IN-001: CLI 配置独立加载，独立的请求版本号用于丢弃过期响应
let cliConfigRequestVersion = 0
// 备份当前已保存的 CLI 配置，关闭弹窗时丢弃临时编辑并还原
let cliTypeBackup = ''
let cliModelBackup = ''
let workspaceBackup = ''
let cliEnabledBackup = true

const cards = computed(() => [
  { key: 'wechat', name: t('remoteChannels.wechat'), description: t('remoteChannels.wechatDescription'), mark: 'wechat', glyph: '' },
  { key: 'wecom', name: t('remoteChannels.wecom'), description: t('remoteChannels.wecomDescription'), mark: 'wecom', glyph: '企' },
  { key: 'feishu', name: t('remoteChannels.feishu'), description: t('remoteChannels.feishuDescription'), mark: 'feishu', glyph: '飞' },
  { key: 'dingtalk', name: t('remoteChannels.dingtalk'), description: t('remoteChannels.dingtalkDescription'), mark: 'dingtalk', glyph: '钉' },
  { key: 'app', name: t('remoteChannels.app'), description: t('remoteChannels.appDescription'), mark: 'app', glyph: '▣' },
])

// S-UI001, IN-002: 已安装的 CLI 列表（过滤掉未安装的）
const installedCLIs = computed(() => cliDiscoveryList.value.filter((cli) => cli.installed))

// S-UI001: 当前已配置 CLI 的展示名（用于卡片状态展示）
const currentCLIName = computed(() => {
  const item = cliDiscoveryList.value.find((cli) => cli.type === selectedCLItype.value)
  return item?.name || selectedCLItype.value
})

// UI-007: CLI 工作目录候选项。读取 conversationStore 的历史目录和项目本地目录作为候选，
// 不调用 recordWorkingDir，避免把兜底任务目录写进对话工作目录历史。
const workspaceCandidates = computed<DirectoryPickerCandidate[]>(() => {
  const normalize = (p: string) => (p || '').replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
  const knownDirs = conversationStore.allKnownWorkDirs
  const projects = conversationStore.projects
  return knownDirs
    .map((dir) => {
      const matched = projects.find((p) => p.local_dir && normalize(p.local_dir) === normalize(dir))
      return {
        path: dir,
        projectName: matched ? matched.name : undefined,
      }
    })
    .sort((a, b) => {
      const aName = a.projectName || getLastDirSegment(a.path)
      const bName = b.projectName || getLastDirSegment(b.path)
      return aName.localeCompare(bName)
    })
})

const loginStatusText = computed(() => {
  switch (login.value?.status) {
    case 'scanned': return t('remoteChannels.scanned')
    case 'verifying': return t('remoteChannels.verifying')
    case 'need_verifycode': return t('remoteChannels.needVerify')
    case 'expired': return t('remoteChannels.expired')
    case 'error': return login.value.error || t('remoteChannels.connectFailed')
    default: return t('remoteChannels.waiting')
  }
})

const statusErrorText = computed(() => lastError.value === 'wechat_session_expired'
  ? t('remoteChannels.connectionLost')
  : t('remoteChannels.statusError'))

// S-UI001: CLI 是否已配置
const cliConfigured = computed(() => selectedCLItype.value !== '' && selectedCLImodel.value !== '')

function stopLoginPolling() {
  if (loginTimer) clearInterval(loginTimer)
  loginTimer = undefined
}

async function loadStatus(silent = false) {
  const requestVersion = ++statusRequestVersion
  if (!silent) loading.value = true
  try {
    const response = await apiClient.get<WeChatStatus>('/remote-channels/wechat')
    if (!response || !['disconnected', 'connecting', 'connected'].includes(response.status)) {
      throw new Error('Invalid channel status')
    }
    if (requestVersion !== statusRequestVersion) return
    status.value = response.status
    lastError.value = response.last_error
    replyMessageIDMissing.value = response.reply_message_id_missing === true
    // IN-001: CLI 默认配置改为独立接口并行加载，不与 status 接口耦合
    loadError.value = false
  } catch {
    if (requestVersion === statusRequestVersion) loadError.value = true
  } finally {
    if (requestVersion === statusRequestVersion) loading.value = false
  }
}

useLocalWS('remote.wechat.status', () => { void loadStatus(true) })

// IN-001: 独立加载 CLI 配置，不依赖 status 接口；status 接口慢或失败时仍可显示已有配置
async function loadCLIConfig() {
  const requestVersion = ++cliConfigRequestVersion
  try {
    const response = await apiClient.get<CLIConfig>('/remote-channels/wechat/cli-config')
    if (!response || typeof response.cli_type !== 'string' || typeof response.cli_model !== 'string') {
      throw new Error('Invalid CLI config')
    }
    if (requestVersion !== cliConfigRequestVersion) return
    selectedCLItype.value = response.cli_type
    selectedCLImodel.value = response.cli_model
    selectedWorkspace.value = response.workspace || ''
    cliConfigEnabled.value = response.enabled !== false
  } catch {
    // 静默失败：CLI 配置拉取失败不影响主状态展示，保留已有选择或默认空值
  }
}

async function loadUncertain() {
  const requestVersion = ++uncertainRequestVersion
  uncertainLoading.value = true
  uncertainError.value = false
  try {
    const response = await apiClient.get<{ items: UncertainRun[] }>('/remote-channels/wechat/uncertain')
    if (!response || !Array.isArray(response.items)) throw new Error('Invalid uncertain runs')
    if (requestVersion === uncertainRequestVersion) uncertainRuns.value = response.items
  } catch {
    if (requestVersion === uncertainRequestVersion) uncertainError.value = true
  } finally {
    if (requestVersion === uncertainRequestVersion) uncertainLoading.value = false
  }
}

function acknowledgeUncertain(run: UncertainRun) {
  if (acknowledgeBusy.value) return
  Modal.confirm({
    title: t('remoteChannels.uncertainConfirmTitle'),
    content: t('remoteChannels.uncertainConfirmDescription', { task: run.task_title || run.task_uuid || t('remoteChannels.uncertainTaskUnknown') }),
    okText: t('remoteChannels.uncertainAcknowledge'),
    okType: 'danger',
    onOk: async () => {
      acknowledgeBusy.value = run.message_id
      try {
        await apiClient.post('/remote-channels/wechat/uncertain/ack', { message_id: run.message_id })
        uncertainRuns.value = uncertainRuns.value.filter((item) => item.message_id !== run.message_id)
        await loadUncertain()
        message.success(t('remoteChannels.uncertainAcknowledged'))
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.uncertainAckFailed'))
        throw error
      } finally {
        acknowledgeBusy.value = null
      }
    },
  })
}

async function pollLogin() {
  const id = login.value?.id
  if (!id || loginBusy.value) return
  try {
    const response = await apiClient.get<LoginAttempt>(`/remote-channels/wechat/login/${encodeURIComponent(id)}`)
    if (!response || response.id !== id || typeof response.status !== 'string') {
      throw new Error('Invalid login status')
    }
    if (login.value?.id !== id) return
    login.value = response
    if (response.status === 'connected') {
      stopLoginPolling()
      loginOpen.value = false
      status.value = 'connected'
      lastError.value = ''
      await loadStatus()
    } else if (response.status === 'expired' || response.status === 'error') {
      stopLoginPolling()
    }
  } catch {
    stopLoginPolling()
    if (login.value?.id === id) login.value = { ...login.value, status: 'error' }
  }
}

async function startLogin() {
  if (loginBusy.value) return
  loginBusy.value = true
  stopLoginPolling()
  verifyCode.value = ''
  login.value = null
  loginOpen.value = true
  try {
    const response = await apiClient.post<LoginAttempt>('/remote-channels/wechat/login', {})
    if (!response || !response.id || !response.qr_image) throw new Error(t('remoteChannels.connectFailed'))
    login.value = response
    status.value = 'connecting'
    loginTimer = setInterval(() => { void pollLogin() }, 2000)
  } catch (error) {
    login.value = { id: '', status: 'error', qr_image: '', error: error instanceof Error ? error.message : t('remoteChannels.connectFailed') }
  } finally {
    loginBusy.value = false
  }
}

async function submitVerification() {
  if (!login.value?.id || !verifyCode.value.trim() || verifyBusy.value) return
  verifyBusy.value = true
  try {
    await apiClient.post(`/remote-channels/wechat/login/${encodeURIComponent(login.value.id)}/verify`, { code: verifyCode.value.trim() })
    verifyCode.value = ''
    login.value = { ...login.value, status: 'verifying' }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.verifyFailed'))
  } finally {
    verifyBusy.value = false
  }
}

function closeLogin() {
  loginOpen.value = false
  void loadStatus()
}

function disconnect() {
  Modal.confirm({
    title: t('remoteChannels.disconnectTitle'),
    content: t('remoteChannels.disconnectDescription'),
    okText: t('remoteChannels.disconnect'),
    okType: 'danger',
    onOk: async () => {
      try {
        const result = await apiClient.delete<{ status: ChannelStatus; wechat_notified: boolean }>('/remote-channels/wechat')
        statusRequestVersion++
        status.value = 'disconnected'
        lastError.value = ''
        replyMessageIDMissing.value = false
        login.value = null
        stopLoginPolling()
        uncertainRequestVersion++
        uncertainLoading.value = false
        // S-UI001: 重新拉取状态，确保 CLI 配置在断开后仍被保留和显示
        await loadStatus()
        await loadUncertain()
        if (result.wechat_notified) {
          message.success(t('remoteChannels.disconnectSuccess'))
        } else {
          message.warning(t('remoteChannels.stopNotifyFailed'))
        }
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.disconnectFailed'))
        throw error
      }
    },
  })
}

// IN-001: 打开 CLI 配置弹窗，先备份已保存的配置；关闭弹窗时会还原，避免卡片被临时编辑污染
async function openCLIconfig() {
  cliTypeBackup = selectedCLItype.value
  cliModelBackup = selectedCLImodel.value
  workspaceBackup = selectedWorkspace.value
  cliEnabledBackup = cliConfigEnabled.value
  cliConfigOpen.value = true
  await loadCLIDiscovery()
}

// IN-001: 关闭 CLI 配置弹窗时还原备份（丢弃临时编辑），保证卡片上的 CLI 配置始终反映已保存值
function closeCLIconfig() {
  cliConfigOpen.value = false
  selectedCLItype.value = cliTypeBackup
  selectedCLImodel.value = cliModelBackup
  selectedWorkspace.value = workspaceBackup
  cliConfigEnabled.value = cliEnabledBackup
  cliModels.value = []
}

// S-UI001, IN-002: 加载 CLI 发现列表
async function loadCLIDiscovery() {
  cliDiscoveryLoading.value = true
  try {
    const response = await apiClient.get<{ items: CLIDiscoveryItem[] }>('/tasks/cli-discovery')
    if (response && Array.isArray(response.items)) {
      cliDiscoveryList.value = response.items
    }
  } catch (error) {
    message.error(t('remoteChannels.cliDiscoveryFailed'))
  } finally {
    cliDiscoveryLoading.value = false
  }
}

// S-UI001, IN-003: 仅加载 CLI 模型列表，不修改 selectedCLImodel。
// 用于程序化触发（如弹窗打开时重新拉模型、loadCLIConfig 完成后预热下拉）。
async function loadCLIModels(cliType: string) {
  if (!cliType) {
    cliModels.value = []
    return
  }
  try {
    const response = await apiClient.get<{ models: string[] }>(`/tasks/cli-models?cli_type=${encodeURIComponent(cliType)}`)
    if (response && Array.isArray(response.models)) {
      cliModels.value = response.models
    }
  } catch (error) {
    message.error(t('remoteChannels.cliModelsLoadFailed'))
  }
}

// S-UI001, IN-003: 用户在弹窗内切换 CLI 类型时调用，清空已选 model 并加载新模型列表。
// 仅由 a-select 的 @change 触发，避免任何程序化赋值（包括 loadCLIConfig / backup 还原）误清空 selectedCLImodel。
async function onCLItypeChange(cliType: string) {
  selectedCLImodel.value = ''
  await loadCLIModels(cliType)
}

// S-UI001, IN-004: 保存 CLI 配置
// S-UI001-fix: CLI 配置与连接状态无关，作为通道指令消息的兜底处理选项，可随时保存。
async function saveCLIconfig() {
  if (!selectedCLItype.value) {
    message.warning(t('remoteChannels.selectCliType'))
    return
  }
  if (!selectedCLImodel.value) {
    message.warning(t('remoteChannels.selectCliModel'))
    return
  }
  cliConfigSaving.value = true
  try {
    await apiClient.put('/remote-channels/wechat/cli-config', {
      cli_type: selectedCLItype.value,
      cli_model: selectedCLImodel.value,
      workspace: selectedWorkspace.value.trim(),
      enabled: cliConfigEnabled.value,
    })
    // IN-001: 保存成功时把当前值同步为备份，closeCLIconfig 还原时保留新值，卡片不会被覆盖回旧值
    cliTypeBackup = selectedCLItype.value
    cliModelBackup = selectedCLImodel.value
    workspaceBackup = selectedWorkspace.value
    cliEnabledBackup = cliConfigEnabled.value
    message.success(t('remoteChannels.cliConfigSaved'))
    closeCLIconfig()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    cliConfigSaving.value = false
  }
}

// IN-006: 卡片上的开关切换。直接落库后回写本地状态，不打开弹窗。
async function toggleCLIEnabled(next: boolean) {
  if (cliConfigToggling.value) return
  const previous = cliConfigEnabled.value
  cliConfigEnabled.value = next
  cliConfigToggling.value = true
  try {
    await apiClient.put('/remote-channels/wechat/cli-config', {
      cli_type: selectedCLItype.value,
      cli_model: selectedCLImodel.value,
      workspace: selectedWorkspace.value.trim(),
      enabled: next,
    })
    cliEnabledBackup = next
  } catch (error) {
    cliConfigEnabled.value = previous
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    cliConfigToggling.value = false
  }
}

// S-UI001, IN-005: 暂不配置
function skipCLIconfig() {
  closeCLIconfig()
}

// IN-001: 弹窗打开时预热模型列表。已选 model 必须保留（用 loadCLIModels 而非 onCLItypeChange，
// 否则 backup 还原 selectedCLItype 时会把 selectedCLImodel 又清掉一次）。
watch(cliConfigOpen, async (isOpen) => {
  if (isOpen && selectedCLItype.value) {
    await loadCLIModels(selectedCLItype.value)
  }
})

onMounted(() => {
  void loadStatus()
  void loadUncertain()
  // IN-001: 并行独立加载 CLI 配置，status 接口慢或失败时不影响 CLI 配置展示
  void loadCLIConfig()
  statusTimer = setInterval(() => { void loadStatus(true) }, 10_000)
})
onUnmounted(() => {
  stopLoginPolling()
  if (statusTimer) clearInterval(statusTimer)
  statusRequestVersion++
  uncertainRequestVersion++
  cliConfigRequestVersion++
})
</script>

<template>
  <section class="remote-page">
    <header class="page-header"><h1>{{ t('remoteChannels.title') }}</h1></header>
    <div class="remote-content">
      <div class="intro-banner" role="note">
        <span class="info-symbol" aria-hidden="true"><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M6 5v3.5M6 3.5h.01" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></span>
        <div><strong>{{ t('remoteChannels.intro') }}</strong><p>{{ t('remoteChannels.hint') }}</p></div>
      </div>

      <div v-if="loading" class="state-message">{{ t('remoteChannels.loading') }}</div>
      <div v-if="loadError" class="state-message">
        {{ t('remoteChannels.loadFailed') }}
        <a-button size="small" @click="loadStatus()">{{ t('remoteChannels.retry') }}</a-button>
      </div>
      <a-alert v-if="lastError" class="status-alert" type="warning" show-icon :message="statusErrorText" />
      <div class="channel-grid">
          <article v-for="card in cards" :key="card.key" class="channel-card">
            <div class="card-heading">
              <span class="channel-icon" :class="card.mark" aria-hidden="true">
                <svg v-if="card.mark === 'wechat'" viewBox="0 0 36 36" fill="none">
                  <ellipse cx="15" cy="15" rx="12" ry="9.5" fill="white" />
                  <path d="m8 23-1 5 5-3" fill="white" />
                  <ellipse cx="23.5" cy="21" rx="9.5" ry="7.5" fill="#d9f6e6" />
                  <path d="m28 27 1 4-4-3" fill="#d9f6e6" />
                  <circle cx="11" cy="14" r="1.1" fill="#16a36a" /><circle cx="19" cy="14" r="1.1" fill="#16a36a" />
                </svg>
                <span v-else>{{ card.glyph }}</span>
              </span>
              <h2>{{ card.name }}</h2>
              <span v-if="card.key === 'wechat'" class="wechat-action-wrap">
                <a-button class="wechat-action" :class="{ 'wechat-action-with-warning': status === 'connected' && replyMessageIDMissing }" size="small" :loading="loginBusy" :disabled="loading || loadError" @click="status === 'connected' ? disconnect() : startLogin()">
                  {{ status === 'connected' ? t('remoteChannels.disconnect') : status === 'connecting' ? t('remoteChannels.connecting') : t('remoteChannels.connect') }}
                </a-button>
                <a-popover v-if="status === 'connected' && replyMessageIDMissing" :trigger="['hover', 'click']" placement="topRight">
                  <template #content><span class="wechat-reply-warning-text">{{ t('remoteChannels.replyMessageIDMissing') }}</span></template>
                  <button type="button" class="wechat-reply-warning" :aria-label="t('remoteChannels.replyMessageIDMissing')" />
                </a-popover>
              </span>
              <a-button v-if="card.key !== 'wechat'" class="channel-action" size="small" disabled>{{ t('remoteChannels.comingSoon') }}</a-button>
            </div>
            <!-- IN-006, IN-007: 微信 CLI 配置区。整块可点击打开弹窗；左侧设置图标、右侧启用开关独立交互。 -->
            <div
              v-if="card.key === 'wechat'"
              class="wechat-cli-panel"
              role="button"
              tabindex="0"
              :aria-label="t('remoteChannels.configureCLI')"
              @click="openCLIconfig"
              @keydown.enter.prevent="openCLIconfig"
              @keydown.space.prevent="openCLIconfig"
            >
              <span class="cli-settings-icon" aria-hidden="true">
                <SettingOutlined />
              </span>
              <span class="wechat-cli-status">
                <span class="cli-label">{{ t('remoteChannels.cliStatusLabel') }}</span>
                <template v-if="cliConfigured">
                  <span class="cli-name">{{ currentCLIName }}</span>
                  <span class="cli-sep" aria-hidden="true">·</span>
                  <span class="cli-model">{{ selectedCLImodel }}</span>
                </template>
                <span v-else class="cli-unset">{{ t('remoteChannels.cliStatusUnset') }}</span>
              </span>
              <a-switch
                class="cli-enabled-switch"
                :checked="cliConfigEnabled"
                :loading="cliConfigToggling"
                :disabled="!cliConfigured"
                :aria-label="t('remoteChannels.enableCLIConfig')"
                @click.stop
                @keydown.stop
                @change="(v: boolean) => toggleCLIEnabled(v)"
              />
            </div>
            <p>{{ card.description }}</p>
          </article>
      </div>
      <section v-if="status === 'connected' || uncertainRuns.length || uncertainError || uncertainLoading" class="uncertain-panel" aria-labelledby="uncertain-title">
        <div class="uncertain-heading">
          <h2 id="uncertain-title">{{ t('remoteChannels.uncertainTitle') }}</h2>
          <a-button size="small" :loading="uncertainLoading" @click="loadUncertain">{{ t('remoteChannels.retry') }}</a-button>
        </div>
        <a-alert v-if="uncertainRuns.length" type="warning" show-icon :message="t('remoteChannels.uncertainDescription')" />
        <p v-if="uncertainError" class="uncertain-error">{{ t('remoteChannels.uncertainLoadFailed') }}</p>
        <p v-if="uncertainLoading && !uncertainRuns.length" class="uncertain-empty">{{ t('remoteChannels.uncertainLoading') }}</p>
        <p v-else-if="!uncertainRuns.length && !uncertainError" class="uncertain-empty">{{ t('remoteChannels.uncertainEmpty') }}</p>
        <div v-for="run in uncertainRuns" :key="run.message_id" class="uncertain-item">
          <div class="uncertain-task-line">
            <span class="uncertain-hash" aria-hidden="true">#</span>
            <div class="uncertain-details">
              <strong>{{ run.task_title || t('remoteChannels.uncertainTaskMissing') }}</strong>
              <span>{{ t('remoteChannels.uncertainMeta', { time: d(new Date(run.received_at), 'long'), id: run.task_uuid || t('remoteChannels.uncertainTaskUnknown') }) }}</span>
            </div>
          </div>
          <div class="uncertain-actions">
            <router-link v-if="run.task_title" :to="{ name: 'workflows-task-detail', params: { taskUuid: run.task_uuid } }">
              {{ t('remoteChannels.openTask') }}
            </router-link>
            <a-button danger size="small" :loading="acknowledgeBusy === run.message_id" :disabled="!!acknowledgeBusy" @click="acknowledgeUncertain(run)">
              {{ t('remoteChannels.uncertainAcknowledge') }}
            </a-button>
          </div>
        </div>
      </section>
    </div>

    <a-modal v-model:open="loginOpen" :title="t('remoteChannels.loginTitle')" :footer="null" :mask-closable="false" width="400px" @cancel="closeLogin">
      <div class="login-body">
        <p class="scan-prompt">{{ t('remoteChannels.scanPrompt') }}</p>
        <div class="qr-frame">
          <img v-if="login?.qr_image" :src="login.qr_image" :alt="t('remoteChannels.scanPrompt')">
          <a-spin v-else :spinning="loginBusy" />
        </div>
        <p class="login-state" :class="{ error: login?.status === 'error' || login?.status === 'expired' }" role="status">{{ loginStatusText }}</p>
        <div v-if="login?.status === 'need_verifycode'" class="verify-row">
          <a-input v-model:value="verifyCode" :placeholder="t('remoteChannels.verifyPlaceholder')" :aria-label="t('remoteChannels.verifyPlaceholder')" @press-enter="submitVerification" />
          <a-button type="primary" :loading="verifyBusy" @click="submitVerification">{{ t('remoteChannels.verify') }}</a-button>
        </div>
        <a-button v-if="login?.status === 'expired' || login?.status === 'error'" @click="startLogin">{{ t('remoteChannels.refreshQR') }}</a-button>
        <a-button class="close-button" @click="closeLogin">{{ t('remoteChannels.close') }}</a-button>
      </div>
    </a-modal>

    <!-- S-UI001, IN-001, IN-002, IN-003, IN-004, IN-005, IN-006, UI-002, UI-004, UI-005, UI-006, UI-007: CLI 配置弹窗 -->
    <a-modal
      v-model:open="cliConfigOpen"
      :title="t('remoteChannels.cliConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeCLIconfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint">{{ t('remoteChannels.cliConfigHint') }}</p>

        <!-- IN-006: 启用此配置的总开关，关闭后 dispatch 不再走 CLI 兜底 -->
        <div class="form-item cli-enabled-row">
          <span class="form-label cli-enabled-label">{{ t('remoteChannels.enableCLIConfig') }}</span>
          <a-switch
            :checked="cliConfigEnabled"
            :aria-label="t('remoteChannels.enableCLIConfig')"
            @change="(v: boolean) => (cliConfigEnabled = v)"
          />
        </div>

        <!-- CLI 类型选择 -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliType') }}</label>
          <a-select
            v-model:value="selectedCLItype"
            :placeholder="t('remoteChannels.selectCliType')"
            :loading="cliDiscoveryLoading"
            style="width: 100%"
            @change="onCLItypeChange"
          >
            <a-select-option v-for="cli in installedCLIs" :key="cli.type" :value="cli.type">
              {{ cli.name }}
              <span class="cli-version">{{ cli.version }}</span>
            </a-select-option>
          </a-select>
        </div>

        <!-- 模型选择 -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliModel') }}</label>
          <a-select
            v-model:value="selectedCLImodel"
            :placeholder="t('remoteChannels.selectCliModel')"
            :disabled="!selectedCLItype"
            style="width: 100%"
          >
            <a-select-option v-for="model in cliModels" :key="model" :value="model">
              {{ model }}
            </a-select-option>
          </a-select>
        </div>

        <!-- 工作区目录：兜底任务自动创建时使用的工作目录；留空使用默认 ~/goteams/wechat_workspace -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.workspaceDir') }}</label>
          <DirectoryPicker
            v-model:value="selectedWorkspace"
            :candidates="workspaceCandidates"
            :placeholder="t('remoteChannels.workspaceDirPlaceholder')"
            :empty-text="t('remoteChannels.workspaceEmpty')"
            :aria-label="t('remoteChannels.workspaceDir')"
          />
        </div>

        <!-- IN-007: 两个按钮，左侧稍后配置（取消），右侧确认配置（保存），不再有第三个取消按钮 -->
        <div class="cli-config-actions">
          <a-button class="cli-skip-button" @click="skipCLIconfig">{{ t('remoteChannels.skipCLIConfig') }}</a-button>
          <a-button type="primary" :loading="cliConfigSaving" :disabled="!selectedCLItype || !selectedCLImodel" @click="saveCLIconfig">
            {{ t('remoteChannels.confirmCLIConfig') }}
          </a-button>
        </div>
      </div>
    </a-modal>
  </section>
</template>

<style scoped>
.remote-page { height: 100%; min-height: 0; overflow: auto; background: #fff; color: #262626; }
.page-header { height: 64px; display: flex; align-items: center; padding: 0 24px; border-bottom: 1px solid #f0f0f0; }
.page-header h1 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.remote-content { padding: 24px; }
.intro-banner { display: flex; gap: 12px; align-items: flex-start; padding: 16px; background: #eaf0ff; border-radius: 12px; font-size: 14px; line-height: 22px; }
.intro-banner strong { font-weight: 600; }
.intro-banner p { margin: 4px 0 0; color: #595959; }
.info-symbol { display: inline-grid; place-items: center; width: 18px; height: 18px; flex: 0 0 18px; margin-top: 2px; border-radius: 50%; background: #3157e2; color: #fff; font-size: 12px; font-weight: 700; }
.state-message { padding: 24px 0; display: flex; align-items: center; gap: 12px; color: #595959; }
.status-alert { margin-top: 16px; }
.uncertain-panel { margin-top: 24px; padding: 20px; border: 1px solid #d9d9d9; border-radius: 12px; background: #fff; }
.uncertain-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.uncertain-heading h2 { margin: 0; font-size: 16px; line-height: 24px; font-weight: 600; }
.uncertain-error { margin: 12px 0 0; color: #fb363f; font-size: 14px; line-height: 22px; }
.uncertain-empty { margin: 12px 0 0; color: #8c8c8c; font-size: 14px; line-height: 22px; }
.uncertain-item { display: flex; flex-direction: column; gap: 8px; padding: 12px 0; border-bottom: 1px solid #f0f0f0; }
.uncertain-item:last-child { border-bottom: 0; padding-bottom: 0; }
.uncertain-task-line { display: flex; align-items: flex-start; gap: 8px; }
.uncertain-hash { flex: 0 0 auto; color: #262626; font-size: 14px; font-weight: 600; line-height: 22px; user-select: none; }
.uncertain-details { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.uncertain-details strong { font-size: 14px; line-height: 22px; color: #262626; }
.uncertain-details span { color: #8c8c8c; font-size: 12px; line-height: 20px; overflow-wrap: anywhere; }
.uncertain-actions { display: flex; align-items: center; gap: 12px; padding-left: 24px; }
.channel-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 16px; }
.channel-card { min-width: 0; padding: 20px; border: 1px solid #e5e7eb; border-radius: 14px; background: #fff; }
.card-heading { display: flex; align-items: center; gap: 12px; }
.card-heading h2 { min-width: 0; margin: 0; flex: 1; font-size: 16px; font-weight: 600; line-height: 24px; }
.channel-card p { margin: 16px 0 0; color: #595959; font-size: 14px; line-height: 22px; }
.channel-action { flex: 0 0 auto; }
.wechat-action-wrap { position: relative; display: inline-flex; flex: 0 0 auto; }
.wechat-action-with-warning { padding-right: 28px; }
.wechat-reply-warning { position: absolute; top: 50%; right: 1px; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; border: 0; background: transparent; cursor: pointer; transform: translateY(-50%); }
.wechat-reply-warning::before { display: inline-flex; align-items: center; justify-content: center; width: 15px; height: 15px; border-radius: 50%; background: #fb363f; color: #fff; content: '!'; font-size: 11px; font-weight: 700; }
.wechat-reply-warning:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.wechat-reply-warning-text { display: block; max-width: 280px; }
.wechat-action:not(:disabled) { color: #3157e2; border-color: #3157e2; }
.channel-icon { display: inline-grid; place-items: center; width: 40px; height: 40px; flex: 0 0 40px; border-radius: 12px; color: white; font-size: 18px; font-weight: 700; }
.channel-icon svg { width: 28px; height: 28px; }
.wechat { background: #16a36a; }.wecom { background: #35a8ed; }.feishu { background: #4b79f5; }.dingtalk { background: #2783ee; }.app { background: #5b7ce7; }
.login-body { display: flex; align-items: center; flex-direction: column; padding: 12px 0 0; }
.scan-prompt { margin: 8px 0 16px; font-size: 14px; }
.qr-frame { display: grid; place-items: center; width: 240px; height: 240px; border: 1px solid #e5e7eb; border-radius: 12px; }
.qr-frame img { width: 224px; height: 224px; }
.login-state { min-height: 22px; margin: 16px 0; color: #595959; text-align: center; }
.login-state.error { color: #c63d43; }
.verify-row { display: flex; width: 100%; gap: 8px; margin-bottom: 12px; }
.close-button { align-self: flex-end; margin-top: 12px; }

/* S-UI001, IN-001: 旧的 CLI 配置按钮已被整块可点击的 .wechat-cli-panel 取代，保留占位以避免误删 */
.cli-config-button { display: none; }

/* IN-006, IN-007: 微信卡片 CLI 整块渲染区，作为打开配置弹窗的入口 */
.wechat-cli-panel { display: flex; align-items: center; gap: 10px; margin-top: 16px; padding: 10px 12px; border: 1px solid #e5e7eb; border-radius: 10px; background: #fafafa; cursor: pointer; transition: border-color 0.15s ease, background 0.15s ease; }
.wechat-cli-panel:hover { border-color: #722ed1; background: #f6f0ff; }
.wechat-cli-panel:focus-visible { outline: 2px solid #722ed1; outline-offset: 2px; }
.wechat-cli-panel .cli-settings-icon { display: inline-grid; place-items: center; width: 24px; height: 24px; flex: 0 0 24px; border-radius: 6px; background: #fff; color: #722ed1; border: 1px solid #e5e7eb; font-size: 14px; }
.wechat-cli-panel .cli-enabled-switch { flex: 0 0 auto; margin-left: auto; pointer-events: auto; }

/* S-UI001: 微信卡片 CLI 状态展示行（被包裹在 .wechat-cli-panel 内） */
.wechat-cli-status { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; flex: 1; }
.wechat-cli-status .cli-label { color: #8c8c8c; }
.wechat-cli-status .cli-name { color: #262626; font-weight: 500; }
.wechat-cli-status .cli-model { color: #262626; }
.wechat-cli-status .cli-sep { color: #bfbfbf; }
.wechat-cli-status .cli-unset { color: #bfbfbf; }

/* S-UI001: CLI 配置弹窗样式 */
.cli-config-body { padding: 8px 0; }
.cli-config-hint { margin: 0 0 16px; color: #595959; font-size: 14px; line-height: 22px; }
.form-item { margin-bottom: 16px; }
.form-label { display: block; margin-bottom: 8px; font-size: 14px; font-weight: 500; color: #262626; }
.cli-version { margin-left: 8px; color: #8c8c8c; font-size: 12px; }
.cli-enabled-row { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border: 1px solid #e5e7eb; border-radius: 8px; background: #fafafa; }
.cli-enabled-row .form-label { margin: 0; }
.cli-enabled-label { cursor: default; }
/* IN-007: 弹窗按钮区：稍后配置与确认配置并排显示在右侧，组成一组按钮 */
.cli-config-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 24px; }

@media (max-width: 820px) { .channel-grid { grid-template-columns: 1fr; } .remote-content { padding: 16px; } .uncertain-actions { padding-left: 24px; } }
</style>
