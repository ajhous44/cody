const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const html = readFileSync(require('node:path').resolve(__dirname, '../../internal/server/index.html'), 'utf8');
function section(from, to) {
  const start = html.indexOf(from), end = html.indexOf(to, start);
  assert.ok(start >= 0 && end > start, 'UI source section exists');
  return html.slice(start, end);
}

test('switching to an empty session clears the previous transcript, steady state preserves it', () => {
  const rendered = [], nodes = {};
  const context = vm.createContext({
    activeSpace: '/project', renderedSession: 'old', activeThread: 'old',
    transcriptGeneration: 0, threadCache: {'/project': [{id:'old'}, {id:'new'}]},
    pendingThreadLoad: '', runningByRoot: {}, pendingById: {}, shownApprovals: new Set(),
    resyncNeeded: new Set(), document: {activeElement: null},
    $: id => nodes[id] || (nodes[id] = {replaceChildren() {}}),
    setBusy() {}, pendingFor: () => [], renderPendingForFocus() {},
    loadThreads: async () => {}, renderTree() {}, activeTab: 'changes', loadGit() {},
    renderHistory: items => rendered.push(Array.from(items)),
  });
  vm.runInContext(section('function applyState(', 'function handle(ev)'), context);
  context.applyState({session:'new',workspace:'/project'});
  assert.deepEqual(rendered, [[]]);
  context.applyState({session:'new',workspace:'/project'});
  assert.equal(rendered.length, 1);
  context.applyState({session:'old',workspace:'/project',history:['saved reply']});
  assert.deepEqual(rendered, [[], ['saved reply']]);
});

test('a delayed approval response preserves the newer streamed status', async () => {
  for (const status of ['working…', 'ready', 'Loading model… other']) {
    const nodes = [], statusText = { textContent: 'waiting for approval' };
    let completeApproval;
    const context = vm.createContext({
      busy: status !== 'ready', statusText, activeSpace: '/project',
      shownApprovals: new Set(), pendingById: {},
      document: { createElement: () => {
        const node = { dataset: {}, append() {}, setAttribute() {}, contains() { return false; }, remove() {}, addEventListener(name, fn) { this[name] = fn; } };
        nodes.push(node); return node;
      } },
      $: () => ({ appendChild() {} }), renderTree() {},
      api: () => new Promise(resolve => { completeApproval = resolve; }),
    });
    vm.runInContext(section('function showApprovalCard(', 'function isFocused('), context);
    context.showApprovalCard({ id: 'approval', tool: 'bash', args: 'npm install' });
    const pending = nodes.find(n => n.className === 'allow').click();
    // SSE can start a tool, finish the turn, or switch projects before HTTP resolves.
    statusText.textContent = status;
    completeApproval();
    await pending;
    assert.equal(statusText.textContent, status);
  }
});

test('cold-model status survives thinking events and clears on response or cancellation', () => {
  const statusText = {}, context = vm.createContext({
    statusText, face: {}, faces: ['face'], spinner: null, spinnerFrame: 0, busy: true,
    setInterval: () => 1, clearInterval: () => {},
    sendBtn: {}, stopBtn: {}, dot: {}, runningByRoot: {},
    isFocused: ev => ev.root === 'active',
    assistantChunk: () => {}, resetTurn: () => {}, notice: () => {},
    resyncTranscript: () => {}, refreshAfterTurn: () => {}, renderTree: () => {},
    pendingById: {}, renderPendingForFocus() {},
  });
  vm.runInContext(section('function setBusy(', 'function userMessage(') +
    section('function handle(ev)', 'async function resyncTranscript('), context);
  const event = ev => context.handle({root: 'active', ...ev});
  event({type: 'thinking'});
  event({type: 'thinking', text: 'Loading model… local'});
  event({type: 'thinking'});
  event({type: 'thinking', root: 'other', text: 'unrelated'});
  assert.equal(statusText.textContent, 'Loading model… local');
  event({type: 'text', text: 'hello'});
  assert.equal(statusText.textContent, 'working…');
  event({type: 'thinking'});
  assert.equal(statusText.textContent, 'thinking…');
  event({type: 'thinking', text: 'Loading model… local'});
  event({type: 'turn_cancelled'});
  assert.equal(statusText.textContent, 'ready');
  assert.equal(context.spinner, null);
});

