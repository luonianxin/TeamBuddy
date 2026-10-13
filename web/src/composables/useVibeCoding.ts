import codexAppLogo from '@/assets/icons/codex-logo.svg'
import antigravityLogo from '@/assets/icons/vibe-antigravity.svg'
import { cliLogo } from '@/composables/useCliLogo'

export const VIBE_CODING_TOOLS = [
  { id: 'codex', cliType: '', launch: 'app' },
  { id: 'codex_cli', cliType: 'codex', launch: 'terminal' },
  { id: 'pi', cliType: 'pi', launch: 'terminal' },
  { id: 'antigravity', cliType: 'antigravity', launch: 'terminal' },
  { id: 'qoder', cliType: 'qoder', launch: 'terminal' },
  { id: 'codebuddy', cliType: 'codebuddy', launch: 'terminal' },
  { id: 'claude', cliType: 'claude', launch: 'terminal' },
] as const

export type VibeCodingToolId = (typeof VIBE_CODING_TOOLS)[number]['id']

const RECENT_VIBE_TOOL_KEY = 'goteams.vibe-coding.recent-tool'

export function isVibeCodingTool(tool: string): tool is VibeCodingToolId {
  return VIBE_CODING_TOOLS.some((item) => item.id === tool)
}

export function vibeToolLabelKey(tool: string) {
  return isVibeCodingTool(tool)
    ? `workflows.task.assign.vibeTools.${tool}`
    : 'workflows.task.assign.vibeCodingMode'
}

// Codex 应用保留彩色应用标识；其余工具与 CLI 选择器共用同一套本地厂家图标。
const VIBE_TOOL_LOGOS: Record<VibeCodingToolId, string> = {
  codex: codexAppLogo,
  codex_cli: cliLogo('codex'),
  pi: cliLogo('pi'),
  antigravity: antigravityLogo,
  qoder: cliLogo('qoder'),
  codebuddy: cliLogo('codebuddy'),
  claude: cliLogo('claude'),
}

export function vibeToolLogo(tool: string) {
  return isVibeCodingTool(tool) ? VIBE_TOOL_LOGOS[tool] : codexAppLogo
}

export function readRecentVibeTool() {
  try {
    const value = localStorage.getItem(RECENT_VIBE_TOOL_KEY) || ''
    return isVibeCodingTool(value) ? value : ''
  } catch {
    return ''
  }
}

export function writeRecentVibeTool(tool: string) {
  if (!isVibeCodingTool(tool)) return
  try {
    localStorage.setItem(RECENT_VIBE_TOOL_KEY, tool)
  } catch {
    // 最近使用只影响角标，存储失败时保持当前选择可用。
  }
}
