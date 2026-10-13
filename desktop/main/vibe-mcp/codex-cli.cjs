'use strict'

const fs = require('node:fs/promises')
const os = require('node:os')
const path = require('node:path')

const CHANNEL_MCP = '回传通道：mcp'

function tomlBasicString(value) {
  return `"${String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

function configNamesServer(text) {
  if (!text) return false
  if (/^\s*\[mcp_servers\.teamsboard(?:\.|\])/m.test(text)) return true
  if (/^\s*mcp_servers\.teamsboard(?:\.|\s*=)/m.test(text)) return true
  return text.split(/^\s*\[/m).some(block => {
    if (!block.startsWith('mcp_servers]')) return false
    const body = block.slice(block.indexOf(']') + 1)
    return /^\s*teamsboard\s*=/m.test(body)
  })
}

function mcpBackendPath(command) {
  return typeof command === 'string' && path.isAbsolute(command) ? command : ''
}

async function readConfig(file) {
  try {
    const data = await fs.readFile(file)
    if (data.length > 1024 * 1024) return null
    return data.toString('utf8')
  } catch {
    return ''
  }
}

/** @param {{ backendPath?: string, workDir?: string, homeDir?: string, codexHome?: string }} [options] */
async function prepareCodexCLI({ backendPath, workDir, homeDir = os.homedir(), codexHome } = {}) {
  const home = codexHome || path.join(homeDir, '.codex')
  const files = [path.join(home, 'config.toml')]
  if (typeof workDir === 'string' && workDir !== '') {
    files.push(path.join(workDir, '.codex', 'config.toml'))
  }
  for (const file of files) {
    const text = await readConfig(file)
    if (text == null || configNamesServer(text)) {
      return { channel: 'script', prefixArgs: [] }
    }
  }
  const command = mcpBackendPath(backendPath)
  if (command === '') return { channel: 'script', prefixArgs: [] }
  return {
    channel: 'mcp',
    prefixArgs: [
      '-c', `mcp_servers.teamsboard.command=${tomlBasicString(command)}`,
      '-c', 'mcp_servers.teamsboard.args=["mcp"]',
    ],
  }
}

function withCodexChannel(prompt, channel) {
  if (channel !== 'mcp' || String(prompt).includes('回传通道：')) return prompt
  return `${prompt}\n${CHANNEL_MCP}`
}

module.exports = {
  prepareCodexCLI,
  withCodexChannel,
  mcpBackendPath,
  configNamesServer,
}
