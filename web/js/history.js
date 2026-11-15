'use strict';

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
    <button type="button" class="btn small back" data-act="back">‹ All sessions</button>
    <div class="d-title"><span class="tid${c.ticket ? ' ticket' : ''}">${esc(c.ticket || 'No ticket')}</span><h2>${esc(c.title || short(c.repo))}</h2></div>
    <div class="d-tools"><button type="button" class="btn small" data-act="resume-conversation">Resume</button></div>
    <div class="d-meta">
      <span class="state ${KIND.ended.cls}">${KIND.ended.label} · last active ${since(c.updatedAt)} ago</span>
      <span>${esc(c.repo)}</span>${branch}<span class="mono">${esc(c.dir)}</span><span class="mono">${esc(c.id)}</span>
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
