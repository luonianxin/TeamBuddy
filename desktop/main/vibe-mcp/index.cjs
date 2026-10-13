'use strict'

const { prepareClaude } = require('./claude.cjs')
const { prepareCodeBuddy } = require('./codebuddy.cjs')
const { withMcpChannel } = require('./common.cjs')
const { prepareCodexCLI } = require('./codex-cli.cjs')
const { preparePi } = require('./pi.cjs')
const { prepareQoder } = require('./qoder.cjs')

const preparers = Object.freeze({
  codex_cli: prepareCodexCLI,
  claude: prepareClaude,
  codebuddy: prepareCodeBuddy,
  qoder: prepareQoder,
  pi: preparePi,
})

async function prepareVibeMCP(tool, options = {}) {
  const prepare = preparers[tool]
  if (!prepare) return { channel: 'script', prefixArgs: [] }
  return prepare(/** @type {any} */ (options))
}

module.exports = { prepareVibeMCP, withMcpChannel }
