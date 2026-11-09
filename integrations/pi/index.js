// Persona runtime for pi (docs/integrations/pi.md): a pi extension that puts the sessions of this pi process — a terminal, an RPC
// host, or pi-web's server — into the Persona apps through the personad node on this machine
// (~/.persona/runtime.sock, docs/integrations/runtimes.md). It does nothing visible until a node listens.
//
// Configuration (optional): <agent dir>/persona.json — { "approve": ["bash"], "askUser": true, "answerFirst": "auto",
// "autoUpdate": true }.
//
// Updates (docs/integrations/pi.md "Auto-update"): installed from a store (pi's settings name <store>/current/pi), this extension takes
// the version the node carries — installs it as current and, once its sessions are idle, has each of them reload
// (pi reads extensions afresh on reload), so the new code runs in this same process. Installed from anywhere else (a
// checkout), it never updates itself.

import { readFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { SessionManager } from '@earendil-works/pi-coding-agent'
import { Type } from 'typebox'
import { envValue } from '../runtime-sdk/lib/identity.js'
import { selfPackage } from '../runtime-sdk/index.js'
import { piConfigPath, sharedPiGlobals } from './lib/compat.js'
import { createPiHost, herdrPaneOf, piWebOf } from './lib/pi.js'

const SELF = selfPackage(import.meta.url, 'persona-pi-runtime')

function config() {
  const dir = process.env.PI_CODING_AGENT_DIR || join(homedir(), '.pi', 'agent')
  try {
    return JSON.parse(readFileSync(piConfigPath(dir), 'utf8'))
  } catch {
    return {}
  }
}

// One host per process and version: pi loads this module once and calls the factory once per session; a reload
// that brings a new version loads it again, and that version's host takes the process over from the one before.
function host() {
  sharedPiGlobals()
  const slot = (globalThis.__personaPi ??= {})
  if (slot.host && slot.hash === SELF.hash) {
    if (slot.host.retired) slot.host.revive() // a switch that brought this same version back
    return slot.host
  }
  slot.host?.stop() // an older version's: its sessions are reloading onto this one
  if (globalThis.__personaPiHost) { // the host of a version from before self-update
    try {
      globalThis.__personaPiHost.stop()
    } catch {}
    globalThis.__personaPiHost = undefined
  }
  const c = config()
  const web = piWebOf()
  slot.hash = SELF.hash
  slot.host = createPiHost({
    SessionManager,
    web,
    // A pi in a herdr pane: the node shows it as herdr's session (pi-web's server may run in a pane too: not it); a
    // pane of a named herdr session comes with the session (herdrPaneOf).
    paneId: web ? undefined : herdrPaneOf(process.env),
    log: (m) => { if (envValue('PERSONA_PI_DEBUG')) console.error(m) },
  }, {
    socketPath: typeof c.socket === 'string' && c.socket ? c.socket : undefined,
    answerFirst: ['auto', 'phone', 'local'].includes(c.answerFirst) ? c.answerFirst : 'auto',
    approve: Array.isArray(c.approve) ? c.approve.filter((t) => typeof t === 'string') : [],
    // The same process to the node across versions: the next version's host replaces this connection.
    instance: `pi-${process.pid}`,
    update: SELF.store && c.autoUpdate !== false ? { name: SELF.name, hash: SELF.hash, store: SELF.store } : undefined,
  })
  globalThis.__personaPiAskUser = c.askUser !== false
  return slot.host
}

export default function persona(pi) {
  // Not for one-shot runs (pi -p, --mode json): nothing to drive from the phone, outside pi-web's server.
  if (!piWebOf() && process.argv.some((a) => a === '-p' || a === '--print' || a === 'json')) return
  host().register(pi, globalThis.__personaPiAskUser ? { Type } : undefined)
}
