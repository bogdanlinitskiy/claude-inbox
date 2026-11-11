'use strict';

const actionsOf = list => (list === PRIMARY ? pageConfig.primaryActions : pageConfig.secondaryActions);
const actionKey = (s, list, i) => `${s.id}:${list}:${i}`;
const glyph = a => `<span class="glyph">${a.kind === 'run' ? '$' : '↵'}</span>`;

// actionBlocked says why an action cannot act on this session now; empty when it can. The server checks again.
function actionBlocked(s, a) {
  if (a.kind === 'run') {
    if (s.state.kind === 'working') return 'Claude is working in this folder. Run it once the turn ends.';
    return s.dir ? '' : 'The folder of this session is not known.';
  }
  if (!s.inTmux) return 'This session does not run in tmux, so the app cannot type into it.';
  return ['idle', 'done', 'working'].includes(s.state.kind) ? '' : 'Answer the question first: a message would land in it.';
}

function actionBarHtml(s) {
  const list = pageConfig.primaryActions;
  if (!list.length) return '';
  const chips = list.slice(0, MAX_CHIPS).map((a, i) => {
    const key = actionKey(s, PRIMARY, i);
    const why = actionBlocked(s, a);
    const running = runningActions.has(key);
    const label = running ? `${a.label}…` : armedAction === key ? `Click again: ${a.label}` : a.label;
    return `<button type="button" class="chip${a.kind === 'run' ? ' run' : ''}${armedAction === key ? ' armed' : ''}" data-action="${i}" title="${esc(why || a.text)}"${why || running ? ' disabled' : ''}>${glyph(a)}${esc(label)}</button>`;
  }).join('');
  return `<div class="act-bar" id="act-bar"><span class="lbl">Actions</span>${chips}<button type="button" class="chip more" data-act="more-actions" hidden></button></div>`;
}

// fitBar keeps the bar to two rows: the chips that would start a third go behind "+N more", which opens the
// primary list in the menu.
function fitBar() {
  const bar = $('#act-bar');
  if (!bar) return;
  const chips = [...bar.querySelectorAll('[data-action]')];
  const more = $('[data-act="more-actions"]', bar);
  const total = pageConfig.primaryActions.length;
  chips.forEach(c => { c.hidden = false; });
  more.hidden = true;
  // From the bar's own top: the bar moves down as it loses rows.
  const top = el => Math.round(el.getBoundingClientRect().top - bar.getBoundingClientRect().top);
  const tops = [...new Set(chips.map(top))].sort((a, b) => a - b);
  if (tops.length <= 2 && chips.length === total) return;
  const lastRow = tops[1] ?? tops[0];
  let shown = chips.filter(c => top(c) <= lastRow).length;
  chips.forEach((c, i) => { c.hidden = i >= shown; });
  more.hidden = false;
  for (;;) {
    more.textContent = `+${(total - shown).toLocaleString()} more`;
    if (top(more) <= lastRow || shown === 0) return;
    shown -= 1;
    chips[shown].hidden = true;
  }
}

function refreshActions() {
  const s = sessions.get(selectedId);
  if (s) renderReply(s);
  if (menu) renderMenuItems();
}

function arm(key) {
  armedAction = key;
  refreshActions();
  setTimeout(() => { if (armedAction === key) { armedAction = null; refreshActions(); } }, CLOSE_ARM_MS);
}

// activate acts on the session, or first waits for the second click a confirm action needs. An action with an
// input comes here from the menu's field only.
function activate(s, list, i, input = '') {
  const a = actionsOf(list)[i];
  if (!a) return;
  const why = actionBlocked(s, a);
  if (why) {
    toast(`${a.label} is not available`, why);
    return;
  }
  const key = actionKey(s, list, i);
  if (a.confirm && armedAction !== key) {
    arm(key);
    return;
  }
  armedAction = null;
  closeMenu();
  perform(s, list, i, a, input);
}

