'use strict';

// tokens.css is written with light-dark(), so the operating system decides until you pick a side. A choice
// sets data-theme, which wins in both directions, and is remembered. An embedding page still overrides it.
const THEME_CYCLE = { system: 'dark', dark: 'light', light: 'system' };
const THEME_LABEL = { system: 'Auto', dark: 'Dark', light: 'Light' };
// In a frame the host owns the theme. A choice stored for the standalone page says nothing about the page
// we are embedded in, and the toggle is hidden there, so it could not be corrected from inside the frame.
const inFrame = window.self !== window.top;
let themeChoice = 'system';
if (!inFrame) {
  try { themeChoice = localStorage.getItem('inbox-theme') || 'system'; } catch { /* private window */ }
}
if (!(themeChoice in THEME_CYCLE)) themeChoice = 'system';

// The host answers home:theme? a moment after load, so painting first would show the wrong theme and then
// flip. Hold the page until it answers; after THEME_WAIT_MS, show it at the system theme rather than
// leaving a blank frame when no host answers at all.
const THEME_WAIT_MS = 250;
function revealTheme() {
  document.documentElement.classList.remove('theme-wait');
}
if (inFrame) {
  document.documentElement.classList.add('theme-wait');
  setTimeout(revealTheme, THEME_WAIT_MS);
}

function applyTheme() {
  if (themeChoice === 'system') delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = themeChoice;
  const btn = $('#theme');
  btn.textContent = THEME_LABEL[themeChoice];
  btn.title = themeChoice === 'system' ? 'Theme: follows your system' : `Theme: always ${themeChoice}`;
}

$('#theme').addEventListener('click', () => {
  themeChoice = THEME_CYCLE[themeChoice];
  try { localStorage.setItem('inbox-theme', themeChoice); } catch { /* private window */ }
  applyTheme();
});
applyTheme();

// Framed by the embed origin, the page follows that page's theme and lets its background show through. Messages
// from any other origin are ignored. The message names (home:theme?, home:theme) are the embedder's protocol.
function followEmbedder(origin) {
  if (!origin || !inFrame) return;
  document.documentElement.classList.add('framed');
  addEventListener('message', e => {
    if (e.origin !== origin || e.data?.type !== 'home:theme') return;
    document.documentElement.dataset.theme = e.data.theme === 'light' ? 'light' : 'dark';
    revealTheme();
  });
  window.parent.postMessage({ type: 'home:theme?' }, origin);
}
