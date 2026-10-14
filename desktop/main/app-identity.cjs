'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const APP_NAME = 'TeamBuddy'
const LEGACY_USER_DATA_DIRNAME = 'TeamsBoard'
const USER_DATA_DIRNAME = 'TeamBuddy'
const APP_USER_MODEL_ID = 'cn.goteams.client'
const APP_ICON_FILENAME =
  process.platform === 'darwin'
    ? 'icon-macos.png'
    : process.platform === 'win32'
      ? 'icon-windows.png'
      : 'icon.png'
const APP_ICON_PATH = path.join(__dirname, '..', 'build', APP_ICON_FILENAME)
const APP_TRAY_ICON_FILENAME = process.platform === 'darwin' ? 'trayTemplate.png' : 'icon.png'
const APP_TRAY_ICON_PATH = path.join(__dirname, '..', 'build', APP_TRAY_ICON_FILENAME)
const SINGLETON_LOCK_FILENAME = 'SingletonLock'

// Electron scopes its single-instance lock to userData. Development launches
// use package.json's name while packaged launches use productName, so pinning
// this path lets both forms participate in the same single-instance lifecycle.
// 历史桌面目录 TeamsBoard 保留。新版本直接使用 TeamBuddy，不改名、不删除、不把 userData 指回旧目录。
/**
 * @param {string} appData
 * @param {typeof path} [pathImpl]
 */
function resolveUserDataDir(appData, pathImpl = path) {
  return pathImpl.join(appData, USER_DATA_DIRNAME)
}

function isPidAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    return Boolean(error && error.code === 'EPERM')
  }
}

// Chromium POSIX 单实例锁是指向「主机名-PID」的符号链接，目标不必存在。
// 只读 readlink；不存在、不是符号链接、主机名不符或 PID 已退出都不是本机存活实例。
/**
 * @param {string} target
 * @param {string} hostname
 * @param {(pid: number) => boolean} pidAlive
 */
function singletonLockTargetIsLive(target, hostname, pidAlive) {
  if (typeof target !== 'string' || target.length === 0 || target.includes('\0')) return false
  const dash = target.lastIndexOf('-')
  if (dash <= 0 || dash >= target.length - 1) return false
  const lockHost = target.slice(0, dash)
  const pidText = target.slice(dash + 1)
  if (!/^[1-9][0-9]*$/.test(pidText)) return false
  const pid = Number(pidText)
  if (!Number.isSafeInteger(pid)) return false
  if (lockHost !== hostname) return false
  return pidAlive(pid) === true
}

/**
 * @param {string} userDataDir
 * @param {{ fsImpl?: typeof fs, pathImpl?: typeof path, hostname?: string, isPidAlive?: (pid: number) => boolean }} [options]
 */
function legacyProfileHasLiveProcess(userDataDir, {
  fsImpl = fs,
  pathImpl = path,
  hostname = os.hostname(),
  isPidAlive: pidAlive = isPidAlive,
} = {}) {
  let target
  try {
    target = fsImpl.readlinkSync(pathImpl.join(userDataDir, SINGLETON_LOCK_FILENAME))
  } catch {
    return false
  }
  return singletonLockTargetIsLive(target, hostname, pidAlive)
}

/**
 * 先调用 Electron requestSingleInstanceLock，重复启动才能通知已有进程。
 * 拿到锁之后，macOS/Linux 才检查旧 TeamsBoard profile 的 SingletonLock。
 * Windows 旧进程由 Go sidecar 全局实例锁负责；数据根复制锁也在 Go workspace 层，这里不读 SingletonLock、不实现迁移锁。
 * @param {string} appData
 * @param {() => unknown} requestSingleInstanceLock
 * @param {{ platform?: NodeJS.Platform, fsImpl?: typeof fs, pathImpl?: typeof path, hostname?: string, isPidAlive?: (pid: number) => boolean }} [options]
 * @returns {boolean} 本进程是否应继续启动
 */
function shouldStartInstance(appData, requestSingleInstanceLock, {
  platform = process.platform,
  fsImpl = fs,
  pathImpl = path,
  hostname = os.hostname(),
  isPidAlive: pidAlive = isPidAlive,
} = {}) {
  if (requestSingleInstanceLock() !== true) return false
  if (platform === 'win32') return true
  const legacyDir = pathImpl.join(appData, LEGACY_USER_DATA_DIRNAME)
  return !legacyProfileHasLiveProcess(legacyDir, { fsImpl, pathImpl, hostname, isPidAlive: pidAlive })
}

function configureAppIdentity(app) {
  app.setName(APP_NAME)
  app.setPath('userData', resolveUserDataDir(app.getPath('appData')))
  if (process.platform === 'win32') app.setAppUserModelId(APP_USER_MODEL_ID)
  if (process.platform === 'darwin' && app.dock) app.dock.setIcon(APP_ICON_PATH)
}

module.exports = {
  APP_ICON_PATH,
  APP_TRAY_ICON_PATH,
  APP_NAME,
  APP_USER_MODEL_ID,
  LEGACY_USER_DATA_DIRNAME,
  USER_DATA_DIRNAME,
  resolveUserDataDir,
  legacyProfileHasLiveProcess,
  shouldStartInstance,
  configureAppIdentity,
}