async function perform(s, list, i, a, input) {
  const key = actionKey(s, list, i);
  if (runningActions.has(key)) return;
  runningActions.add(key);
  refreshActions();
  try {
    const res = await api('POST', `/api/session/${encodeURIComponent(s.id)}/action`, { list, index: i, input });
    if (res.session) apply({ session: res.session, live: false });
    if (res.run) {
      const r = res.run;
      const status = r.timedOut ? 'stopped: took too long' : `exit ${r.exitCode}`;
      const lines = r.output.trimEnd().split('\n').slice(-RUN_TOAST_LINES).join('\n');
      toast(`${a.label} · ${short(s.repo)} · ${status}`, lines || '(no output)', null, RUN_TOAST_MS);
    }
  } catch (err) {
    toast(`${a.label} failed`, errorText(err));
  } finally {
    runningActions.delete(key);
    refreshActions();
  }
}

// openMenu shows a list in the menu under (secondary) or over (primary) its button. With only, it shows that one
// action with its input field: a primary chip that needs an input.
function openMenu(list, anchor, only) {
  const s = sessions.get(selectedId);
  if (!s || !anchor) return;
  // By selector, not element: the header and the bar are redrawn while the menu is open.
  const anchorSel = only !== undefined ? `[data-action="${only}"]` : list === PRIMARY ? '[data-act="more-actions"]' : '[data-act="actions"]';
  menu = { list, sessionId: s.id, query: '', sel: 0, inputIndex: only ?? null, inputValue: '', only, anchorSel };
  const box = $('#act-menu');
  const search = only === undefined
    ? `<div class="search"><span class="glyph">⌕</span><input id="act-search" type="search" placeholder="Search actions…" aria-label="Search actions" autocomplete="off"><span class="cnt" id="act-count"></span></div>`
    : '';
  box.innerHTML = `${list === PRIMARY ? '<div class="head">Primary actions</div>' : ''}${search}<div class="items" id="act-items" role="listbox" aria-label="Actions"></div><div class="foot" id="act-foot"></div>`;
  box.hidden = false;
  placeMenu();
  renderMenuItems();
  if (only === undefined) $('#act-search').focus();
}

// placeMenu puts the menu under its button (secondary) or over it (primary), right edges aligned.
function placeMenu() {
  const anchor = menu && document.querySelector(menu.anchorSel);
  if (!anchor) return;
  const box = $('#act-menu');
  const r = anchor.getBoundingClientRect();
  box.style.right = `${Math.max(8, innerWidth - r.right)}px`;
  box.style.top = menu.list === PRIMARY ? 'auto' : `${r.bottom + 6}px`;
  box.style.bottom = menu.list === PRIMARY ? `${innerHeight - r.top + 6}px` : 'auto';
}

function closeMenu() {
  menu = null;
  $('#act-menu').hidden = true;
}

function menuWords() {
  return menu.query.toLowerCase().split(/\s+/).filter(Boolean);
}

function menuMatches() {
  const all = actionsOf(menu.list).map((a, i) => ({ a, i }));
  if (menu.only !== undefined) return all.filter(m => m.i === menu.only);
  const words = menuWords();
  return all.filter(({ a }) => {
    const text = `${a.label} ${a.text}`.toLowerCase();
    return words.every(w => text.includes(w));
  });
}

const escapeRegExp = w => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

function highlight(text, words) {
  if (!words.length) return esc(text);
  const re = new RegExp(`(${words.map(escapeRegExp).join('|')})`, 'gi');
  return text.split(re).map((part, i) => (i % 2 ? `<mark>${esc(part)}</mark>` : esc(part))).join('');
}