test('request helpers preserve query strings and authenticate via header', async () => {
  const requests = [];
  const context = {
    token: 'test-token',
    fetch: async (path, options) => {
      requests.push({ path, options });
      return { ok: true, json: async () => ({}) };
    },
  };
  await vm.runInNewContext(section('function api(', '// Concurrent refreshes') + `
    Promise.all([fetchJSON('/api/file?path=nested%20%26%20files%2Fsample.txt'),
      api('/api/session?path=project%20%26%20space', { id: 'new' })]);`, context);
  assert.deepEqual(requests.map(r => r.path), [
    '/api/file?path=nested%20%26%20files%2Fsample.txt',
    '/api/session?path=project%20%26%20space',
  ]);
  for (const { options } of requests) assert.equal(options.headers['X-Cody-Token'], 'test-token');
});

test('turn end flushes text even when animation frames are suspended', () => {
  const frames = [], nodes = [];
  vm.runInNewContext(`
    let assistantEl = null, assistantRaw = '', renderQueued = false, toolEl = null, reasoningEl = null;
    ${section('function resetTurn()', 'function reasoningChunk(')}
    assistantChunk('first');
    assistantChunk(' answer');
    resetTurn();
    assistantChunk('second answer');
    resetTurn();
  `, {
    el: () => { const n = { isConnected: true, innerHTML: '' }; nodes.push(n); return n; },
    mdToHTML: s => s,
    requestAnimationFrame: f => frames.push(f),
    scrollSticky: () => {},
  });
  assert.deepEqual(nodes.map(n => n.innerHTML), ['first answer', 'second answer']);
  frames.forEach(f => f());
  assert.deepEqual(nodes.map(n => n.innerHTML), ['first answer', 'second answer']);
});

test('streamed text stays in chronological order across tool calls', () => {
  const nodes = [], frames = [];
  const node = () => ({ isConnected: true, innerHTML: '', append() {}, setAttribute() {}, addEventListener() {} });
  vm.runInNewContext(`
    let assistantEl = null, assistantRaw = '', renderQueued = false, toolEl = null, reasoningEl = null;
    ${section('function resetTurn()', 'function reasoningChunk(')}
    ${section('function toolCard(', 'function toolFailed(')}
    assistantChunk('Before the tool.');
    toolCard('bash', 'npm install');
    assistantChunk('After the tool.');
    resetTurn();
  `, {
    document: { createElement: node }, transcript: { appendChild: n => nodes.push(n) },
    el: (_, className) => { const n = node(); n.className = className; nodes.push(n); return n; },
    mdToHTML: s => s, requestAnimationFrame: f => frames.push(f), scroll() {}, scrollSticky() {},
  });
  assert.deepEqual(nodes.map(n => n.className), ['assistant', 'tool', 'assistant']);
  assert.deepEqual(nodes.filter(n => n.className === 'assistant').map(n => n.innerHTML), ['Before the tool.', 'After the tool.']);
  frames.forEach(f => f());
  assert.equal(nodes[2].innerHTML, 'After the tool.');
});

test('tool failures include nonzero shell exits without misclassifying file contents', () => {
  const context = vm.createContext({});
  vm.runInContext(section('function toolFailed(', 'function toolResult('), context);
  for (const [name, result, failed] of [
    ['bash', 'exit 1 (3s)\nBuild failed', true],
    ['bash', 'exit -1 (1s)\ninterrupted', true],
    ['bash', 'exit 0 (2s)\nerror: example output', false],
    ['bash', 'error: timed out', true],
    ['write', 'user denied', true],
    ['edit', 'error: no match', true],
    ['read', 'exit 1 (3s)\nsample file contents', false],
    ['write', 'created page.js (42 bytes)', false],
  ]) assert.equal(context.toolFailed(name, result), failed, name + ': ' + result);
  assert.equal(context.cleanToolOutput('exit 1 (3s)\n\u001b[31mError:\u001b[0m bad CSS\n  1 | code\n'), 'exit 1 (3s)\nError: bad CSS\n  1 | code');
});

