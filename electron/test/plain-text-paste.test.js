const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function preload(cancel) {
  const listeners = new Map();
  const events = [];
  const inserted = [];
  const context = {
    require: () => ({
      contextBridge: { exposeInMainWorld() {} },
      ipcRenderer: { on: (name, callback) => listeners.set(name, callback), once() {} },
    }),
    process: { platform: 'linux', argv: [], versions: {} },
    window: { addEventListener() {} },
    document: {
      activeElement: { dispatchEvent(event) { events.push(event); return !cancel; } },
      execCommand: (...args) => inserted.push(args),
    },
    CustomEvent: class { constructor(type, init) { this.type = type; Object.assign(this, init); } },
    console: { log() {}, info() {}, warn() {}, error() {} },
    setTimeout() {},
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../src/preload.js'), 'utf8'), context);
  return { receive: text => listeners.get('paste-plain-text')({}, text), events, inserted };
}

test('plain-text paste is a single cancellable operation, with no stale bypass', () => {
  const p = preload(true);
  const text = '  café 😀\r\n\t```  ';
  p.receive(text);
  assert.equal(p.events.length, 1);
  assert.equal(p.events[0].type, 'reliant:paste-plain-text');
  assert.equal(p.events[0].detail.text, text);
  assert.equal(p.events[0].bubbles, true);
  assert.equal(p.events[0].cancelable, true);
  assert.equal(p.inserted.length, 0);
  p.receive(null);
  p.receive('');
  assert.equal(p.events.length, 1);
});

test('unhandled plain-text paste uses native editing, once per invocation', () => {
  const p = preload(false);
  p.receive('one');
  p.receive('two');
  assert.deepEqual(p.inserted, [['insertText', false, 'one'], ['insertText', false, 'two']]);
});

test('Edit menu reserves plain-text chord while ordinary paste remains native', () => {
  const main = fs.readFileSync(path.join(__dirname, '../src/main.js'), 'utf8');
  assert.match(main, /label: "Paste as Plain Text",\s+accelerator: "CmdOrCtrl\+Shift\+V"/);
  assert.match(main, /focused\.webContents\.send\("paste-plain-text", require\("electron"\)\.clipboard\.readText\(\)\)/);
  assert.match(main, /mainWindow\.webContents\.send\("paste-plain-text", require\("electron"\)\.clipboard\.readText\(\)\)/);
  assert.match(main, /\{ role: "paste" \}/);
});
