'use strict';

// A link may name a session (#session=<id>), such as another local page's link to a session that waits on you.
// A session that is not loaded yet is selected when it arrives (linkedId).
function linkedSession() {
  const hash = location.hash.slice(1);
  if (!hash.startsWith(SESSION_HASH)) return null;
  try {
    return decodeURIComponent(hash.slice(SESSION_HASH.length));
  } catch {
    return null;
  }
}

function followLink() {
  const id = linkedSession();
  if (!id || id === selectedId) return;
  linkedId = sessions.has(id) ? null : id;
  if (!linkedId) select(id);
}

window.addEventListener('hashchange', followLink);

function setConnection(text, down) {
  const el = $('#connection');
  el.textContent = text;
  el.classList.toggle('down', down);
}

async function loadAll() {
  const list = await api('GET', '/api/sessions');
  const seen = new Set(list.map(s => s.id));
  for (const id of [...sessions.keys()]) if (!seen.has(id)) sessions.delete(id);
  for (const s of list) sessions.set(s.id, s);
  if (!sessions.has(selectedId) && !past.has(selectedId)) {
    const first = listed()[0];
    selectedId = first ? first.id : null;
  }
  loaded = true;
  render();
  followLink();
  loadPast();
}

function connect() {
  stream = new EventSource('/api/stream');
  stream.addEventListener('change', ev => apply(JSON.parse(ev.data)));
  // Changes missed while disconnected are not replayed, so every (re)connect reloads the list.
  stream.onopen = () => {
    // A working connection needs no word; only a broken one is shown.
    setConnection('', false);
    // The terminal's socket died with the server; the reload below mounts a new one.
    if (term && term.ws.readyState >= WebSocket.CLOSING) paneKey = '';
    loadAll().catch(err => setConnection(errorText(err), true));
  };
  stream.onerror = () => setConnection('Reconnecting to claude-inbox…', true);
}

setInterval(() => {
  const now = Date.now();
  document.querySelectorAll('[data-since]').forEach(n => { n.textContent = fmt(now - Number(n.dataset.since)); });
}, SEC);

async function loadPageConfig() {
  pageConfig = await api('GET', '/api/config');
  pageConfig.primaryActions ??= [];
  pageConfig.secondaryActions ??= [];
  $('#cheatsheet').hidden = !pageConfig.cheatSheet;
  followEmbedder(pageConfig.embedOrigin);
  render();
}
