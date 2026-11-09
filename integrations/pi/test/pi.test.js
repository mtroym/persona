import { wirePackageName } from '../../runtime-sdk/lib/compat.js'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readlinkSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { packageHash } from '../../runtime-sdk/lib/package.js'
import { createPiHost, herdrPaneOf, piWebOf } from '../lib/pi.js'

/** A pi session manager over entries (file order); the branch runs from the leaf back through parentId. */
function sessionManager(id, cwd, entries = [], name = '') {
  const sm = {
    entries,
    leaf: entries.at(-1)?.id ?? null,
    getSessionId: () => id,
    getSessionFile: () => `/pi/sessions/${id}.jsonl`,
    getCwd: () => cwd,
    getSessionName: () => name,
    getEntries: () => sm.entries,
    getBranch() {
      const byId = new Map(sm.entries.map((e) => [e.id, e]))
      const out = []
      for (let e = byId.get(sm.leaf); e; e = byId.get(e.parentId)) out.unshift(e)
      return out
    },
    append(e) {
      sm.entries = [...sm.entries, e]
      sm.leaf = e.id
    },
  }
  return sm
}

const msg = (id, parentId, role, text) => ({ type: 'message', id, parentId, timestamp: new Date().toISOString(),
  message: role === 'user' ? { role, content: text } : { role, content: [{ type: 'text', text }], stopReason: 'stop' } })

/** One pi session: its extension API and context, and a way to fire its events. */
function piSession(id, cwd = '/w/app', entries = [], mode = 'rpc') {
  const handlers = new Map()
  const sm = sessionManager(id, cwd, entries)
  const ui = { confirms: [], selects: [], answer: undefined }
  const ctx = {
    sessionManager: sm, mode, hasUI: true, model: { id: 'model-x' }, signal: undefined, aborted: 0,
    abort() { ctx.aborted++ },
    ui: {
      confirm: async (t, m) => { ui.confirms.push([t, m]); return ui.answer === true },
      select: async (t, o) => { ui.selects.push([t, o]); return ui.answer },
      input: async () => ui.answer,
    },
  }
  const pi = {
    sent: [], names: [], tools: [],
    on(event, fn) { (handlers.get(event) ?? handlers.set(event, []).get(event)).push(fn) },
    registerTool(t) { pi.tools.push(t) },
    async sendUserMessage(content, opts) { pi.sent.push({ content, opts }) },
    setSessionName(n) { pi.names.push(n) },
    appendEntry(customType, data) { sm.append({ type: 'custom', id: `c${sm.entries.length}`, parentId: sm.leaf, customType, data }) },
  }
  const fire = async (event, e = {}) => {
    let out
    for (const fn of handlers.get(event) ?? []) out = await fn(e, ctx)
    return out
  }
  return { pi, ctx, sm, ui, fire }
}

async function setup(t, opts = {}) {
  const n = await startFakeNode(t)
  const posts = []
  const deps = {
    SessionManager: {
      listAll: async () => opts.disk ?? [],
      open: (path) => opts.files?.[path] ?? sessionManager('x', '/', []),
    },
    web: opts.web ? { port: 30141, host: '127.0.0.1', password: 'pw' } : undefined,
    fetch: async (url, init) => {
      posts.push({ url, body: JSON.parse(init.body), auth: init.headers.authorization })
      return new Response(JSON.stringify(opts.reply ?? { success: true, sessionId: 'new-1' }), { status: 200 })
    },
    pid: 4242,
    paneId: opts.paneId,
  }
  const host = createPiHost(deps, { socketPath: n.path, retryMs: 20, pushDelayMs: 5, approve: opts.approve, answerFirst: opts.answerFirst, listDisk: opts.listDisk,
    update: opts.update, instance: opts.instance })
  t.after(() => host.stop())
  return { n, host, posts }
}