test('project switch does not reuse or render a previous project request', async () => {
  const pending = [], rendered = [], list = { textContent: '', innerHTML: '' };
  const context = vm.createContext({
    inflight: {}, activeSpace: '/project A', paneGeneration: 0, treeRoot: null,
    $: () => list,
    fetchJSON: url => new Promise(resolve => pending.push({ url, resolve })),
    renderEntries: (_, entries) => rendered.push(...entries.map(e => e.name)),
    encodeURIComponent,
  });
  vm.runInContext(section('function singleflight(', 'function el(') + section('async function loadFiles(', 'async function openFile('), context);
  const a = vm.runInContext('loadFiles()', context);
  context.activeSpace = '/project B'; context.paneGeneration++;
  const b = vm.runInContext('loadFiles()', context);
  assert.equal(pending.length, 2);
  assert.equal(new URL(pending[1].url, 'http://localhost').searchParams.get('root'), '/project B');
  pending[1].resolve({ root: '/project B', entries: [{ name: 'B.txt' }] }); await b;
  pending[0].resolve({ root: '/project A', entries: [{ name: 'A.txt' }] }); await a;
  assert.deepEqual(rendered, ['B.txt']);
  assert.equal(context.treeRoot.root, '/project B');
});

test('old file previews cannot overwrite the latest selection', async () => {
  const pending = [], rendered = [];
  const context = vm.createContext({
    activeSpace: '/project', viewerGeneration: 0, gitFiles: {},
    showViewer: () => {}, renderPlain: value => rendered.push(value),
    fetchJSON: url => new Promise(resolve => pending.push({ url, resolve })),
    encodeURIComponent,
  });
  vm.runInContext(section('async function openFile(', 'function showViewer('), context);
  const a = vm.runInContext('openFile("a.txt")', context);
  const b = vm.runInContext('openFile("b.txt")', context);
  pending[1].resolve({ root: '/project', content: 'B' }); await b;
  pending[0].resolve({ root: '/project', content: 'A' }); await a;
  assert.deepEqual(rendered, ['B']);
});

test('Git state rejects a delayed response after switching projects', async () => {
  const pending = [], nodes = { 'git-list': {}, 'git-branch': {} };
  const context = vm.createContext({
    inflight: {}, activeSpace: 'A', paneGeneration: 0, gitFiles: {},
    $: id => nodes[id], encodeURIComponent,
    fetchJSON: url => new Promise(resolve => pending.push({ url, resolve })),
  });
  vm.runInContext(section('function singleflight(', 'function el(') + section('async function loadGit(', 'async function openDiff('), context);
  const a = vm.runInContext('loadGit()', context);
  context.activeSpace = 'B'; context.paneGeneration++;
  const b = vm.runInContext('loadGit()', context);
  pending[1].resolve({ root: 'B', repo: true, branch: 'B-branch', files: [] }); await b;
  pending[0].resolve({ root: 'A', repo: true, branch: 'A-branch', files: [] }); await a;
  assert.equal(nodes['git-branch'].textContent, 'branch B-branch');
});

test('reconnected transcript refreshes after completion and rejects stale snapshots', async () => {
  const rendered = [], pending = [], key = 'root\0session';
  const context = vm.createContext({
    resyncNeeded: new Set([key]), transcriptGeneration: 1, busy: false,
    isFocused: () => true,
    fetchJSON: () => new Promise(resolve => pending.push(resolve)),
    renderHistory: history => rendered.push(history), notice: () => {}, errorLine: () => {},
  });
  vm.runInContext(section('async function resyncTranscript(', 'function refreshAfterTurn('), context);
  const first = vm.runInContext('resyncTranscript({root:"root",session:"session",type:"turn_end"})', context);
  context.transcriptGeneration++;
  pending[0]({root:'root',session:'session',busy:false,history:['old']});await first;
  assert.equal(rendered.length, 0);
  assert.ok(context.resyncNeeded.has(key));
  const second = vm.runInContext('resyncTranscript({root:"root",session:"session",type:"turn_end"})', context);
  pending[1]({root:'root',session:'session',busy:false,history:['complete answer']});await second;
  assert.deepEqual(rendered, [['complete answer']]);
  assert.equal(context.resyncNeeded.size, 0);
});
