'use strict'

const path = require('node:path')

const APP_NAME = 'TeamBuddy'
// Keep the historical directory so upgrades retain desktop preferences and cached updates.
const USER_DATA_DIRNAME = 'TeamsBoard'
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

// Electron scopes its single-instance lock to userData. Development launches
// use package.json's name while packaged launches use productName, so pinning
// this path lets both forms participate in the same single-instance lifecycle.
function configureAppIdentity(app) {
  app.setName(APP_NAME)
  app.setPath('userData', path.join(app.getPath('appData'), USER_DATA_DIRNAME))
  if (process.platform === 'win32') app.setAppUserModelId(APP_USER_MODEL_ID)
  if (process.platform === 'darwin' && app.dock) app.dock.setIcon(APP_ICON_PATH)
}

module.exports = {
  APP_ICON_PATH,
  APP_TRAY_ICON_PATH,
  APP_NAME,
  APP_USER_MODEL_ID,
  configureAppIdentity,
}
