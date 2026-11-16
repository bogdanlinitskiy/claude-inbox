'use strict';

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
    case 'pin':
      try {
        apply({ session: await api(s.pinned ? 'DELETE' : 'PUT', `/api/session/${encodeURIComponent(s.id)}/pin`), live: false });
      } catch (err) {
        toast(s.pinned ? 'Could not unpin' : 'Could not pin', errorText(err));
      }
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
    case 'close-group':
      closeGroup(s);
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
    case 'commit':
      openCommit(s);
      break;
    case 'push':
      pushBranch(s, target);
      break;
    case 'open-pr':
      openPR(s, target);
      break;
  }
}

$('#list').addEventListener('click', e => {
  const head = e.target.closest('[data-group]');
  if (head) {
    const id = head.dataset.group;
    if (!foldedGroups.delete(id)) foldedGroups.add(id);
    renderList();
    return;
  }
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
  const pick = e.target.closest('[data-pick]');
  if (pick) {
    select(pick.dataset.pick);
    return;
  }
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

// The reply box grows with its text through field-sizing: content in css/answer.css, so nothing measures it here.
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
