'use strict';

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
    case 'working': return st.stalled ? STALLED_TEXT : st.activity || 'Working';
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
  for (const s of sessions.values()) if (shown(s) && !s.group) byBlock.get(group(s)).push(s);
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

// sessionGroups are the groups started together (New session, Multi), each one block of the rail between Needs
// you and Working. A member stays in its group whatever its state; a group with a member that needs you comes
// first and says so in its header, so a blocked session is still seen at the top. Inside a group the coordinator
// leads, then the repositories by name. counts covers every member; list only the ones the filters let through.
function sessionGroups() {
  const byId = new Map();
  for (const s of sessions.values()) {
    if (!s.group) continue;
    if (!byId.has(s.group)) byId.set(s.group, { id: s.group, members: [], latest: 0 });
    const g = byId.get(s.group);
    g.members.push(s);
    g.latest = Math.max(g.latest, parseTime(s.state.changedAt));
  }
  const rank = s => (s.role === 'coordinator' ? '0' : '1') + (s.repo || '');
  const list = [...byId.values()].map(g => {
    g.members.sort((a, b) => (rank(a) < rank(b) ? -1 : rank(a) > rank(b) ? 1 : 0));
    g.counts = { needs: 0, working: 0, done: 0 };
    for (const s of g.members) g.counts[group(s)] += 1;
    g.list = g.members.filter(shown);
    g.lead = g.members[0];
    return g;
  }).filter(g => g.list.length);
  return list.sort((a, b) => (b.counts.needs > 0) - (a.counts.needs > 0) || b.latest - a.latest);
}

// railBlocks is the rail top to bottom: Needs you, the groups, then Working and Done.
function railBlocks() {
  const blocks = ordered();
  const groups = sessionGroups().map(g => ({ key: 'group', group: g, list: foldedGroups.has(g.id) ? [] : g.list }));
  const at = blocks[0]?.key === 'needs' ? 1 : 0;
  return [...blocks.slice(0, at), ...groups, ...blocks.slice(at)];
}

// listed is every row on screen, top to bottom.
function listed() {
  if (filter === 'history') return pastList();
  return railBlocks().flatMap(b => b.list);
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
