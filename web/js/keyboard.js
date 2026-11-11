'use strict';

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
