'use strict'

const os = require('node:os')
const path = require('node:path')
const { prepareJsonMcp } = require('./common.cjs')

function codebuddyConfigFiles(homeDir, workDir) {
  const files = [
    path.join(homeDir, '.codebuddy', '.mcp.json'),
    path.join(homeDir, '.codebuddy', 'mcp.json'),
    path.join(homeDir, '.codebuddy.json'),
    path.join(homeDir, '.codebuddy', 'settings.json'),
  ]
  if (workDir) {
    files.push(
      path.join(workDir, '.mcp.json'),
      path.join(workDir, 'mcp.json'),
      path.join(workDir, '.codebuddy', 'settings.json'),
      path.join(workDir, '.codebuddy', 'settings.local.json'),
    )
  }
  return files
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string }} [options] */
async function prepareCodeBuddy({ backendPath, workDir, homeDir = os.homedir() } = {}) {
  return prepareJsonMcp({
    backendPath,
    workDir,
    files: codebuddyConfigFiles(homeDir, workDir),
  })
}

module.exports = { prepareCodeBuddy, codebuddyConfigFiles }