test('a pi process lists its running sessions to the node as one runtime', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-a', '/w/app', [msg('u1', null, 'user', 'fix the build'), msg('a1', 'u1', 'assistant', 'Fixed.')])
  const b = piSession('s-b', '/w/lib')
  host.register(a.pi)
  host.register(b.pi)
  await a.fire('session_start')
  await b.fire('session_start')
  await n.ready
  assert.equal(n.hello.runtime, 'pi-4242')
  assert.equal(n.hello.agent, 'pi')
  assert.equal(n.hello.launcher, undefined, 'a terminal pi starts no sessions')
  await until('both listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  const rows = Object.fromEntries(n.last('runtime.sessions').sessions.map((s) => [s.session_id, s]))
  assert.equal(rows['s-a'].title, 'fix the build')
  assert.equal(rows['s-a'].status, 'done')
  assert.equal(rows['s-a'].last_text, 'Fixed.')
  assert.equal(rows['s-a'].rev, 2)
  assert.equal(rows['s-b'].status, 'idle')
  await b.fire('session_shutdown')
  await until('b gone', () => n.last('runtime.sessions')?.sessions.length === 1)
})

test('pages the branch; sends into the turn (steer while working); interrupts; renames', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-p', '/w/app', [msg('u1', null, 'user', 'one'), msg('a1', 'u1', 'assistant', 'a1'), msg('u2', 'a1', 'user', 'two'), msg('a2', 'u2', 'assistant', 'a2')])
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  const p = await n.peer.call('session.items', { session_id: 's-p', turns: 1 })
  assert.deepEqual(p.items.map((i) => i.text), ['two', 'a2'])
  assert.equal(p.to_seq, 4)
  assert.match(p.history, /^pi:s-p:/)
  await n.peer.call('session.send', { session_id: 's-p', text: 'go on', client_msg_id: 'm1' })
  assert.deepEqual(a.pi.sent.at(-1), { content: 'go on', opts: undefined })
  await n.peer.call('session.send', { session_id: 's-p', text: 'go on', client_msg_id: 'm1' }) // a retry
  assert.equal(a.pi.sent.length, 1)
  await a.fire('agent_start')
  await n.peer.call('session.send', { session_id: 's-p', text: 'also this', images: [{ mime: 'image/png', data: 'AAAA' }], client_msg_id: 'm2' })
  assert.deepEqual(a.pi.sent.at(-1), { content: [{ type: 'text', text: 'also this' }, { type: 'image', data: 'AAAA', mimeType: 'image/png' }], opts: { deliverAs: 'steer' } })
  await n.peer.call('session.interrupt', { session_id: 's-p' })
  assert.equal(a.ctx.aborted, 1)
  await n.peer.call('session.rename', { session_id: 's-p', title: 'Build fix' })
  assert.deepEqual(a.pi.names, ['Build fix'])
  await assert.rejects(n.peer.call('session.send', { session_id: 'elsewhere', text: 'x' }), /没有在这里运行/)
})

test('a watched session gets what it writes as continuous batches, its live text as deltas, and a new history on another branch', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-w', '/w/app', [msg('u1', null, 'user', 'one'), msg('a1', 'u1', 'assistant', 'a1')])
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await n.peer.call('session.watch', { session_id: 's-w', since_seq: 0 })
  await a.fire('agent_start')
  await a.fire('message_update', { assistantMessageEvent: { type: 'text_delta', delta: 'He', partial: { content: [{ type: 'text', text: 'He' }] } } })
  await until('delta', () => n.all('session.delta').some((d) => d.text === 'He'))
  a.sm.append(msg('u2', 'a1', 'user', 'two'))
  await a.fire('message_end')
  a.sm.append(msg('a2', 'u2', 'assistant', 'Hello'))
  await a.fire('message_end')
  await a.fire('agent_settled')
  await until('pushed', () => n.all('session.items').flatMap((b) => b.items).some((i) => i.text === 'Hello'))
  const batches = n.all('session.items')
  assert.deepEqual(batches.slice(1).filter((b, i) => b.from_seq !== batches[i].to_seq), [])
  assert.equal(batches.at(-1).to_seq, 4)
  const first = batches.at(-1).history
  // An edit of "two": a branch off a1, appended at the end of the file.
  a.sm.append(msg('u2b', 'a1', 'user', 'two, again'))
  await a.fire('message_end')
  await until('the new branch', () => n.all('session.items').at(-1)?.history !== first)
  assert.deepEqual(n.all('session.items').at(-1).items.map((i) => i.text), ['two, again'])
})

