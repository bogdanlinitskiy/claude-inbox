'use strict';

const cssVar = name => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

function mountTerminal(s) {
  unmountTerminal();
  const pane = $('#pane');
  pane.innerHTML = '<div class="term-host" id="term-host"></div>';
  const host = $('#term-host');
  const xterm = new Terminal({
    fontFamily: cssVar('--font-mono'),
    fontSize: 13,
    cursorBlink: true,
    scrollback: 0,
    theme: { background: cssVar('--term-bg'), foreground: cssVar('--term-fg'), cursor: cssVar('--term-fg') },
  });
  const fit = new FitAddon.FitAddon();
  xterm.loadAddon(fit);
  xterm.open(host);
  safeFit(fit);

  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  // The fitted size goes with the URL so tmux draws at the right size from the first frame (terminal.go).
  const ws = new WebSocket(`${scheme}://${location.host}/api/session/${encodeURIComponent(s.id)}/terminal?cols=${xterm.cols}&rows=${xterm.rows}`);
  ws.binaryType = 'arraybuffer';
  const encoder = new TextEncoder();
  const sendSize = () => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols: xterm.cols, rows: xterm.rows }));
  };
  ws.onmessage = ev => xterm.write(new Uint8Array(ev.data));
  ws.onclose = () => { if (term?.ws === ws) xterm.write('\r\n\x1b[2m[terminal disconnected — switch tabs to reconnect]\x1b[0m\r\n'); };
  xterm.onData(d => { if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(d)); });
  // xterm.js pastes only text. A paste carrying an image goes to Claude Code as ^V instead, which makes it read
  // the image from the desktop clipboard itself. Captured on the host, so it runs before xterm's own handler.
  host.addEventListener('paste', ev => {
    if (![...(ev.clipboardData?.items ?? [])].some(i => i.kind === 'file' && i.type.startsWith('image/'))) return;
    ev.preventDefault();
    ev.stopPropagation();
    if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode('\x16'));
  }, true);
  // Mouse reports (wheel scrolling with tmux's mouse mode) come as binary strings, one byte per character.
  // Ctrl+V is left to the browser's paste (handled above), which can tell text from an image; xterm.js would send
  // ^V either way. By code, not key, so it also works on other keyboard layouts.
  xterm.attachCustomKeyEventHandler(ev => !(ev.ctrlKey && !ev.altKey && !ev.metaKey && ev.code === 'KeyV'));
  xterm.onBinary(d => { if (ws.readyState === WebSocket.OPEN) ws.send(Uint8Array.from(d, c => c.charCodeAt(0))); });
  // Claude Code tracks the mouse and copies its own selection through tmux, which hands the text on as
  // OSC 52 ("<targets>;<base64>"); xterm.js drops that unless it is wired to the clipboard here.
  xterm.parser.registerOscHandler(52, data => {
    const b64 = data.slice(data.indexOf(';') + 1);
    if (b64 === '?') return true;
    try {
      const text = new TextDecoder().decode(Uint8Array.from(atob(b64), c => c.charCodeAt(0)));
      navigator.clipboard?.writeText(text).catch(() => {});
    } catch { /* not base64 */ }
    return true;
  });
  xterm.onResize(sendSize);
  // Fit only when the host really changed size, once per frame: fitting itself can nudge the layout, and
  // reacting to that would loop.
  let lastSize = '';
  let frame = 0;
  const observer = new ResizeObserver(entries => {
    const box = entries[entries.length - 1].contentRect;
    const size = `${Math.round(box.width)}x${Math.round(box.height)}`;
    if (size === lastSize) return;
    lastSize = size;
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => safeFit(fit));
  });
  observer.observe(host);
  term = { id: s.id, xterm, ws, observer };
}

function safeFit(fit) {
  try { fit.fit(); } catch { /* the host has no size yet (hidden on a narrow screen); the observer fits it later */ }
}

function unmountTerminal() {
  if (!term) return;
  const t = term;
  term = null;
  t.observer.disconnect();
  t.ws.close();
  t.xterm.dispose();
}
