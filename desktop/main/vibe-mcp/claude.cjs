'use strict'

const os = require('node:os')
const path = require('node:path')
const { prepareJsonMcp } = require('./common.cjs')

function claudeConfigFiles(homeDir, workDir) {
  const files = [path.join(homeDir, '.claude.json')]
  if (workDir) files.push(path.join(workDir, '.mcp.json'))
  return files
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string }} [options] */
async function prepareClaude({ backendPath, workDir, homeDir = os.homedir() } = {}) {
  return prepareJsonMcp({
    backendPath,
    workDir,
    files: claudeConfigFiles(homeDir, workDir),
  })
}

module.exports = { prepareClaude, claudeConfigFiles }