test('a move to another branch already in the file goes out as another history, and the list sees it', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-t', '/w/app', [msg('u1', null, 'user', 'one'), msg('a1', 'u1', 'assistant', 'a1'),
    msg('u2', 'a1', 'user', 'two'), msg('a2', 'u2', 'assistant', 'answer two'),
    msg('u2b', 'a1', 'user', 'two, again'), msg('a2b', 'u2b', 'assistant', 'answer two again')])
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  const rev = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 's-t')?.rev
  await until('listed', () => rev() === 6)
  // Not watched: back on the first branch (/tree), nothing written.
  a.sm.leaf = 'a2'
  await a.fire('session_tree')
  await until('the list moves', () => rev() !== 6)
  await n.peer.call('session.watch', { session_id: 's-t', since_seq: 0 })
  const page = await n.peer.call('session.items', { session_id: 's-t', turns: 10 })
  assert.equal(page.history, 'pi:s-t:root')
  assert.deepEqual(page.items.map((i) => i.text), ['one', 'a1', 'two', 'answer two'])
  // Watched: to the other branch, then to an earlier point of it.
  a.sm.leaf = 'a2b'
  await a.fire('session_tree')
  await until('pushed as another history', () => n.last('session.items')?.history === 'pi:s-t:u2b')
  assert.equal(n.last('session.items').to_seq, 6)
  a.sm.leaf = 'a1'
  await a.fire('session_tree')
  await until('an earlier point is another history too', () => n.last('session.items')?.history === 'pi:s-t:root@a1')
  assert.deepEqual((await n.peer.call('session.items', { session_id: 's-t', turns: 10 })).items.map((i) => i.text), ['one', 'a1'])
  assert.equal(host.live.get('s-t').key, 'root@a1')
  // Writing from there: a third branch, its entries pushed on from its seq.
  a.sm.append(msg('u2c', 'a1', 'user', 'two, a third time'))
  await a.fire('message_end')
  await until('the new entry', () => n.last('session.items')?.items.some((i) => i.text === 'two, a third time'))
  assert.equal(n.last('session.items').history, 'pi:s-t:u2c')
  assert.equal(n.last('session.items').to_seq, 7)
  assert.equal(rev() >= 7, true)
})

test('what a watched session writes is pushed cut to the page limits, as its pages are', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-big', '/w/app', [msg('u1', null, 'user', 'one'), msg('a1', 'u1', 'assistant', 'a1')])
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await n.peer.call('session.watch', { session_id: 's-big', since_seq: 0 })
  const output = 'line of output\n'.repeat(2000) // 30 KB
  a.sm.append({ type: 'message', id: 'r1', parentId: 'a1', timestamp: new Date().toISOString(),
    message: { role: 'toolResult', toolCallId: 'call_1', toolName: 'bash', isError: false, content: [{ type: 'text', text: output }] } })
  await a.fire('message_end')
  const pushed = await until('pushed', () => n.all('session.items').flatMap((b) => b.items).find((i) => i.kind === 'tool_result'))
  const paged = (await n.peer.call('session.items', { session_id: 's-big', limit: 1 })).items.find((i) => i.kind === 'tool_result')
  assert.equal(pushed.full_size, Buffer.byteLength(output), 'cut, with the size of the whole')
  assert.ok(Buffer.byteLength(pushed.text) < 5 * 1024, 'at most the 4 KB a page carries')
  assert.deepEqual(pushed, paged, 'the push and the page say the same')
})

test('an unwatched session skips item mapping, keeps branch bookkeeping, and catches up when watched', async (t) => {
  const { n, host } = await setup(t)
  const a = piSession('s-later', '/w/app', [msg('u1', null, 'user', 'one'), msg('a1', 'u1', 'assistant', 'a1')])
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.some((s) => s.session_id === 's-later'))
  let mapped = 0
  const timestamp = a.sm.entries[0].timestamp
  Object.defineProperty(a.sm.entries[0], 'timestamp', { get() { mapped++; return timestamp } })
  a.sm.append(msg('u2', 'a1', 'user', 'two'))
  await a.fire('message_end')
  a.sm.append(msg('u2b', 'a1', 'user', 'two, again'))
  await a.fire('message_end')
  assert.equal(mapped, 0, 'no item conversion while nobody watches')
  assert.equal(host.live.get('s-later').pushed, 4)
  assert.equal(host.live.get('s-later').key, 'u2b')
  assert.equal(n.all('session.items').length, 0)
  await until('unwatched revision is listed', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === 's-later')?.rev === 4)

  await n.peer.call('session.watch', { session_id: 's-later', since_seq: 2 })
  await until('caught up on the new branch', () => n.last('session.items')?.to_seq === 4)
  const catchup = n.last('session.items')
  assert.deepEqual(catchup.items.map((i) => i.text), ['two, again'])
  assert.equal(catchup.from_seq, 2)
  assert.equal(catchup.history, 'pi:s-later:u2b')
  assert.ok(mapped > 0, 'watch catch-up still converts the branch')
  a.sm.append(msg('a2b', 'u2b', 'assistant', 'new answer'))
  await a.fire('message_end')
  await until('live after catch-up', () => n.last('session.items')?.to_seq === 5)
  const live = n.last('session.items')
  assert.equal(live.from_seq, 4)
  assert.equal(live.history, catchup.history)
  assert.deepEqual(live.items.map((i) => i.text), ['new answer'])
})

