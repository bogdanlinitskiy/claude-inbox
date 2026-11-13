'use strict';

// The Group tab: the sessions started together with this one, and what they said to each other
// (claude-inbox send). The roster comes from the live list; the messages from the group's log.

const groupSize = id => [...sessions.values()].filter(s => s.group === id).length;

// The group whose Close group waits for its second click, as Close session does for one session.
let closeGroupArmed = null;

// The groups whose log is being read: renderDetail calls loadGroup on every change, and one read at a time is
// enough to follow along.
const loadingGroups = new Set();

// loadGroup draws the tab at once from what is known, then again once the log has been read.
async function loadGroup(s) {
  paintGroup(s);
  if (loadingGroups.has(s.group)) return;
  loadingGroups.add(s.group);
  let msgs;
  try {
    msgs = await api('GET', `/api/group/${encodeURIComponent(s.group)}/messages`);
  } catch (err) {
    if (paneKey === `${s.id}:group`) $('.g-log').innerHTML = `<p class="empty-pane">${esc(errorText(err))}</p>`;
    return;
  } finally {
    loadingGroups.delete(s.group);
  }
  const before = JSON.stringify(groupMessages.get(s.group) || []);
  groupMessages.set(s.group, msgs);
  if (paneKey === `${s.id}:group` && JSON.stringify(msgs) !== before) paintGroup(s);
}

function paintGroup(s) {
  const members = sessionGroups().find(g => g.id === s.group)?.members
    || [...sessions.values()].filter(m => m.group === s.group);
  const roster = members.map(m => {
    const k = m.state.stalled ? STALLED : KIND[m.state.kind] || KIND.unknown;
    return `<button type="button" class="g-member" data-pick="${esc(m.id)}" aria-current="${m.id === s.id}">
      <span class="g-who"><b>${esc(m.tmuxName)}</b><small>${esc(m.role)} · ${esc(m.dir)}</small></span>
      <span class="state ${k.cls}">${k.label}</span>
    </button>`;
  }).join('');
  const msgs = groupMessages.get(s.group) || [];
  const log = msgs.length
    ? msgs.map(m => `<div class="g-msg"><span class="g-route">${esc(m.from)} → ${esc(m.to)}<time>${esc(clock(m.sentAt))}</time></span><span class="g-text">${esc(m.text)}</span></div>`).join('')
    : '<p class="empty-pane">No messages yet. They show here as the sessions send them.</p>';
  const armed = closeGroupArmed === s.group;
  $('#pane').innerHTML = `<div class="g-view">
    <section>
      <div class="g-bar"><h4>Sessions</h4><button type="button" class="btn small danger${armed ? ' armed' : ''}" data-act="close-group">${armed ? `Click again to close all ${members.length}` : 'Close group'}</button></div>
      <div class="g-roster">${roster}</div>
    </section>
    <section><h4>Messages</h4><div class="g-log">${log}</div></section>
  </div>`;
}

// closeGroup closes every session of s's group, all at once, after a second click. Worktrees and branches stay.
async function closeGroup(s) {
  const id = s.group;
  if (closeGroupArmed !== id) {
    closeGroupArmed = id;
    paintGroup(s);
    setTimeout(() => {
      if (closeGroupArmed !== id) return;
      closeGroupArmed = null;
      const shown = sessions.get(selectedId);
      if (shown?.group === id && paneKey === `${shown.id}:group`) paintGroup(shown);
    }, CLOSE_ARM_MS);
    return;
  }
  closeGroupArmed = null;
  const members = [...sessions.values()].filter(m => m.group === id);
  const results = await Promise.allSettled(members.map(async m => {
    await api('DELETE', `/api/session/${encodeURIComponent(m.id)}`);
    apply({ session: m, removed: true });
  }));
  const failed = results.filter(r => r.status === 'rejected');
  if (failed.length) {
    toast(`Closed ${members.length - failed.length} of ${members.length} sessions`, errorText(failed[0].reason));
  } else {
    toast(`Closed the group: ${members.length} sessions`, 'They ended and were archived. The worktrees and branches stay as they are.');
  }
}

const clock = at => new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
