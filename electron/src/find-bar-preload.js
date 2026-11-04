// Preload for the find bar's own view (find-bar.js) — and nothing else. The
// page it serves is the web bundle's find-bar.html; this is the whole of what
// that page may ask of main: search the window's page, step through it, close.
//
// Sandboxed, so it may require only electron: the channel names repeat
// find-bar.js's FIND_BAR_CHANNELS, and test/find-bar.test.js holds them equal.
const { contextBridge, ipcRenderer } = require('electron');

function subscribe(channel, callback) {
  const listener = (_event, payload) => callback(payload);
  ipcRenderer.on(channel, listener);
  return () => ipcRenderer.removeListener(channel, listener);
}

contextBridge.exposeInMainWorld('findBarAPI', {
  ready: () => ipcRenderer.send('find-bar:ready'),
  find: (request) => ipcRenderer.send('find-bar:find', request),
  close: () => ipcRenderer.send('find-bar:close'),
  onOpened: (callback) => subscribe('find-bar:opened', callback),
  onResult: (callback) => subscribe('find-bar:result', callback),
});