// renderMenuItems redraws the rows only, so the search box keeps its focus and caret while typing.
function renderMenuItems() {
  const s = sessions.get(menu.sessionId);
  if (!s) {
    closeMenu();
    return;
  }
  const all = actionsOf(menu.list);
  const matches = menuMatches();
  menu.sel = Math.min(menu.sel, Math.max(0, matches.length - 1));
  const words = menuWords();
  $('#act-items').innerHTML = matches.slice(0, MAX_MENU_ROWS).map(({ a, i }, n) => {
    const key = actionKey(s, menu.list, i);
    const why = actionBlocked(s, a);
    const side = `${a.confirm ? '<kbd>asks first</kbd>' : ''}<span class="kind ${a.kind}">${a.kind}</span>`;
    const note = why || (armedAction === key ? `Press Enter or click again to ${a.kind === 'run' ? 'run' : 'send'} it.` : '');
    const input = menu.inputIndex === i
      ? `<form class="inp" data-input-form="${i}"><input id="act-input" placeholder="${esc(a.input)}" aria-label="${esc(a.input)}" autocomplete="off"><button class="btn small primary" type="submit">${a.kind === 'run' ? 'Run' : 'Send'}</button></form>`
      : '';
    return `<div class="mi${n === menu.sel ? ' hl' : ''}${why ? ' off' : ''}" data-menu-index="${i}" role="option" aria-selected="${n === menu.sel}">
      <b>${highlight(a.label, words)}</b><span class="sub">${highlight(a.text, words)}</span><span class="side">${side}</span>
      ${note ? `<span class="why">${esc(note)}</span>` : ''}${input}</div>`;
  }).join('') || '<div class="none">Nothing matches.</div>';
  const count = $('#act-count');
  if (count) count.textContent = menu.query ? `${matches.length.toLocaleString()} of ${all.length.toLocaleString()}` : `${all.length.toLocaleString()} actions`;
  const cut = matches.length > MAX_MENU_ROWS ? `The first ${MAX_MENU_ROWS} are shown; type to narrow. ` : '';
  const pick = menu.only === undefined ? '<kbd>↑</kbd><kbd>↓</kbd> pick · ' : '';
  $('#act-foot').innerHTML = `${esc(cut)}${pick}<kbd>Enter</kbd> act · <kbd>Esc</kbd> close`;
  const field = $('#act-input');
  if (field) {
    field.value = menu.inputValue;
    field.focus();
  }
  $('#act-items .mi.hl')?.scrollIntoView({ block: 'nearest' });
}

function pickInMenu(i) {
  const s = sessions.get(menu.sessionId);
  if (!s) return;
  const a = actionsOf(menu.list)[i];
  if (a?.input && menu.inputIndex !== i) {
    menu.inputIndex = i;
    menu.inputValue = '';
    renderMenuItems();
    return;
  }
  if (a?.input && !menu.inputValue.trim()) {
    $('#act-input')?.focus();
    return;
  }
  activate(s, menu.list, i, a?.input ? menu.inputValue : '');
}

const actMenu = $('#act-menu');

actMenu.addEventListener('input', e => {
  if (e.target.id === 'act-search') {
    menu.query = e.target.value;
    menu.sel = 0;
    menu.inputIndex = null;
    renderMenuItems();
  } else if (e.target.id === 'act-input') {
    menu.inputValue = e.target.value;
  }
});

actMenu.addEventListener('keydown', e => {
  if (!menu) return;
  if (e.key === 'Escape') {
    e.preventDefault();
    if (e.target.id === 'act-input' && menu.only === undefined) {
      menu.inputIndex = null;
      renderMenuItems();
      $('#act-search').focus();
    } else {
      closeMenu();
    }
    return;
  }
  if (e.target.id !== 'act-search') return;
  const matches = menuMatches().slice(0, MAX_MENU_ROWS);
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    menu.sel = Math.max(0, Math.min(matches.length - 1, menu.sel + (e.key === 'ArrowDown' ? 1 : -1)));
    renderMenuItems();
  } else if (e.key === 'Enter' && matches[menu.sel]) {
    e.preventDefault();
    pickInMenu(matches[menu.sel].i);
  }
});

actMenu.addEventListener('submit', e => {
  e.preventDefault();
  pickInMenu(Number(e.target.dataset.inputForm));
});

actMenu.addEventListener('click', e => {
  const row = e.target.closest('.mi');
  if (!row || e.target.closest('form') || row.classList.contains('off')) return;
  const i = Number(row.dataset.menuIndex);
  menu.sel = menuMatches().findIndex(m => m.i === i);
  pickInMenu(i);
});

document.addEventListener('mousedown', e => {
  if (menu && !actMenu.contains(e.target) && !e.target.closest('[data-act="actions"], [data-act="more-actions"], [data-action]')) closeMenu();
});

addEventListener('resize', placeMenu);

// Fit the bar when the reply area changes width (window resize, narrow screen showing the detail).
let barWidth = 0;
new ResizeObserver(entries => {
  const width = Math.round(entries[entries.length - 1].contentRect.width);
  if (width === barWidth) return;
  barWidth = width;
  fitBar();
}).observe($('#reply'));
