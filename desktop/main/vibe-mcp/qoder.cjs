'use strict'

const os = require('node:os')
const path = require('node:path')
const { prepareJsonMcp } = require('./common.cjs')

function qoderConfigFiles(homeDir, workDir) {
  const files = [path.join(homeDir, '.qoder', 'settings.json')]
  if (workDir) {
    files.push(
      path.join(workDir, '.qoder', 'settings.json'),
      path.join(workDir, '.qoder', 'settings.local.json'),
      path.join(workDir, '.mcp.json'),
    )
  }
  return files
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string }} [options] */
async function prepareQoder({ backendPath, workDir, homeDir = os.homedir() } = {}) {
  return prepareJsonMcp({
    backendPath,
    workDir,
    files: qoderConfigFiles(homeDir, workDir),
  })
}

module.exports = { prepareQoder, qoderConfigFiles }