test('the pi-web companion lists sessions on disk, opens one to send to it, and starts new ones through pi-web', async (t) => {
  const disk = [
    { id: 's-old', path: '/pi/s-old.jsonl', cwd: '/w/app', name: '', firstMessage: 'refactor the parser', modified: new Date(Date.now() - 3600_000), messageCount: 4 },
    { id: 's-ancient', path: '/pi/s-ancient.jsonl', cwd: '/w/app', modified: new Date(Date.now() - 30 * 86400_000), messageCount: 2 },
  ]
  const files = { '/pi/s-old.jsonl': sessionManager('s-old', '/w/app', [msg('u1', null, 'user', 'refactor the parser'), msg('a1', 'u1', 'assistant', 'Done.')]) }
  const { n, host, posts } = await setup(t, { web: true, disk, files, listDisk: true })
  const live = piSession('s-live')
  host.register(live.pi)
  await live.fire('session_start')
  await n.ready
  assert.equal(n.hello.runtime, 'pi-files')
  assert.equal(n.hello.launcher, 'pi-web')
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  const old = n.last('runtime.sessions').sessions.find((s) => s.session_id === 's-old')
  assert.equal(old.status, 'offline')
  assert.equal(old.title, 'refactor the parser')
  const p = await n.peer.call('session.items', { session_id: 's-old', turns: 2 })
  assert.deepEqual(p.items.map((i) => i.text), ['refactor the parser', 'Done.'])
  await n.peer.call('session.send', { session_id: 's-old', text: 'and the tests', client_msg_id: 'w1' })
  assert.equal(posts[0].url, 'http://127.0.0.1:30141/api/agent/s-old')
  assert.deepEqual(posts[0].body, { type: 'prompt', message: 'and the tests' })
  assert.equal(posts[0].auth, 'Basic ' + Buffer.from('pi:pw').toString('base64'))
  const { session } = await n.peer.call('session.start', { cwd: '/w/new', prompt: 'hello' })
  assert.equal(session.session_id, 'new-1')
  assert.deepEqual(posts[1].body, { cwd: '/w/new', type: 'prompt', message: 'hello' })
})

