(() => {
  'use strict';

  const $ = (sel, root = document) => root.querySelector(sel);
  const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const short = repo => pageConfig.stripPrefix && repo.startsWith(pageConfig.stripPrefix) ? repo.slice(pageConfig.stripPrefix.length) : repo;
  const narrow = matchMedia('(max-width: 880px)');
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const SEC = 1000;
  const CLOSE_ARM_MS = 4000;
  const TOAST_MS = 6500;
  // A run action's output stays up long enough to read.
  const RUN_TOAST_MS = 15000;
  const RUN_TOAST_LINES = 8;
  // The bar renders this many primary actions at most before fitting them into two rows; the menu lists this many
  // matches at most. Both keep a config with thousands of actions cheap to draw.
  const MAX_CHIPS = 60;
  const MAX_MENU_ROWS = 200;
  const PRIMARY = 'primary';
  const SECONDARY = 'secondary';
  // The answer found nothing to answer on the screen (internal/server, codeNothingOnScreen).
  const NOTHING_ON_SCREEN = 'NOTHING_ON_SCREEN';
  const MAX_TOASTS = 3;
  const TITLE = 'Session Inbox';

  const KIND = {
    trust: { label: 'Trust folder', cls: 's-wait' },
    question: { label: 'Question', cls: 's-wait' },
    permission: { label: 'Permission', cls: 's-wait' },
    unknown: { label: 'Check terminal', cls: 's-fail' },
    working: { label: 'Working', cls: 's-work' },
    idle: { label: 'Idle', cls: 's-idle' },
    done: { label: 'Done', cls: 's-done' },
    ended: { label: 'Ended', cls: 's-ended' },
  };
  const SINCE_LABEL = { working: 'for', done: 'finished', idle: 'idle for' };
  const EMPTY = {
    all: { head: 'No sessions yet', body: 'Start one with New session, or run claude-tmux in a terminal.' },
    needs: { head: 'Nothing is waiting on you', body: 'A session lands here the moment it asks a question or wants a permission.' },
    working: { head: 'Nothing is running', body: 'A session sits here while Claude is working in it.' },
    done: { head: 'Nothing has finished', body: 'A session moves here when it stops, and when it goes idle.' },
    history: { head: 'No past conversations', body: 'A conversation is kept here after its session ends.' },
  };
  // The diff bar is five blocks wide, like a review tool's.
  const DIFF_BLOCKS = 5;
  const TAB_LABEL = { terminal: 'Terminal', diff: 'Diff', preview: 'Preview' };

  const sessions = new Map();
  // History: past conversations from Claude Code's transcripts that no session in the list holds, latest first.
  const past = new Map();
  let pastLoaded = false;
  let pastQuery = '';
  const tabs = new Map();
  const drafts = new Map();
  let selectedId = null;
  const SESSION_HASH = 'session=';
  let linkedId = null;
  let filter = 'all';
  // Set by the ticket chip in a row: the rail narrows to one ticket, still split into state blocks, so the
  // readiness of a ticket with several sessions is read off one view instead of counted off the rail.
  let ticketFilter = null;
  // How many live sessions each ticket has, rebuilt per draw. A row shows its ticket as a chip only when the
  // ticket has more than one, because a chip that narrows the rail to a single row buys nothing.
  let tixCount = new Map();
  let alertsOn = readSetting('alerts', true);
  // Sound is off until asked for: a page that beeps unprompted is worse than one that stays quiet. Turning it
  // on is the click that lets the browser open an AudioContext at all.
  let soundOn = readSetting('sound', false);
  let audio = null;
  // The text typed in the live filter; it narrows the live list the way the ticket chip narrows it.
  let query = '';
  let term = null;
  let paneKey = '';
  let replyKey = '';
  const sending = new Set();
  // Sessions whose answer found nothing to answer on the screen, by id: the seq of the state it was for. The
  // buttons of that state stay locked; a new state brings them back.
  const nothingOnScreen = new Map();
  let closeArmed = null;
  let renamingId = null;
  let repoList = null;
  // Files the Diff tab shows expanded, by path, so a Refresh keeps them open.
  const openDiffFiles = new Set();
  // GET /api/config: which optional buttons have something behind them (hidden until it says so), the prefix left
  // out of repository names, and the origin that may frame the page.
  let pageConfig = { ide: false, cheatSheet: false, stripPrefix: '', embedOrigin: '', primaryActions: [], secondaryActions: [] };
  // The action waiting for its second click, and the actions on their way, by actionKey.
  let armedAction = null;
  const runningActions = new Set();
  // The open Actions menu: which list, the search, the selected row, and the action whose input field is open.
  let menu = null;
  let stream = null;

  /* ---------- Helpers ---------- */

  function readSetting(key, fallback) {
    try {
      const v = localStorage.getItem('inbox.' + key);
      return v === null ? fallback : JSON.parse(v);
    } catch {
      return fallback;
    }
  }

  function writeSetting(key, value) {
    try { localStorage.setItem('inbox.' + key, JSON.stringify(value)); } catch { /* storage blocked: the default applies next time */ }
  }

  const parseTime = s => (s ? Date.parse(s.replace(' ', 'T') + 'Z') : NaN);

  function fmt(ms) {
    if (!Number.isFinite(ms)) return '';
    const s = Math.max(0, Math.floor(ms / SEC));
    if (s < 60) return `${s}s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ${m % 60}m`;
    return `${Math.floor(h / 24)}d ${h % 24}h`;
  }

  const since = s => `<span data-since="${parseTime(s)}">${fmt(Date.now() - parseTime(s))}</span>`;

  async function api(method, path, body) {
    const res = await fetch(path, {
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    });
    if (res.status === 204) return null;
    const data = await res.json().catch(() => null);
    if (!res.ok) {
      const err = new Error(data?.message || `${res.status} ${res.statusText}`);
      err.status = res.status;
      err.fields = data?.errors || {};
      err.code = data?.code;
      if (res.status === 401) showBanner(err.message);
      throw err;
    }
    return data;
  }

  function errorText(err) {
    const field = Object.values(err.fields || {})[0];
    return field || err.message;
  }

  function showBanner(text) {
    const b = $('#banner');
    b.textContent = text;
    b.hidden = false;
  }

  function toast(title, body, id, ms = TOAST_MS) {
    const box = $('#toasts');
    const t = document.createElement(id ? 'button' : 'div');
    t.className = 'toast';
    if (id) t.type = 'button';
    t.innerHTML = `<b>${esc(title)}</b>${body ? `<span>${esc(body)}</span>` : ''}`;
    if (id) t.addEventListener('click', () => { if (sessions.has(id)) select(id); t.remove(); });
    box.appendChild(t);
    setTimeout(() => t.remove(), ms);
    while (box.children.length > MAX_TOASTS) box.firstElementChild.remove();
  }

  /* ---------- Session model ---------- */

  const needs = s => s.state.needsYou;
  const group = s => (needs(s) ? 'needs' : s.state.kind === 'working' ? 'working' : 'done');
  // The rail is these three blocks, in this order. Blocked sessions are the reason the app exists, so they
  // are always the top of the list rather than wherever their ticket happens to sort.
  const BLOCK = { needs: 'Needs you', working: 'Working', done: 'Done' };
  const BLOCK_ORDER = ['needs', 'working', 'done'];

  function summary(s) {
    const st = s.state;
    switch (st.kind) {
      case 'question': return st.questions?.[0]?.question || 'Claude asks a question';
      case 'permission': return st.permission ? `${st.permission.tool}: ${st.permission.detail}` : 'Asks for permission';
      case 'trust': return 'Asks whether to trust this folder';
      case 'unknown': return 'Waiting on something the app does not recognize. Check the terminal.';
      case 'working': return st.activity || 'Working';
      case 'done': return st.lastMessage || 'Finished';
      case 'idle': return 'Waiting for a prompt';
      default: return 'Ended';
    }
  }

  const shown = s => (filter === 'all' || group(s) === filter)
    && (!ticketFilter || (s.ticket || '') === ticketFilter)
    && matches(s);

  // matches tests the typed text against the fields a row puts on screen, so what you read is what you search.
  function matches(s) {
    if (!query) return true;
    return [s.title, s.repo, s.branch, s.ticket, summary(s)]
      .some(v => v && v.toLowerCase().includes(query));
  }

  // cluster keys a session by ticket, so rows on one ticket sort together. A session without a ticket is its
  // own cluster and so sorts on its own recency; the prefix keeps those keys out of the ticket space.
  const cluster = s => s.ticket || `\u0000${s.id}`;

  // ordered splits the shown sessions into the state blocks, in urgency order. Inside a block, rows that share
  // a ticket sit next to each other, the cluster with the most recent activity first, so several sessions on
  // one ticket read as a set without a card drawn around them. An ended session leaves the list; its
  // conversation is in History.
  function ordered() {
    const byBlock = new Map(BLOCK_ORDER.map(k => [k, []]));
    for (const s of sessions.values()) if (shown(s)) byBlock.get(group(s)).push(s);
    const blocks = [];
    for (const key of BLOCK_ORDER) {
      const list = byBlock.get(key);
      if (!list.length) continue;
      const latest = new Map();
      for (const s of list) {
        const at = parseTime(s.state.changedAt);
        const k = cluster(s);
        if (!(latest.get(k) >= at)) latest.set(k, at);
      }
      list.sort((a, b) => latest.get(cluster(b)) - latest.get(cluster(a))
        || (cluster(a) < cluster(b) ? -1 : cluster(a) > cluster(b) ? 1 : 0)
        || parseTime(b.state.changedAt) - parseTime(a.state.changedAt));
      blocks.push({ key, label: BLOCK[key], list });
    }
    return blocks;
  }

  // listed is every row on screen, top to bottom.
  function listed() {
    if (filter === 'history') return pastList();
    return ordered().flatMap(b => b.list);
  }

  // ticketCount counts the live sessions on each ticket, across every state and whatever the rail is
  // narrowed to, because that is what decides whether a ticket is worth a chip.
  function ticketCount() {
    const c = new Map();
    for (const s of sessions.values()) {
      const t = s.ticket || '';
      if (t) c.set(t, (c.get(t) || 0) + 1);
    }
    return c;
  }

  function setTicketFilter(ticket) {
    ticketFilter = ticket || null;
    // The selection is left alone: narrowing the rail should not pull the detail pane out from under you.
    renderList();
  }

  function apply(change) {
    const info = change.session;
    const prev = sessions.get(info.id);
    if (change.removed) {
      sessions.delete(info.id);
      tabs.delete(info.id);
      drafts.delete(info.id);
      if (selectedId === info.id) {
        // A session that ended stays on screen as its History entry, with Preview and Resume.
        if (info.claudeSessionId) {
          selectedId = info.claudeSessionId;
          loadPast();
        } else {
          const next = listed()[0];
          selectedId = next ? next.id : null;
          $('#main').classList.remove('show-detail');
        }
      }
    } else {
      sessions.set(info.id, info);
      if (change.live) maybeAlert(prev, info);
    }
    render();
    if (info.id === linkedId) followLink();
  }

  // A narrow screen shows the list or the detail, not both.
  const onScreen = id => !document.hidden && selectedId === id && (!narrow.matches || $('#main').classList.contains('show-detail'));

  function maybeAlert(prev, info) {
    if (!needs(info)) return;
    if (prev && needs(prev) && prev.state.kind === info.state.kind && summary(prev) === summary(info)) return;
    const title = `${short(info.repo)} · ${info.ticket || 'no ticket'} needs you`;
    const body = summary(info);
    if (!alertsOn || onScreen(info.id)) return;
    toast(title, body, info.id);
    beep();
    if (!('Notification' in window) || Notification.permission !== 'granted') return;
    const n = new Notification(title, { body, tag: info.id });
    n.onclick = () => { window.focus(); if (sessions.has(info.id)) select(info.id); n.close(); };
  }

  /* ---------- Rendering ---------- */

  function render() {
    renderCounts();
    renderList();
    renderDetail();
  }

  function renderCounts() {
    const c = { all: sessions.size, needs: 0, working: 0, done: 0, history: past.size };
    for (const s of sessions.values()) c[group(s)] += 1;
    for (const k of Object.keys(c)) $(`#c-${k}`).textContent = c[k];
    document.title = c.needs ? `(${c.needs}) ${TITLE}` : TITLE;
    announceNeeds(c.needs);
    renderSearch();
    // .has colours a filter's count in its own state colour, so the top bar reports the board: a grey row of
    // zeroes when nothing is live, one amber dot the moment a session blocks.
    document.querySelectorAll('.filter').forEach(b => {
      b.setAttribute('aria-pressed', String(b.dataset.filter === filter));
      b.classList.toggle('has', c[b.dataset.filter] > 0);
    });
  }

  // The live filter and the History search share the top of the rail; the open view picks which one shows.
  function renderSearch() {
    $('#q').hidden = filter === 'history';
    $('#h-search').hidden = filter !== 'history';
  }

  // announceNeeds says out loud what the amber count says on screen. Only a change is announced: a screen
  // reader repeating the same sentence on every event would bury everything else the page says.
  // Nothing is announced before the first load: an empty map would read as "No sessions need you."
  let announced = -1;
  let loaded = false;
  function announceNeeds(n) {
    if (!loaded || n === announced) return;
    announced = n;
    const el = $('#live-status');
    if (!el) return;
    el.textContent = n === 0 ? 'No sessions need you.'
      : n === 1 ? '1 session needs you.'
      : `${n} sessions need you.`;
  }

  // An empty state is either one of the five above, or a passing line such as "Loading…".
  function emptyHtml(e) {
    return typeof e === 'string'
      ? `<div class="empty-list">${esc(e)}</div>`
      : `<div class="empty-list"><b>${esc(e.head)}</b><span>${esc(e.body)}</span></div>`;
  }

  function toneOf(s) {
    if (needs(s)) return 'r-needs';
    return s.state.kind === 'working' ? 'r-work' : 'r-done';
  }

  function rowHtml(s) {
    const k = KIND[s.state.kind] || KIND.unknown;
    const text = summary(s);
    // The stripe and the block heading already say which state this is, so the pill stays only where the kind
    // is finer than the block - Question, Permission, Idle - and goes where it would repeat the heading.
    const pill = k.label === BLOCK[group(s)] ? '' : `<span class="state ${k.cls}">${k.label}</span>`;
    const branch = s.branch ? `<span class="row-branch" title="${esc(s.branch)}">${esc(s.branch)}</span>` : '';
    // A span, not a button: a button inside a button is invalid. The rail's click handler reads the attribute,
    // and the keyboard reaches the same thing with t on the selected row.
    const tix = s.ticket && tixCount.get(s.ticket) > 1
      ? `<span class="row-tix" data-ticket="${esc(s.ticket)}" title="Show only ${esc(s.ticket)}">${esc(s.ticket)}</span>`
      : '';
    const flag = s.delivery === 'failed' ? '<span class="row-flag">Not sent</span>' : '';
    const tag = (s.discovered ? `<span class="row-tag" title="Started outside the app${s.inTmux ? ', in tmux' : ''}">${s.inTmux ? 'yours · tmux' : 'yours'}</span>` : '')
      + (s.worktree ? `<span class="row-tag" title="${esc(s.dir)}">worktree</span>` : '')
      + (s.state.fromScreen ? '<span class="row-tag" title="No hook reported this; the app read it from the terminal">from screen</span>' : '');
    return `<button type="button" class="row ${toneOf(s)}" data-id="${esc(s.id)}" aria-current="${s.id === selectedId}">
      <span class="row-top"><span class="repo">${esc(short(s.repo) || 'unknown folder')}</span>${branch}${pill}${flag}${tag}${tix}<span class="age">${since(s.state.changedAt)}</span></span>
      <span class="row-text">${esc(text)}</span>
    </button>`;
  }

  // scopeHtml is the ticket view's readout: how that ticket's sessions sit across the three states, stated
  // rather than counted off the rows. It counts every session on the ticket, not only the filtered ones.
  function scopeHtml() {
    const n = { needs: 0, working: 0, done: 0 };
    let total = 0;
    for (const s of sessions.values()) {
      if ((s.ticket || '') !== ticketFilter) continue;
      n[group(s)] += 1;
      total += 1;
    }
    const mix = [
      n.needs ? `${n.needs} waiting` : '',
      n.working ? `${n.working} working` : '',
      n.done ? `${n.done} done` : '',
    ].filter(Boolean).join(' \u00b7 ');
    return `<div class="scope">
      <span class="tid">${esc(ticketFilter)}</span>
      <span class="scope-n">${total} ${total === 1 ? 'session' : 'sessions'}</span>
      <span class="scope-mix">${esc(mix)}</span>
      <button type="button" class="scope-x" data-act="clear-ticket">Clear</button>
    </div>`;
  }

  function renderList() {
    if (filter === 'history') {
      renderHistoryList();
      return;
    }
    const list = $('#list');
    tixCount = ticketCount();
    const blocks = ordered();
    const scope = ticketFilter ? scopeHtml() : '';
    if (!blocks.length) {
      const text = query ? `Nothing matches “${query}”.`
        : ticketFilter ? `Nothing on ${ticketFilter} matches this filter.`
          : EMPTY[filter];
      list.innerHTML = scope + emptyHtml(text);
      return;
    }
    // One block needs no heading: the pressed filter already names it. The ticket view keeps every heading,
    // because the split across states is the whole point of that view.
    const heads = blocks.length > 1 || !!ticketFilter;
    list.innerHTML = scope + blocks.map(b => `<section class="group" role="group" aria-label="${esc(b.label)}">
        ${heads ? `<div class="g-head g-${b.key}"><span class="g-label">${esc(b.label)}</span><span class="g-count">${b.list.length}</span></div>` : ''}
        ${b.list.map(rowHtml).join('')}
      </section>`).join('');
  }

  function renderDetail() {
    const s = sessions.get(selectedId);
    const detail = $('#detail');
    const c = past.get(selectedId);
    if (!s && c) {
      renderPastDetail(c);
      return;
    }
    if (!s) {
      unmountTerminal();
      paneKey = '';
      detail.classList.add('empty');
      $('#d-head').innerHTML = '';
      $('#tabs').innerHTML = '';
      $('#reply').innerHTML = '';
      replyKey = '';
      $('#pane').innerHTML = `<div class="empty-detail"><b>Nothing selected</b><span>Pick a session on the left, or start one with New session.</span></div>`;
      return;
    }
    detail.classList.remove('empty');
    const tab = currentTab(s);
    // Changes stream in while the title is edited; redrawing the head would throw the typed text away.
    if (!(renamingId === s.id && $('#rename-input'))) $('#d-head').innerHTML = headHtml(s);
    $('#tabs').innerHTML = tabsHtml(tabsOf(s), tab);
    labelPane(tab);
    renderReply(s);

    const key = `${s.id}:${tab}`;
    if (key !== paneKey) {
      paneKey = key;
      if (tab === 'diff') {
        unmountTerminal();
        loadDiff(s);
      } else {
        mountTerminal(s);
      }
    }
  }

  // A session outside tmux has no terminal the app can attach to.
  function tabsOf(s) {
    return s.inTmux ? ['terminal', 'diff'] : ['diff'];
  }

  function currentTab(s) {
    const list = tabsOf(s);
    return list.includes(tabs.get(s.id)) ? tabs.get(s.id) : list[0];
  }

  // The WAI-ARIA tabs pattern: each tab names the panel it controls, and only the selected one is a tab stop,
  // so Tab leaves the tablist and the arrow keys move inside it (see the keydown handler on #tabs).
  const tabsHtml = (list, tab) => list.map(t => `<button type="button" class="tab" role="tab" id="tab-${t}" data-tab="${t}" aria-controls="pane" aria-selected="${t === tab}" tabindex="${t === tab ? 0 : -1}">${TAB_LABEL[t]}</button>`).join('');

  // labelPane ties the panel to the tab that opened it, so a screen reader reads the panel with a name.
  const labelPane = tab => $('#pane').setAttribute('aria-labelledby', `tab-${tab}`);

  function titleHtml(s) {
    const fallback = short(s.repo) || 'unknown folder';
    if (renamingId !== s.id) return `<h2>${esc(s.title || fallback)}</h2>`;
    return `<form class="rename" id="rename-form"><input id="rename-input" value="${esc(s.title)}" placeholder="${esc(fallback)}" aria-label="Session title"></form>`;
  }

  function startRename(s) {
    renamingId = s.id;
    renderDetail();
    $('#rename-input').select();
  }

  function cancelRename() {
    if (renamingId === null) return;
    renamingId = null;
    renderDetail();
  }

  async function saveRename(s, title) {
    renamingId = null;
    try {
      apply({ session: await api('PATCH', `/api/session/${encodeURIComponent(s.id)}`, { title }), live: false });
    } catch (err) {
      toast('Could not rename', errorText(err));
      renderDetail();
    }
  }

  function headHtml(s) {
    const k = KIND[s.state.kind] || KIND.unknown;
    const sinceLabel = SINCE_LABEL[s.state.kind] || 'waiting';
    const closeLabel = s.discovered ? 'Hide' : closeArmed === s.id ? 'Click again to close' : 'Close session';
    return `
      <button type="button" class="btn small back" data-act="back">← All sessions</button>
      <div class="d-title">
        <span class="tid${s.ticket ? ' ticket' : ''}">${esc(s.ticket || 'No ticket')}</span>${titleHtml(s)}
        <span class="state ${k.cls}">${k.label}</span>
        <div class="d-tools">
          ${pageConfig.secondaryActions.length ? '<button type="button" class="btn small" data-act="actions" aria-haspopup="listbox" title="Actions (.)">Actions ▾</button><span class="act-sep"></span>' : ''}
          <button type="button" class="btn small" data-act="rename">Rename</button>
          ${pageConfig.ide ? '<button type="button" class="btn small" data-act="ide">Open in IDE</button>' : ''}
          <button type="button" class="btn small${s.discovered ? '' : ' danger'}${closeArmed === s.id ? ' armed' : ''}" data-act="close">${closeLabel}</button>
        </div>
      </div>
      <div class="d-meta">
        ${s.branch ? `<span${s.discovered ? '' : ` title="tmux ${esc(s.tmuxName)}"`}>${esc(s.branch)}</span>` : ''}
        ${s.worktree ? `<span title="${esc(s.dir)}">worktree</span>` : ''}
        ${s.discovered ? `<span>started outside the app${s.inTmux ? ', in tmux' : ', not in tmux'}</span>` : ''}
        <span>${esc(sinceLabel)} ${since(s.state.changedAt)}</span>
        <span>started ${since(s.createdAt)} ago</span>
        ${s.state.permissionMode && s.state.permissionMode !== 'auto' ? `<span>${esc(s.state.permissionMode)} mode</span>` : ''}
      </div>`;
  }

  /* ---------- Answer panel ---------- */

  // renderReply redraws the answer panel only when it changed: every update of any session renders, and a redraw
  // would move the caret of the reply being typed. A redraw that keeps the text keeps the caret too.
  function renderReply(s) {
    const html = actionBarHtml(s) + replyHtml(s);
    const key = `${s.id}\n${html}`;
    if (key === replyKey) return;
    replyKey = key;
    const reply = $('#reply');
    const old = $('#reply-input', reply);
    const hadFocus = document.activeElement === old;
    const caret = old && { value: old.value, start: old.selectionStart, end: old.selectionEnd, scroll: old.scrollTop };
    reply.innerHTML = html;
    fitBar();
    const input = $('#reply-input', reply);
    if (!input) return;
    input.value = drafts.get(s.id) || '';
    if (!hadFocus) return;
    input.focus();
    if (caret.value === input.value) {
      input.setSelectionRange(caret.start, caret.end);
      input.scrollTop = caret.scroll;
    }
  }

  // A free-text answer to a question goes into a one-line field on Claude's screen, so it cannot take line breaks.
  function sendForm(placeholder, withInterrupt, multiline = true) {
    return `<form class="send" id="send-form" autocomplete="off">
      <label class="sr" for="reply-input">Reply to this session</label>
      <textarea id="reply-input" rows="1" ${multiline ? '' : 'data-single-line'} placeholder="${esc(placeholder)}"></textarea>
      <button class="btn primary" type="submit">Send</button>
      ${withInterrupt ? '<button class="btn" type="button" data-act="interrupt">Interrupt</button>' : ''}
    </form>`;
  }

  function deliveryHtml(s) {
    if (s.delivery === 'pending') return '<div class="delivery">Sending… waiting for Claude to react.</div>';
    if (locked(s) && s.delivery !== 'failed') return '<div class="delivery">Nothing to answer on the screen: the answer most likely reached Claude already. The buttons come back when it asks again.</div>';
    if (s.delivery === 'failed') return '<div class="delivery failed">Not sent. Claude did not react; check the terminal.</div>';
    return '';
  }

  // The ask header reads as a sentence: who is asking, then the tool as a quiet mono tag, then how long it
  // has been waiting — the first thing you want when you come back to the screen. askOf is the session the
  // panel is being built for; replyHtml and viewOnlyHtml set it, so every ask() gets the elapsed label
  // without threading it through a dozen call sites.
  let askOf = null;

  function ask(kindClass, label, body) {
    const [who, ...rest] = String(label).split(' · ');
    const tag = rest.join(' · ');
    const ago = askOf ? `<span class="ago">${esc(SINCE_LABEL[askOf.state.kind] || 'waiting')} ${since(askOf.state.changedAt)}</span>` : '';
    const tool = tag ? `<span class="tool">${esc(tag)}</span>` : '';
    return `<div class="ask ${kindClass}"><div class="ask-head"><span class="who">${esc(who)}</span>${tool}${ago}</div>${body}</div>`;
  }

  // locked: an answer to this state is on its way, or found nothing left to answer; a second click would land in
  // Claude's prompt as text.
  function locked(s) {
    return s.delivery === 'pending' || nothingOnScreen.get(s.id) === s.state.seq;
  }

  const off = s => (locked(s) ? ' disabled' : '');

  function optionButtons(s, options) {
    return `<div class="opts">${options.map((o, i) => `<button type="button" class="opt" data-opt="${i}"${off(s)}><kbd>${i + 1}</kbd>${esc(o.label)}${o.description ? `<span class="note">${esc(o.description)}</span>` : ''}</button>`).join('')}</div>`;
  }

  // viewOnlyHtml shows what a session outside tmux waits on; the app cannot type into it.
  function viewOnlyHtml(s) {
    askOf = s;
    const st = s.state;
    const note = '<p class="ask-note">This session does not run in tmux, so the app cannot type into it. Answer it where it runs.</p>';
    switch (st.kind) {
      case 'question': {
        const q = st.questions[0];
        const options = `<ol class="opt-list">${q.options.map(o => `<li>${esc(o.label)}${o.description ? `<span class="note">${esc(o.description)}</span>` : ''}</li>`).join('')}</ol>`;
        return ask('k-question', q.header ? `Claude is asking · ${q.header}` : 'Claude is asking', `<p class="ask-text">${esc(q.question)}</p>${options}${note}`);
      }
      case 'permission': {
        const p = st.permission || {};
        return ask('k-permission', `Permission needed · ${p.tool || ''}`, `${p.description ? `<p class="ask-text">${esc(p.description)}</p>` : ''}<pre class="cmd">${esc(p.detail)}</pre>${note}`);
      }
      case 'unknown':
        return ask('k-unknown', 'Check the session', `<p class="ask-text">The session waits on something the app does not recognize.</p>${note}`);
      case 'done':
        return ask(`k-done${st.failed ? ' failed' : ''}`, st.failed ? 'Stopped with an error' : 'Finished', `<p class="ask-text">${esc(st.lastMessage || '(no message)')}</p>${note}`);
      default:
        return note;
    }
  }

  function replyHtml(s) {
    askOf = s;
    if (!s.inTmux) return viewOnlyHtml(s);
    const st = s.state;
    const delivery = deliveryHtml(s);
    switch (st.kind) {
      case 'question': {
        const q = st.questions[0];
        if (!st.canAnswer) {
          return ask('k-unknown', 'Answer in the terminal', `<p class="ask-text">This question has several parts or allows several answers. Answer it in the Terminal tab.</p>`) + delivery;
        }
        if (st.fromScreen) {
          return ask('k-question', 'Claude Code is asking · read from the screen', `<p class="ask-text">${esc(q.question)}</p>${optionButtons(s, q.options)}
            <p class="ask-note">No hook reported this; the app read it from the terminal. Pick an entry, or answer in the Terminal tab.</p>`) + delivery;
        }
        return ask('k-question', q.header ? `Claude is asking · ${q.header}` : 'Claude is asking', `<p class="ask-text">${esc(q.question)}</p>${optionButtons(s, q.options)}`)
          + delivery + sendForm('Or type your own answer…', false, false);
      }
      case 'permission': {
        const p = st.permission || {};
        return ask('k-permission', `Permission needed · ${p.tool || ''}`,
          `${p.description ? `<p class="ask-text">${esc(p.description)}</p>` : ''}<pre class="cmd">${esc(p.detail)}</pre>
           <div class="opts"><button type="button" class="opt" data-allow="true"${off(s)}>Allow</button><button type="button" class="opt" data-allow="false"${off(s)}>Deny</button></div>`)
          + delivery + sendForm('Or deny and tell it what to do instead…', false);
      }
      case 'trust':
        return ask('k-trust', 'Trust this folder?',
          `<p class="ask-text">Claude Code has not worked in ${esc(s.dir)} before and asks whether to trust it. It can read, edit and run files there.</p>
           <div class="opts"><button type="button" class="opt" data-trust="true"${off(s)}>Trust this folder</button><button type="button" class="opt" data-trust="false"${off(s)}>Don't trust (Claude exits)</button></div>`) + delivery;
      case 'unknown':
        return ask('k-unknown', 'Check the terminal', '<p class="ask-text">The session waits on something the app does not recognize. Answer it in the Terminal tab.</p>');
      case 'working':
        return delivery + sendForm('Message this session. It reads it after the current step.', true);
      case 'done':
        return delivery + sendForm('Ask for something else…', false);
      case 'idle':
        return delivery + sendForm('Type a prompt…', false);
      default:
        return '';
    }
  }

  // One send per session at a time, so a second Enter or click before the first returns does not send it twice.
  async function sendOnce(s, send) {
    if (sending.has(s.id)) return;
    sending.add(s.id);
    try {
      await send();
    } finally {
      sending.delete(s.id);
    }
  }

  function sent(s, info) {
    drafts.delete(s.id);
    if (s.id === selectedId) replyKey = '';
    apply({ session: info, live: false });
  }

  function sendAnswer(s, body) {
    return sendOnce(s, async () => {
      try {
        sent(s, await api('POST', `/api/session/${encodeURIComponent(s.id)}/answer`, { seq: s.state.seq, ...body }));
      } catch (err) {
        if (err.code === NOTHING_ON_SCREEN) {
          nothingOnScreen.set(s.id, s.state.seq);
          if (s.id === selectedId) renderDetail();
        } else {
          toast('Answer not sent', errorText(err));
        }
        refresh(s.id);
      }
    });
  }

  function sendMessage(s, text) {
    return sendOnce(s, async () => {
      try {
        sent(s, await api('POST', `/api/session/${encodeURIComponent(s.id)}/message`, { text }));
      } catch (err) {
        toast('Message not sent', errorText(err));
      }
    });
  }

  async function refresh(id) {
    try {
      apply({ session: await api('GET', `/api/session/${encodeURIComponent(id)}`), live: false });
    } catch (err) {
      if (err.status === 404) apply({ session: { id }, removed: true });
    }
  }

  function submitText(s, text) {
    if (!text.trim()) return;
    if (s.state.kind === 'question' || s.state.kind === 'permission') {
      sendAnswer(s, { text });
    } else {
      sendMessage(s, text);
    }
  }

  function pickOption(s, i) {
    if (!s.inTmux || s.state.kind !== 'question' || !s.state.canAnswer || !s.state.questions[0].options[i]) return;
    sendAnswer(s, { option: i });
  }

  /* ---------- Actions ---------- */

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

  /* ---------- Terminal ---------- */

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

  /* ---------- Preview ---------- */

  async function loadPreview(claudeSessionId) {
    const key = paneKey;
    const pane = $('#pane');
    pane.innerHTML = '<div class="diff"><p class="empty-pane">Loading the preview…</p></div>';
    let p;
    try {
      p = await api('GET', `/api/conversation/${encodeURIComponent(claudeSessionId)}/preview`);
    } catch (err) {
      if (paneKey === key) pane.innerHTML = `<div class="diff"><p class="empty-pane">${esc(errorText(err))}</p></div>`;
      return;
    }
    if (paneKey === key) pane.innerHTML = previewHtml(p);
  }

  function previewHtml(p) {
    const text = t => `<p class="pv-text">${esc(t)}</p>`;
    const block = (label, body) => (body ? `<section class="pv"><h4>${label}</h4>${body}</section>` : '');
    const body = block('First prompt', p.firstPrompt && text(p.firstPrompt))
      + block('Last prompts', p.lastPrompts.map(text).join(''))
      + block('Claude\'s last reply', p.lastReply && text(p.lastReply));
    return `<div class="diff preview">${body || '<p class="empty-pane">No prompt or reply was found in this conversation.</p>'}</div>`;
  }

  /* ---------- Diff ---------- */

  async function loadDiff(s) {
    const pane = $('#pane');
    pane.innerHTML = '<div class="diff"><p class="empty-pane">Loading the diff…</p></div>';
    let d;
    try {
      d = await api('GET', `/api/session/${encodeURIComponent(s.id)}/diff`);
    } catch (err) {
      if (paneKey.startsWith(`${s.id}:diff`)) pane.innerHTML = `<div class="diff"><p class="empty-pane">${esc(errorText(err))}</p></div>`;
      return;
    }
    if (!paneKey.startsWith(`${s.id}:diff`)) return;
    try {
      pane.innerHTML = diffHtml(d);
    } catch (err) {
      pane.innerHTML = `<div class="diff"><p class="empty-pane">The diff could not be shown: ${esc(err.message)}</p></div>`;
    }
  }

  function fileTag(f) {
    if (f.untracked) return `<span class="tag">new, untracked${f.binary ? ', binary' : f.uncounted ? ', not counted' : ''}</span>`;
    if (f.binary) return '<span class="tag">binary</span>';
    return '';
  }

  // Five blocks per file, the way a review tool draws them: the shape of a change reads before anything is
  // opened. A file that added and deleted keeps at least one block of each, so 200 added and 1 deleted still
  // reports the deletion. Nothing is drawn for a binary or uncounted file, which has no counts to split.
  function diffBar(f) {
    if (f.binary || f.uncounted) return '';
    const total = f.added + f.deleted;
    let green = total ? Math.round((f.added / total) * DIFF_BLOCKS) : 0;
    if (f.added && green === 0) green = 1;
    if (f.deleted && green === DIFF_BLOCKS) green = DIFF_BLOCKS - 1;
    const cells = [];
    for (let i = 0; i < DIFF_BLOCKS; i++) {
      cells.push(`<i class="${!total ? 'n' : i < green ? 'a' : 'd'}"></i>`);
    }
    // The counts next to it already say this in words.
    return `<span class="bar" aria-hidden="true">${cells.join('')}</span>`;
  }

  function diffHtml(d) {
    d.files = d.files || [];
    const add = d.files.reduce((n, f) => n + f.added, 0);
    const del = d.files.reduce((n, f) => n + f.deleted, 0);
    const head = `<div class="diff-head"><span>Against <span class="mono">${esc(d.base)}</span></span><span><span class="add">+${add}</span> <span class="del">−${del}</span> in ${d.files.length} files</span>
      ${d.truncated ? '<span>Cut short: the diff is too large to show whole.</span>' : ''}<button type="button" class="btn small" data-act="refresh-diff">Refresh</button></div>`;
    if (!d.files.length) return `<div class="diff">${head}<p class="empty-pane">No changes yet.</p></div>`;
    // git writes the patch in the order of the file list, one "diff --git" section per tracked file; untracked files
    // come last and have none. A section cut off by the size cap leaves the files after it without one.
    const sections = d.patch ? d.patch.split(/^(?=diff --git )/m).filter(Boolean) : [];
    const files = d.files.map((f, i) => {
      const summary = `<span class="path">${esc(f.path)}</span>${fileTag(f)}${f.binary || f.uncounted ? '' : `<span class="add">+${f.added}</span><span class="del">−${f.deleted}</span>`}${diffBar(f)}`;
      const section = f.untracked ? '' : sections[i];
      if (!section) return `<div class="file">${summary}</div>`;
      const open = openDiffFiles.has(f.path) ? ' open' : '';
      return `<details class="file-diff" data-path="${esc(f.path)}"${open}><summary class="file">${summary}</summary><div class="patch"><pre>${patchLines(section)}</pre></div></details>`;
    }).join('');
    return `<div class="diff">${head}<div class="files">${files}</div></div>`;
  }

  // patchLines leaves out a section's header (diff --git, index, ---, +++): the file row above already names it.
  function patchLines(section) {
    const hunks = section.search(/^@@/m);
    return (hunks > 0 ? section.slice(hunks) : section).replace(/\n$/, '').split('\n').map(line => {
      const cls = line.startsWith('@@') ? 'm' : line.startsWith('+') && !line.startsWith('+++') ? 'a' : line.startsWith('-') && !line.startsWith('---') ? 'd' : '';
      return `<span class="h ${cls}">${esc(line)}</span>`;
    }).join('');
  }

  /* ---------- Selection and actions ---------- */

  // Switching sessions cross-fades the detail pane, so it is visible that the pane changed under you and
  // which row you landed on. The callback runs on the next frame, so everything that reads the drawn rows
  // goes inside it. A browser without view transitions, or a reader who asked for less motion, just swaps.
  function withTransition(draw) {
    if (reducedMotion.matches || !document.startViewTransition) {
      draw();
      return;
    }
    document.startViewTransition(draw);
  }

  function select(id, show = true) {
    selectedId = id;
    closeArmed = null;
    closeMenu();
    // The address names the selected session, so a link (#session=<id>) to another one is a change the page sees.
    history.replaceState(null, '', `#${SESSION_HASH}${encodeURIComponent(id)}`);
    withTransition(() => {
      render();
      const row = document.querySelector(`.row[data-id="${CSS.escape(id)}"]`);
      if (row) row.scrollIntoView({ block: 'nearest' });
    });
    if (show && narrow.matches) $('#main').classList.add('show-detail');
  }

  async function hideSession(s) {
    try {
      await api('DELETE', `/api/session/${encodeURIComponent(s.id)}`);
      toast('Hidden', 'It comes back the next time it does something.');
      apply({ session: s, removed: true });
    } catch (err) {
      toast('Could not hide the session', errorText(err));
    }
  }

  // The IDE can open the folder but not a terminal tab, so the command that attaches to the session goes to the
  // clipboard to paste into an IDE terminal. It is copied first: once the IDE takes the focus, the browser
  // refuses clipboard writes from this page.
  async function openInIDE(s) {
    let copied = '';
    if (s.inTmux && s.attach) {
      try {
        await navigator.clipboard.writeText(s.attach);
        copied = s.attach;
      } catch {
        copied = '';
      }
    }
    try {
      await api('POST', `/api/session/${encodeURIComponent(s.id)}/open-ide`);
    } catch (err) {
      toast('Could not open the IDE', errorText(err));
      return;
    }
    if (copied) {
      toast(`Opening ${s.repo} in the IDE`, `Copied ${copied}. Paste it into an IDE terminal to open the session.`);
    } else if (s.inTmux) {
      toast(`Opening ${s.repo} in the IDE`, `Copy is blocked here. To open the session, run: ${s.attach}`);
    } else {
      toast(`Opening ${s.repo} in the IDE`);
    }
  }

  async function closeSession(s) {
    if (s.discovered) {
      hideSession(s);
      return;
    }
    if (closeArmed !== s.id) {
      closeArmed = s.id;
      renderDetail();
      setTimeout(() => { if (closeArmed === s.id) { closeArmed = null; renderDetail(); } }, CLOSE_ARM_MS);
      return;
    }
    closeArmed = null;
    try {
      await api('DELETE', `/api/session/${encodeURIComponent(s.id)}`);
      toast(`Closed ${s.tmuxName}`, 'The session ended and was archived. The branch stays as it is.');
      apply({ session: s, removed: true });
    } catch (err) {
      toast('Could not close the session', errorText(err));
      renderDetail();
    }
  }

  async function act(s, action, target) {
    switch (action) {
      case 'actions':
        if (menu?.list === SECONDARY) closeMenu(); else openMenu(SECONDARY, target);
        break;
      case 'more-actions':
        if (menu?.list === PRIMARY && menu.only === undefined) closeMenu(); else openMenu(PRIMARY, target);
        break;
      case 'back':
        $('#main').classList.remove('show-detail');
        break;
      case 'rename':
        startRename(s);
        break;
      case 'ide':
        openInIDE(s);
        break;
      case 'close':
        closeSession(s);
        break;
      case 'interrupt':
        try {
          apply({ session: await api('POST', `/api/session/${encodeURIComponent(s.id)}/interrupt`), live: false });
        } catch (err) {
          toast('Could not interrupt', errorText(err));
        }
        break;
      case 'refresh-diff':
        loadDiff(s);
        break;
    }
  }

  $('#list').addEventListener('click', e => {
    // The chip and the Clear button sit inside the row, so they are read before the row itself.
    const tix = e.target.closest('[data-ticket]');
    if (tix) {
      setTicketFilter(tix.dataset.ticket);
      return;
    }
    if (e.target.closest('[data-act="clear-ticket"]')) {
      setTicketFilter(null);
      return;
    }
    const row = e.target.closest('.row');
    if (row) select(row.dataset.id);
  });

  $('#q').addEventListener('input', e => {
    query = e.target.value.trim().toLowerCase();
    // The selection is left alone, as the ticket chip leaves it: narrowing the rail should not pull the detail
    // pane out from under you.
    renderCounts();
    renderList();
  });

  document.querySelector('.filters').addEventListener('click', e => {
    const b = e.target.closest('.filter');
    if (!b) return;
    filter = b.dataset.filter;
    // History is past conversations, which the ticket view does not narrow; leaving it set would reapply it
    // silently on the way back.
    if (filter === 'history') {
      ticketFilter = null;
      loadPast();
    }
    renderCounts();
    renderList();
  });

  $('#detail').addEventListener('click', e => {
    const s = sessions.get(selectedId);
    const c = past.get(selectedId);
    if (!s && c) {
      const t = e.target.closest('[data-act]');
      if (t?.dataset.act === 'back') $('#main').classList.remove('show-detail');
      if (t?.dataset.act === 'resume-conversation') resumeConversation(c, t);
      return;
    }
    if (!s) return;
    const t = e.target.closest('[data-tab],[data-opt],[data-act],[data-allow],[data-trust],[data-action]');
    if (!t) return;
    if (t.dataset.tab) {
      tabs.set(s.id, t.dataset.tab);
      renderDetail();
    } else if (t.dataset.opt !== undefined) {
      pickOption(s, Number(t.dataset.opt));
    } else if (t.dataset.allow !== undefined) {
      sendAnswer(s, { allow: t.dataset.allow === 'true' });
    } else if (t.dataset.trust !== undefined) {
      sendAnswer(s, { trust: t.dataset.trust === 'true' });
    } else if (t.dataset.action !== undefined) {
      const i = Number(t.dataset.action);
      if (pageConfig.primaryActions[i]?.input) openMenu(PRIMARY, t, i); else activate(s, PRIMARY, i);
    } else {
      act(s, t.dataset.act, t);
    }
  });

  $('#detail').addEventListener('submit', e => {
    e.preventDefault();
    const s = sessions.get(selectedId);
    if (e.target.id === 'rename-form') {
      if (s) saveRename(s, $('#rename-input').value);
      return;
    }
    const input = $('#reply-input');
    if (s && input) submitText(s, input.value);
  });

  // The reply box grows with its text through field-sizing: content in app.css, so nothing measures it here.
  $('#detail').addEventListener('input', e => {
    if (e.target.id !== 'reply-input') return;
    if (selectedId) drafts.set(selectedId, e.target.value);
  });

  // Enter sends; Shift+Enter starts a new line where the text can have several. The terminal's own textarea is
  // left alone.
  function enterSends(e) {
    if (!e.target.matches('#reply-input, #ns-prompt') || e.key !== 'Enter' || e.isComposing) return;
    if (e.shiftKey && !e.target.hasAttribute('data-single-line')) return;
    e.preventDefault();
    e.target.form.requestSubmit();
  }

  // The arrow keys move between tabs and the roving tabindex keeps one tab stop, as the tabs pattern wants.
  // Moving the focus also opens the tab: these panels are cheap, and it keeps focus and panel in step.
  $('#tabs').addEventListener('keydown', e => {
    const list = [...$('#tabs').querySelectorAll('[role="tab"]')];
    const i = list.indexOf(document.activeElement);
    if (i < 0 || list.length < 2) return;
    const last = list.length - 1;
    const to = e.key === 'ArrowRight' ? (i === last ? 0 : i + 1)
      : e.key === 'ArrowLeft' ? (i === 0 ? last : i - 1)
        : e.key === 'Home' ? 0
          : e.key === 'End' ? last
            : -1;
    if (to < 0) return;
    e.preventDefault();
    list[to].click();
    $(`#tab-${list[to].dataset.tab}`)?.focus();
  });

  $('#detail').addEventListener('keydown', enterSends);
  $('#detail').addEventListener('keydown', e => {
    if (e.target.id === 'rename-input' && e.key === 'Escape') cancelRename();
  });
  $('#detail').addEventListener('focusout', e => {
    if (e.target.id === 'rename-input') cancelRename();
  });
  $('#new-form').addEventListener('keydown', enterSends);

  /* ---------- Alerts ---------- */

  // beep is two short notes from an oscillator rather than an audio file: nothing to vendor, nothing to fetch,
  // and it works with the page offline like everything else here. It plays only once unlockAudio has run: a
  // suspended context would hold every note and play them all at once on the next click.
  function beep() {
    if (!soundOn || audio?.state !== 'running') return;
    try {
      const at = audio.currentTime;
      const gain = audio.createGain();
      gain.connect(audio.destination);
      // Ramps, not steps: a square edge on the gain is heard as a click.
      gain.gain.setValueAtTime(0.0001, at);
      gain.gain.exponentialRampToValueAtTime(0.12, at + 0.01);
      gain.gain.exponentialRampToValueAtTime(0.0001, at + 0.34);
      const osc = audio.createOscillator();
      osc.type = 'sine';
      osc.frequency.setValueAtTime(660, at);
      osc.frequency.setValueAtTime(880, at + 0.12);
      osc.connect(gain);
      osc.start(at);
      osc.stop(at + 0.35);
    } catch { /* no audio device, or the browser refused: the toast and the notification still fire */ }
  }

  // unlockAudio opens or resumes the audio context. Browsers allow that only inside a user gesture, so it runs
  // from clicks and keys, never from an alert.
  function unlockAudio() {
    if (!soundOn) return Promise.resolve();
    try {
      audio ||= new (window.AudioContext || window.webkitAudioContext)();
      return audio.state === 'running' ? Promise.resolve() : audio.resume();
    } catch (err) {
      return Promise.reject(err);
    }
  }

  // Sound left on from the last visit has no click behind it after a reload: the first one on the page unlocks it.
  for (const type of ['pointerdown', 'keydown']) {
    document.addEventListener(type, () => { unlockAudio().catch(() => {}); }, { capture: true });
  }

  function renderSound() {
    const b = $('#sound');
    b.setAttribute('aria-pressed', String(soundOn));
    b.textContent = soundOn ? 'Sound on' : 'Sound off';
  }

  $('#sound').addEventListener('click', () => {
    soundOn = !soundOn;
    writeSetting('sound', soundOn);
    renderSound();
    // Play it once on the way on: it confirms the setting, and this click is the gesture that unlocks audio.
    if (soundOn) unlockAudio().then(beep).catch(() => {});
  });

  function renderAlerts() {
    const b = $('#alerts');
    b.setAttribute('aria-pressed', String(alertsOn));
    b.textContent = alertsOn ? 'Alerts on' : 'Alerts off';
  }

  $('#alerts').addEventListener('click', async () => {
    alertsOn = !alertsOn;
    writeSetting('alerts', alertsOn);
    renderAlerts();
    if (!alertsOn || !('Notification' in window)) return;
    if (Notification.permission === 'default') await Notification.requestPermission();
    if (Notification.permission === 'denied') {
      toast('Desktop notifications are blocked', 'Allow them in Chrome\'s site settings for this page. Alerts inside the page still work.');
    }
  });

  /* ---------- New session ---------- */

  const overlay = $('#overlay');

  async function openModal() {
    // A native <dialog>: the browser owns the backdrop, the focus trap, Esc, and the inertness of the page
    // behind it, and app.css animates it in and out with @starting-style.
    overlay.showModal();
    $('#ns-error').hidden = true;
    const select = $('#ns-repo');
    if (!repoList) {
      select.innerHTML = '<option>Loading…</option>';
      try {
        repoList = await api('GET', '/api/repos');
      } catch (err) {
        select.innerHTML = '';
        showFormError(errorText(err));
        return;
      }
    }
    const option = r => `<option value="${esc(r.dir)}">${esc(r.name)}</option>`;
    // The server sends the list in display order; each run of one group is one heading, no group none.
    let html = '';
    for (let i = 0; i < repoList.length;) {
      const title = repoList[i].group || '';
      let end = i;
      while (end < repoList.length && (repoList[end].group || '') === title) end++;
      const options = repoList.slice(i, end).map(option).join('');
      html += title ? `<optgroup label="${esc(title)}">${options}</optgroup>` : options;
      i = end;
    }
    select.innerHTML = html || '<option value="">No repositories yet: run claude in one, or set roots in the config</option>';
    select.focus();
  }

  // showModal remembers what had focus and gives it back on close, so nothing needs refocusing here.
  function closeModal() {
    if (overlay.open) overlay.close();
  }

  function showFormError(text) {
    const el = $('#ns-error');
    el.textContent = text;
    el.hidden = false;
  }

  // toggle does not bubble, hence the capture.
  $('#pane').addEventListener('toggle', e => {
    const path = e.target.dataset?.path;
    if (path === undefined) return;
    if (e.target.open) openDiffFiles.add(path); else openDiffFiles.delete(path);
  }, true);

  $('#new-session').addEventListener('click', openModal);
  $('#ns-worktree').addEventListener('change', e => {
    $('#ns-branch-field').hidden = !e.target.checked;
    if (e.target.checked) $('#ns-branch').focus();
  });
  $('#ns-cancel').addEventListener('click', closeModal);
  overlay.addEventListener('click', e => { if (e.target === overlay) closeModal(); });

  $('#new-form').addEventListener('submit', async e => {
    e.preventDefault();
    const submit = $('#ns-submit');
    submit.disabled = true;
    try {
      const info = await api('POST', '/api/sessions', {
        dir: $('#ns-repo').value,
        ticket: $('#ns-ticket').value.trim(),
        title: $('#ns-title').value.trim(),
        prompt: $('#ns-prompt').value.trim(),
        worktree: $('#ns-worktree').checked,
        branch: $('#ns-branch').value.trim(),
      });
      closeModal();
      $('#new-form').reset();
      $('#ns-branch-field').hidden = true;
      apply({ session: info, live: false });
      select(info.id);
      toast(`Started ${info.tmuxName}`, 'Running in tmux. It shows up here when it needs you.');
    } catch (err) {
      showFormError(errorText(err));
    } finally {
      submit.disabled = false;
    }
  });

  /* ---------- History ---------- */

  async function loadPast() {
    try {
      const list = await api('GET', '/api/conversations');
      past.clear();
      for (const c of list) past.set(c.id, c);
      pastLoaded = true;
    } catch (err) {
      toast('Could not load History', errorText(err));
    }
    render();
  }

  function pastList() {
    const words = pastQuery.toLowerCase().split(/\s+/).filter(Boolean);
    return [...past.values()].filter(c => {
      const text = [c.title, c.repo, c.ticket, c.branch, c.dir].join(' ').toLowerCase();
      return words.every(w => text.includes(w));
    });
  }

  // A past conversation reads like an ended session's row.
  function pastRowHtml(c) {
    const text = [c.ticket || 'No ticket', c.title].filter(Boolean).join(' · ');
    return `<button type="button" class="row r-ended" data-id="${esc(c.id)}" aria-current="${c.id === selectedId}">
      <span class="row-top"><span class="repo">${esc(short(c.repo) || 'unknown folder')}</span><span class="state ${KIND.ended.cls}">${KIND.ended.label}</span><span class="age">${since(c.updatedAt)}</span></span>
      <span class="row-text">${esc(text)}</span>
    </button>`;
  }

  function renderHistoryList() {
    const list = $('#list');
    if (!$('#h-rows')) list.innerHTML = '<div id="h-rows"></div>';
    const rows = pastList();
    const empty = !pastLoaded ? 'Loading…' : past.size ? 'Nothing matches.' : EMPTY.history;
    $('#h-rows').innerHTML = rows.length
      ? dayGroups(rows).map(([day, list]) => `<section class="group ended-group" aria-label="${day}">
          <div class="g-head"><span class="g-title">${day}</span><span class="g-count">${list.length} ${list.length === 1 ? 'conversation' : 'conversations'}</span></div>
          ${list.map(pastRowHtml).join('')}
        </section>`).join('')
      : emptyHtml(empty);
  }

  // dayGroups splits conversations, latest first, into Today, Yesterday, This week and Older, by local days.
  function dayGroups(rows) {
    const today = new Date().setHours(0, 0, 0, 0);
    const DAY = 24 * 60 * 60 * SEC;
    const dayOf = c => {
      const at = parseTime(c.updatedAt);
      if (at >= today) return 'Today';
      if (at >= today - DAY) return 'Yesterday';
      return at >= today - 6 * DAY ? 'This week' : 'Older';
    };
    const groups = new Map();
    for (const c of rows) {
      const day = dayOf(c);
      if (!groups.has(day)) groups.set(day, []);
      groups.get(day).push(c);
    }
    return [...groups.entries()];
  }

  function renderPastDetail(c) {
    $('#detail').classList.remove('empty');
    const branch = c.branch && c.branch !== 'HEAD' ? `<span class="mono">${esc(c.branch)}</span>` : '';
    $('#d-head').innerHTML = `
      <button type="button" class="btn small back" data-act="back">← All sessions</button>
      <div class="d-title"><span class="tid${c.ticket ? ' ticket' : ''}">${esc(c.ticket || 'No ticket')}</span><h2>${esc(c.title || short(c.repo))}</h2></div>
      <div class="d-tools"><button type="button" class="btn small" data-act="resume-conversation">Resume</button></div>
      <div class="d-meta">
        <span class="state ${KIND.ended.cls}">${KIND.ended.label} · last active ${since(c.updatedAt)} ago</span>
        <span>${esc(c.repo)}</span>${branch}<span class="mono">${esc(c.dir)}</span>
      </div>`;
    $('#tabs').innerHTML = tabsHtml(['preview'], 'preview');
    labelPane('preview');
    $('#reply').innerHTML = '';
    replyKey = '';
    const key = `${c.id}:preview`;
    if (key !== paneKey) {
      unmountTerminal();
      paneKey = key;
      loadPreview(c.id);
    }
  }

  async function resumeConversation(c, button) {
    button.disabled = true;
    try {
      const info = await api('POST', `/api/conversation/${encodeURIComponent(c.id)}/resume`);
      past.delete(c.id);
      filter = 'all';
      ticketFilter = null;
      apply({ session: info, live: false });
      select(info.id);
      toast(`Resumed in ${info.tmuxName}`, 'Running in tmux. It shows up here when it needs you.');
    } catch (err) {
      toast('Could not resume', errorText(err));
      button.disabled = false;
    }
  }

  $('#h-search').addEventListener('input', e => {
    pastQuery = e.target.value;
    renderList();
  });

  /* ---------- Keyboard ---------- */

  document.addEventListener('keydown', e => {
    if (overlay.open) return;  // the dialog handles its own Esc.
    const tag = e.target.tagName;
    if (tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA') {
      if (e.key === 'Escape' && e.target.id === 'reply-input') e.target.blur();
      if (e.key === 'Escape' && e.target.id === 'q') {
        e.target.value = '';
        query = '';
        renderCounts();
        renderList();
        e.target.blur();
      }
      return;
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    const flat = listed();
    const idx = flat.findIndex(s => s.id === selectedId);
    if (e.key === 'ArrowDown' || e.key === 'j') {
      e.preventDefault();
      const n = flat[Math.min(flat.length - 1, idx + 1)];
      if (n) select(n.id, false);
    } else if (e.key === 'ArrowUp' || e.key === 'k') {
      e.preventDefault();
      const p = flat[Math.max(0, idx - 1)];
      if (p) select(p.id, false);
    } else if (e.key === 'Enter' && !e.target.closest('button')) {
      const input = $('#reply-input');
      if (input) { e.preventDefault(); input.focus(); }
    } else if (e.key === '.' && pageConfig.secondaryActions.length && sessions.has(selectedId)) {
      e.preventDefault();
      openMenu(SECONDARY, document.querySelector('[data-act="actions"]'));
    } else if (e.key === 't' && sessions.get(selectedId)?.ticket) {
      // The ticket chip is not focusable, so this is how the keyboard narrows the rail to one ticket.
      e.preventDefault();
      setTicketFilter(ticketFilter ? null : sessions.get(selectedId).ticket);
    } else if (e.key === '/') {
      e.preventDefault();
      const box = filter === 'history' ? $('#h-search') : $('#q');
      box.focus();
      box.select();
    } else if (/^[1-9]$/.test(e.key)) {
      // A digit only moves to the option; Enter on it picks it, so a stray key never answers.
      const opt = document.querySelector(`#reply .opt[data-opt="${Number(e.key) - 1}"]`);
      if (opt) opt.focus();
    }
  });

  /* ---------- Links to a session ---------- */

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

  /* ---------- Live updates ---------- */

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

  /* ---------- Theme ---------- */

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

  renderAlerts();
  renderSound();
  render();
  // Without the config the optional buttons stay hidden; loadAll below reports why the server is unreachable.
  loadPageConfig().catch(() => {});
  loadAll().then(connect).catch(err => {
    setConnection(errorText(err), true);
    if (err.status !== 401) connect();
  });
})();
