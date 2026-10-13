// 只活在当前渲染进程里。切到其他页面再回来时恢复输入，关掉客户端后自然消失。
const drafts = new Map<string, string>()

export const NEW_CONVERSATION_DRAFT_KEY = 'new-conversation'

export function readComposerDraft(key: string) {
  return key ? drafts.get(key) ?? '' : ''
}

export function writeComposerDraft(key: string, value: string) {
  if (!key) return
  if (value) drafts.set(key, value)
  else drafts.delete(key)
}