test('approvals of configured tools go to the phone when it drives the session; pi\'s dialog otherwise; no answer blocks', async (t) => {
  const { n, host } = await setup(t, { approve: ['bash'], answerFirst: 'phone' })
  const a = piSession('s-x')
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = () => ({ allow: true })
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c1', input: { command: 'rm -rf build' } }), undefined)
  assert.deepEqual([n.asked[0].tool_name, n.asked[0].summary, n.asked[0].tool_input.command], ['Bash', 'rm -rf build', 'rm -rf build'])
  n.resolve = () => ({ allow: false, message: 'not now' })
  assert.deepEqual(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c2', input: { command: 'ls' } }), { block: true, reason: 'not now' })
  assert.equal(await a.fire('tool_call', { toolName: 'read', toolCallId: 'c3', input: { path: 'x' } }), undefined, 'tools not listed run as pi runs them')
  n.resolve = () => ({ dismiss: true }) // handed back to the computer: pi's own dialog
  a.ui.answer = false
  assert.deepEqual(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c4', input: { command: 'ls' } }), { block: true, reason: '用户拒绝了' })
  assert.equal(a.ui.confirms.length, 1)
})

test('without a dialog (pi -p) an approval goes to the phone even when the phone does not drive the session', async (t) => {
  const { n, host } = await setup(t, { approve: ['bash'] }) // answerFirst auto: the local dialog first
  const a = piSession('s-noui')
  a.ctx.hasUI = false
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = () => ({ allow: true })
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c1', input: { command: 'make' } }), undefined)
  assert.equal(n.asked.length, 1)
  assert.equal(n.asked[0].summary, 'make')
  n.resolve = () => ({ allow: false, message: 'not that' })
  assert.deepEqual(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c2', input: { command: 'ls' } }), { block: true, reason: 'not that' })
  n.resolve = () => ({ dismiss: true }) // nobody answered there either
  assert.deepEqual(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c3', input: { command: 'ls' } }), { block: true, reason: '没有人批准（没有界面，手机也没回答）' })
  // With a dialog, it decides and the phone is not asked.
  a.ctx.hasUI = true
  a.ui.answer = true
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c4', input: { command: 'ls' } }), undefined)
  assert.equal(n.asked.length, 3)
  // No hub: blocked at once, not after waiting for one.
  a.ctx.hasUI = false
  n.setHub(false)
  await new Promise((r) => setTimeout(r, 50))
  assert.deepEqual(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c5', input: { command: 'ls' } }), { block: true, reason: '没有人批准（没有界面，手机也没回答）' })
  assert.equal(n.asked.length, 3)
})

test('总是同意 on a pi card is kept in the session: later calls of that tool run unasked, others still ask', async (t) => {
  const { n, host } = await setup(t, { approve: ['bash', 'write'], answerFirst: 'phone' })
  const a = piSession('s-al')
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = () => ({ allow: true })
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c1', input: { command: 'ls' } }), undefined)
  assert.equal(n.asked[0].always_label, '本会话不再问 Bash', 'the card says what 总是同意 allows')
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c2', input: { command: 'ls' } }), undefined)
  assert.equal(n.asked.length, 2, 'allowing once is not kept')
  n.resolve = () => ({ allow: true, always: true })
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c3', input: { command: 'make' } }), undefined)
  assert.equal(n.asked.length, 3)
  n.resolve = () => { throw new Error('asked again') }
  assert.equal(await a.fire('tool_call', { toolName: 'bash', toolCallId: 'c4', input: { command: 'rm -rf build' } }), undefined)
  assert.equal(n.asked.length, 3, 'the kept tool runs unasked')
  n.resolve = () => ({ allow: false })
  assert.deepEqual(await a.fire('tool_call', { toolName: 'write', toolCallId: 'c5', input: { path: 'x', content: '' } }), { block: true, reason: '用户在手机上拒绝了' })
  assert.equal(n.asked.length, 4, 'another tool still asks')
  // A new process on the same session file (pi resumed elsewhere) reads the grant from the session.
  const kept = a.sm.entries.filter((e) => e.customType === 'persona-allow')
  assert.deepEqual(kept.map((e) => e.data), [{ tool: 'bash' }])
  const b = piSession('s-al2', '/w/app', a.sm.entries)
  host.register(b.pi)
  await b.fire('session_start')
  assert.equal(await b.fire('tool_call', { toolName: 'bash', toolCallId: 'c6', input: { command: 'ls' } }), undefined)
  assert.equal(n.asked.length, 4, 'the resumed session does not ask either')
})

test('ask_user: the phone answers when it drives the session; otherwise pi\'s own dialog', async (t) => {
  const { Type } = await import('./typebox-stub.js')
  const { n, host } = await setup(t, { answerFirst: 'phone' })
  const a = piSession('s-q')
  host.register(a.pi, { Type })
  const tool = a.pi.tools.find((x) => x.name === 'ask_user')
  assert.ok(tool, 'ask_user registered')
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions'))
  n.resolve = (ix) => ({ answers: { [ix.questions[0].question]: 'sqlite' } })
  const res = await tool.execute('q1', { question: 'Which database?', options: ['postgres', 'sqlite'] }, undefined, undefined, a.ctx)
  assert.equal(res.content[0].text, 'The user answered: sqlite')
  assert.deepEqual(n.asked.at(-1).questions[0].options.map((o) => o.label), ['postgres', 'sqlite'])
  n.resolve = () => ({ dismiss: true })
  a.ui.answer = 'postgres'
  const local = await tool.execute('q2', { question: 'Which database?', options: ['postgres', 'sqlite'] }, undefined, undefined, a.ctx)
  assert.equal(local.content[0].text, 'The user answered: postgres')
})

test('inside pi-web\'s server the extension lists only what runs there, as runtime pi-web', async (t) => {
  const { n, host } = await setup(t, { web: true, disk: [{ id: 's-disk', path: '/p', cwd: '/w', modified: new Date(), messageCount: 1 }] })
  const live = piSession('s-live')
  host.register(live.pi)
  await live.fire('session_start')
  await n.ready
  assert.equal(n.hello.runtime, 'pi-web')
  assert.equal(n.hello.launcher, undefined, 'the companion starts sessions')
  await until('listed', () => n.last('runtime.sessions'))
  assert.deepEqual(n.last('runtime.sessions').sessions.map((s) => s.session_id), ['s-live'])
})

test('tells pi-web\'s server from a terminal pi by its process', () => {
  assert.deepEqual(piWebOf(['node', '/x/@agegr/pi-web/node_modules/next/dist/bin/next', 'start', '-p', '30199', '-H', '127.0.0.1'], {}), { port: 30199, host: '127.0.0.1', password: undefined })
  assert.equal(piWebOf(['node', '/opt/homebrew/bin/pi'], {}), undefined)
  assert.equal(piWebOf(['node', 'pi'], { PERSONA_PI_HOST: 'web', PORT: '4000', PI_WEB_HOSTNAME: '0.0.0.0' }).host, '127.0.0.1')
  assert.equal(piWebOf(['node', 'pi'], { PERSONA_PI_HOST: 'web', PREVIOUS_PI_HOST: 'cli' }).host, '127.0.0.1')
  assert.equal(piWebOf(['node', 'pi'], { PERSONA_PI_HOST: '', PREVIOUS_PI_HOST: 'web' }), undefined)
  const webArgv = ['node', '/x/@agegr/pi-web/node_modules/next/dist/bin/next', 'start']
  assert.equal(piWebOf(webArgv, { PERSONA_PI_HOST: 'cli', PREVIOUS_PI_HOST: 'web' }), undefined)
  assert.equal(piWebOf(webArgv, { PERSONA_PI_HOST: '', PREVIOUS_PI_HOST: 'cli' }).host, '127.0.0.1')
})

test('the companion stays off a node that does not let the running runtime answer for a session', async (t) => {
  const n = await startFakeNode(t, { features: [] })
  const host = createPiHost({
    SessionManager: { listAll: async () => [{ id: 's-disk', path: '/p', cwd: '/w', modified: new Date(), messageCount: 1 }], open: () => ({}) },
    web: { port: 30141, host: '127.0.0.1' }, fetch: async () => new Response('{}'),
  }, { socketPath: n.path, retryMs: 20, pushDelayMs: 5, listDisk: true })
  t.after(() => host.stop())
  host.start()
  await n.ready
  await new Promise((r) => setTimeout(r, 300))
  assert.equal(n.last('runtime.sessions'), undefined, 'nothing listed')
  assert.equal(host.runtime.connected, false, 'not connected to it')
})

test('a pi in a herdr pane lists the pane and records that it runs in herdr (once)', async (t) => {
  const { n, host } = await setup(t, { paneId: 'w1:p3' })
  const a = piSession('s-h', '/w/app', [msg('u1', null, 'user', 'hi')], 'tui')
  host.register(a.pi)
  await a.fire('session_start')
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 1)
  assert.equal(n.last('runtime.sessions').sessions[0].pane_id, 'w1:p3')
  const stamps = () => a.sm.entries.filter((e) => e.type === 'custom' && e.customType === 'persona-host')
  assert.deepEqual(stamps().map((e) => e.data.host), ['herdr'])
  await a.fire('session_start') // resumed in the same place: nothing new
  assert.equal(stamps().length, 1)
  const rpc = piSession('s-r', '/w/app', [], 'rpc') // a program's pi (--mode rpc): no pane, not a place
  host.register(rpc.pi)
  await rpc.fire('session_start')
  await until('both', () => n.last('runtime.sessions')?.sessions.length === 2)
  assert.equal(n.last('runtime.sessions').sessions.find((s) => s.session_id === 's-r').pane_id, undefined)
  assert.equal(rpc.sm.entries.length, 0)
})

test('the companion gives sessions that ran in herdr a resume command, and never wakes them in pi-web', async (t) => {
  const { mkdtempSync, writeFileSync, rmSync } = await import('node:fs')
  const { join } = await import('node:path')
  const dir = mkdtempSync('/tmp/jpi-unit-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  const line = (o) => JSON.stringify(o) + '\n'
  const herdrFile = join(dir, 'a_s-herdr.jsonl')
  writeFileSync(herdrFile, line({ type: 'session', id: 's-herdr' }) + line({ type: 'custom', id: 'c1', parentId: null, customType: 'persona-host', data: { host: 'herdr' } }))
  const webFile = join(dir, 'b_s-web.jsonl')
  writeFileSync(webFile, line({ type: 'session', id: 's-web' }) + line({ type: 'custom', id: 'c1', parentId: null, customType: 'persona-host', data: { host: 'herdr' } }) +
    line({ type: 'custom', id: 'c2', parentId: 'c1', customType: 'persona-host', data: { host: 'pi-web' } })) // last ran in pi-web
  const disk = [
    { id: 's-herdr', path: herdrFile, cwd: '/w', modified: new Date(), messageCount: 2 },
    { id: 's-web', path: webFile, cwd: '/w', modified: new Date(), messageCount: 2 },
  ]
  const { n, host, posts } = await setup(t, { web: true, disk, listDisk: true })
  host.start()
  await n.ready
  await until('listed', () => n.last('runtime.sessions')?.sessions.length === 2)
  const rows = Object.fromEntries(n.last('runtime.sessions').sessions.map((s) => [s.session_id, s]))
  assert.deepEqual(rows['s-herdr'].resume_argv, ['pi', '--session', herdrFile])
  assert.equal(rows['s-web'].resume_argv, undefined, 'last ran in pi-web: woken there')
  // A node that did not resume it (an older one) reaches the companion: refused, not opened in pi-web.
  await assert.rejects(n.peer.call('session.send', { session_id: 's-herdr', text: 'x', client_msg_id: 'h1' }), /herdr/)
  assert.equal(posts.length, 0)
  await n.peer.call('session.send', { session_id: 's-web', text: 'y', client_msg_id: 'h2' })
  assert.equal(posts.length, 1)
})

// ---- updates from the node (docs/integrations/pi.md "Auto-update") ----

const PKG = wirePackageName('persona-pi-runtime')
/** The package a node carries: what runtime.package answers. */
function offerOf(text) {
  const files = [{ path: 'pi/index.js', data: Buffer.from(text) }, { path: 'runtime-sdk/index.js', data: Buffer.from('export {}') }]
  return { name: PKG, hash: packageHash(files), version: 'node-2', files: files.map((f) => ({ path: f.path, data: f.data.toString('base64') })) }
}
/** A session whose pi lets extensions register commands (pi 1.x does). */
function withCommands(s) {
  s.pi.commands = new Map()
  s.pi.registerCommand = (name, def) => s.pi.commands.set(name, def)
  return s
}
function store(t) {
  const dir = mkdtempSync('/tmp/jrt-pistore-')
  t.after(() => rmSync(dir, { recursive: true, force: true }))
  return dir
}
const reloads = (s) => s.pi.sent.filter((m) => m.content === '/persona-reload')

test('takes the node\'s version: installs it as current, waits until the process is idle, then every session reloads itself', async (t) => {
  const dir = store(t)
  const { n, host } = await setup(t, { update: { name: PKG, hash: 'the-running-version', store: dir } })
  const v2 = offerOf('v2')
  n.offer = v2
  const a = withCommands(piSession('s-a'))
  const b = withCommands(piSession('s-b'))
  host.register(a.pi)
  host.register(b.pi)
  await a.fire('session_start')
  await b.fire('session_start')
  await a.fire('agent_start') // a turn runs: not now
  await n.ready
  assert.deepEqual(n.hello.package, { name: PKG, hash: 'the-running-version' })
  await until('the package installed', () => { try { return readlinkSync(join(dir, 'current')) === v2.hash } catch { return false } })
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(reloads(a).length + reloads(b).length, 0, 'not while a turn runs')
  assert.equal(host.retired, false)
  await a.fire('agent_settled')
  await until('both sessions told to reload', () => reloads(a).length === 1 && reloads(b).length === 1)
  assert.deepEqual(reloads(a)[0].opts, { expandPromptTemplates: true }, 'as a command, never as a message to the model')
  assert.equal(host.retired, true)
  await until('this version off the node', () => !host.runtime.connected)
  let reloaded = 0
  await a.pi.commands.get('persona-reload').handler('', { reload: async () => { reloaded++ } })
  assert.equal(reloaded, 1, 'the command reloads pi (its new code loads)')
  assert.equal(n.packageCalls.length, 1)
})

test('a session that cannot run the reload command: installed, nothing sent to the model', async (t) => {
  const dir = store(t)
  const { n, host } = await setup(t, { update: { name: PKG, hash: 'the-running-version', store: dir } })
  n.offer = offerOf('v2')
  const a = withCommands(piSession('s-a'))
  const old = piSession('s-old') // a pi without registerCommand
  host.register(a.pi)
  host.register(old.pi)
  await a.fire('session_start')
  await old.fire('session_start')
  await until('installed', () => { try { return readlinkSync(join(dir, 'current')) === n.offer.hash } catch { return false } })
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(a.pi.sent.length + old.pi.sent.length, 0)
  assert.equal(host.retired, false)
  assert.equal(host.runtime.connected, true)
})

test('the companion installs the node\'s version and exits for its supervisor', async (t) => {
  const dir = store(t)
  const exits = []
  const n = await startFakeNode(t)
  n.offer = offerOf('v2')
  // The companion has no session of its own: it connects as soon as it starts.
  const host2 = createPiHost({ SessionManager: { listAll: async () => [], open: () => sessionManager('x', '/') }, web: { port: 1, host: '127.0.0.1' } },
    { socketPath: n.path, retryMs: 20, listDisk: true, update: { name: PKG, hash: 'the-running-version', store: dir, exit: (h) => exits.push(h) } })
  t.after(() => host2.stop())
  host2.start()
  await until('exit called', () => exits.length === 1)
  assert.equal(exits[0], n.offer.hash)
  assert.equal(readlinkSync(join(dir, 'current')), n.offer.hash)
})

test('when the reload brings the same version back, it connects again and does not take that version again', async (t) => {
  const dir = store(t)
  const { n, host } = await setup(t, { update: { name: PKG, hash: 'the-running-version', store: dir } })
  n.offer = offerOf('v2')
  const a = withCommands(piSession('s-a'))
  host.register(a.pi)
  await a.fire('session_start')
  await until('the session told to reload', () => reloads(a).length === 1)
  const hellos = n.hellos
  host.revive() // index.js does this when the reloaded module is this same version
  await until('connected again', () => n.hellos > hellos && host.runtime.connected)
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(n.packageCalls.length, 1, 'the same version is not fetched again')
  assert.equal(reloads(a).length, 1)
  assert.equal(host.retired, false)
})

test('a package whose files do not match its hash is not installed', async (t) => {
  const dir = store(t)
  const { n, host } = await setup(t, { update: { name: PKG, hash: 'the-running-version', store: dir } })
  const v2 = offerOf('v2')
  n.offer = { ...v2, files: v2.files.map((f) => (f.path === 'pi/index.js' ? { ...f, data: Buffer.from('evil').toString('base64') } : f)) }
  const a = withCommands(piSession('s-a'))
  host.register(a.pi)
  await a.fire('session_start')
  await until('fetched', () => n.packageCalls.length === 1)
  await new Promise((r) => setTimeout(r, 50))
  assert.throws(() => readlinkSync(join(dir, 'current')))
  assert.equal(a.pi.sent.length, 0)
  assert.equal(host.retired, false)
})

test('the herdr pane comes with its named session', () => {
  assert.equal(herdrPaneOf({}), undefined)
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1' }), 'w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SESSION: 'default' }), 'w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SESSION: 'build' }), '@build:w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SOCKET_PATH: '/h/.config/herdr/sessions/build/herdr.sock' }), '@build:w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SOCKET_PATH: '/h/.config/herdr/herdr.sock' }), 'w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SOCKET_PATH: '/h/.config/herdr/sessions/a b/herdr.sock' }), 'w4:p1')
  assert.equal(herdrPaneOf({ HERDR_PANE_ID: 'w4:p1', HERDR_SOCKET_PATH: '/h/.config/herdr/sessions/../herdr.sock' }), 'w4:p1')
})
