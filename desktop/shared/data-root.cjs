'use strict'

const fs = require('node:fs')
const path = require('node:path')

// 与 internal/workspace.detectScheme 同一规则：新指针、新库或结构标记即 current；
// runtime.json 不参与判定。双根/双指针时新根优先。坏的新指针不回退旧数据。
function hasDatabase(pathImpl, existsSync, root) {
  return existsSync(pathImpl.join(root, 'data', 'base.db')) || existsSync(pathImpl.join(root, 'data', 'goteams.db'))
}

function isCurrentScheme(pathImpl, existsSync, home) {
  const currentDir = pathImpl.join(home, '.teambuddy')
  return existsSync(pathImpl.join(home, '.teambuddy.mlink')) ||
    hasDatabase(pathImpl, existsSync, currentDir) ||
    existsSync(pathImpl.join(currentDir, '.teambuddy-root'))
}

function resolveRuntimeJsonPath(home, { pathImpl = path, existsSync = fs.existsSync, readFileSync = fs.readFileSync } = {}) {
  const useCurrent = isCurrentScheme(pathImpl, existsSync, home) ||
    !(existsSync(pathImpl.join(home, '.goteams')) || existsSync(pathImpl.join(home, '.goteams.mlink')))
  const dirName = useCurrent ? '.teambuddy' : '.goteams'
  const linkName = useCurrent ? '.teambuddy.mlink' : '.goteams.mlink'
  const fallback = pathImpl.join(home, dirName, 'runtime', 'runtime.json')
  try {
    const target = String(readFileSync(pathImpl.join(home, linkName), 'utf8')).trim()
    if (!target) return fallback
    const relocated = pathImpl.join(target, 'runtime', 'runtime.json')
    if (existsSync(relocated)) return relocated
  } catch {
    // 还没搬迁过，或当前方案指针不可读。保持该方案的 HOME fallback，不改读另一套根。
  }
  return fallback
}

module.exports = { resolveRuntimeJsonPath }
