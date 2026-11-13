'use strict';

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
  if (s.group) return groupRowHtml(s);
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
  // The title is what tells two sessions in one repository apart, so it leads the row; without one, the
  // repository takes its place, as in the detail header.
  const repo = short(s.repo) || 'unknown folder';
  const name = s.title || repo;
  const meta = (s.title ? `<span class="row-repo">${esc(repo)}</span>` : '') + branch + pill + flag + tag + tix;
  return `<button type="button" class="row ${toneOf(s)}" data-id="${esc(s.id)}" aria-current="${s.id === selectedId}">
    <span class="row-head"><span class="row-title" title="${esc(name)}">${esc(name)}</span><span class="age">${since(s.state.changedAt)}</span></span>
    ${meta ? `<span class="row-top">${meta}</span>` : ''}
    <span class="row-text">${esc(text)}</span>
  </button>`;
}

// groupRowHtml is a row inside a group's block. The header carries the ticket, title and branch, and no state
// block heading sits above the row, so the row names its repository and its state.
function groupRowHtml(s) {
  const k = s.state.stalled ? STALLED : KIND[s.state.kind] || KIND.unknown;
  const coordinator = s.role === 'coordinator';
  const name = coordinator ? 'Coordinator' : short(s.repo) || 'unknown folder';
  const flag = s.delivery === 'failed' ? '<span class="row-flag">Not sent</span>' : '';
  return `<button type="button" class="row in-group ${toneOf(s)}" data-id="${esc(s.id)}" aria-current="${s.id === selectedId}">
    <span class="row-head"><span class="row-title" title="${esc(s.tmuxName)}">${esc(name)}</span><span class="age">${since(s.state.changedAt)}</span></span>
    <span class="row-top"><span class="state ${k.cls}">${k.label}</span>${coordinator ? `<span class="row-repo">${esc(short(s.repo))}</span>` : ''}${flag}</span>
    <span class="row-text">${esc(summary(s))}</span>
  </button>`;
}

// groupHeadHtml heads a group's block: what the group works on, and how its sessions sit across the states. It
// turns amber while one of them needs you, folded or not.
function groupHeadHtml(g) {
  const lead = g.lead;
  const worker = g.members.find(s => s.role !== 'coordinator');
  const branch = worker?.branch || '';
  const mix = [
    g.counts.needs ? `<b>${g.counts.needs} waiting</b>` : '',
    g.counts.working ? `${g.counts.working} working` : '',
    g.counts.done ? `${g.counts.done} done` : '',
  ].filter(Boolean).join(' \u00b7 ');
  const open = !foldedGroups.has(g.id);
  return `<button type="button" class="gg-head${g.counts.needs ? ' gg-wait' : ''}" data-group="${esc(g.id)}" aria-expanded="${open}" title="${open ? 'Fold' : 'Unfold'} this group">
    <span class="gg-chev" aria-hidden="true"></span>
    <span class="gg-tid">${esc(lead.ticket || 'Group')}</span>
    <span class="gg-title">${esc(lead.title || '')}</span>
    <span class="gg-n">${g.members.length}</span>
    <span class="gg-mix">${mix}${branch ? ` \u00b7 <span class="gg-branch">${esc(branch)}</span>` : ''}</span>
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
  const blocks = railBlocks();
  const scope = ticketFilter ? scopeHtml() : '';
  if (!blocks.length) {
    const text = query ? `Nothing matches “${query}”.`
      : ticketFilter ? `Nothing on ${ticketFilter} matches this filter.`
        : EMPTY[filter];
    list.innerHTML = scope + emptyHtml(text);
    return;
  }
  // One block needs no heading: the pressed filter already names it. The ticket view keeps every heading,
  // because the split across states is the whole point of that view. A group always has its header.
  const heads = blocks.length > 1 || !!ticketFilter;
  list.innerHTML = scope + blocks.map(b => b.group
    ? `<section class="group sgroup" role="group" aria-label="${esc(`Group ${b.group.lead.ticket || ''}`)}">${groupHeadHtml(b.group)}${b.list.map(rowHtml).join('')}</section>`
    : `<section class="group" role="group" aria-label="${esc(b.label)}">
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
    } else if (tab === 'group') {
      unmountTerminal();
      loadGroup(s);
    } else {
      mountTerminal(s);
    }
  } else if (tab === 'group') {
    // Any change may be a member moving on or a message arriving, so the open Group tab follows along.
    loadGroup(s);
  }
}

// A session outside tmux has no terminal the app can attach to.
function tabsOf(s) {
  const list = s.inTmux ? ['terminal', 'diff'] : ['diff'];
  return s.group ? [...list, 'group'] : list;
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
  const k = s.state.stalled ? STALLED : KIND[s.state.kind] || KIND.unknown;
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
      ${s.group ? `<span>${esc(s.role)} in a group of ${groupSize(s.group)}</span>` : ''}
      ${s.discovered ? `<span>started outside the app${s.inTmux ? ', in tmux' : ', not in tmux'}</span>` : ''}
      <span>${esc(sinceLabel)} ${since(s.state.changedAt)}</span>
      <span>started ${since(s.createdAt)} ago</span>
      ${s.state.permissionMode && s.state.permissionMode !== 'auto' ? `<span>${esc(s.state.permissionMode)} mode</span>` : ''}
    </div>`;
}
