'use strict';

const overlay = $('#overlay');
// Multi starts one session per picked repository, as a group (docs/guide.md, "Several repositories at once").
let multi = false;
const picked = new Set();
// Single's repository, a radio in the same in-page list: a native <select> opens an OS popup that can land
// under the cursor while it animates in, and the mouse-up then picks whatever row is there.
let chosen = '';

async function openModal() {
  // A native <dialog>: the browser owns the backdrop, the focus trap, Esc, and the inertness of the page
  // behind it, and css/overlay.css animates it in and out with @starting-style.
  overlay.showModal();
  $('#ns-error').hidden = true;
  if (!repoList) {
    $('#ns-repos').innerHTML = '<p class="hint pick-none">Loading…</p>';
    try {
      repoList = await api('GET', '/api/repos');
    } catch (err) {
      $('#ns-repos').innerHTML = '';
      showFormError(errorText(err));
      return;
    }
  }
  renderPicker();
  $('#ns-repo-q').focus();
}

// runs splits the list into its runs of one group, in the order the server sends: each run is one heading.
function runs(list) {
  const out = [];
  for (let i = 0; i < list.length;) {
    const title = list[i].group || '';
    let end = i;
    while (end < list.length && (list[end].group || '') === title) end++;
    out.push([title, list.slice(i, end)]);
    i = end;
  }
  return out;
}

// renderPicker draws the repository list: radios for Single, checkboxes for Multi. A workspace runs across
// repositories, so Multi offers only repositories. Each is listed once even when it is under two headings. The
// Multi picks stay visible as chips above the filtered list; Single's pick is named next to the heading.
function renderPicker() {
  renderSubmit();
  if (!repoList) return;
  const q = $('#ns-repo-q').value.trim().toLowerCase();
  const seen = new Set();
  const repos = repoList.filter(r => (!multi || !r.workspace) && !seen.has(r.dir) && seen.add(r.dir));
  if (!multi && !repos.some(r => r.dir === chosen)) chosen = repos[0]?.dir || '';
  const input = r => multi
    ? `<input type="checkbox" data-dir="${esc(r.dir)}"${picked.has(r.dir) ? ' checked' : ''}>`
    : `<input type="radio" name="ns-dir" data-dir="${esc(r.dir)}"${r.dir === chosen ? ' checked' : ''}>`;
  const html = runs(repos).map(([title, list]) => {
    const hits = list.filter(r => r.name.toLowerCase().includes(q));
    if (!hits.length) return '';
    return (title ? `<div class="pick-head">${esc(title)}</div>` : '') + hits.map(r =>
      `<label class="check">${input(r)} ${esc(r.name)}</label>`).join('');
  }).join('');
  $('#ns-repos').innerHTML = html || `<p class="hint pick-none">${q ? `No repository matches “${esc(q)}”.` : 'No repositories yet: run claude in one, or set roots in the config.'}</p>`;
  const names = new Map(repos.map(r => [r.dir, r.name]));
  $('#ns-chips').innerHTML = !multi ? '' : [...picked].map(dir => `<span class="chip">${esc(names.get(dir) || dir)}<button type="button" data-unpick="${esc(dir)}" aria-label="Remove ${esc(names.get(dir) || dir)}">×</button></span>`).join('');
  $('#ns-picked').textContent = multi ? `${picked.size} of ${repos.length}` : names.get(chosen) || '';
}

function renderSubmit() {
  const n = picked.size + ($('#ns-coordinator').checked ? 1 : 0);
  $('#ns-submit').textContent = multi ? `Start ${n} session${n === 1 ? '' : 's'}` : 'Start session';
  $('#ns-prompt-label').textContent = !multi ? 'First message' : $('#ns-coordinator').checked ? 'Task for the coordinator' : 'First message for every session';
}

// setMode switches the form between one session and a group. Both start in the repository folders unless the
// worktree box is ticked.
function setMode(toMulti) {
  multi = toMulti;
  $('#ns-single').setAttribute('aria-selected', String(!multi));
  $('#ns-multi').setAttribute('aria-selected', String(multi));
  $('#ns-repos-label').firstChild.data = multi ? 'Repositories, one session each ' : 'Repository ';
  $('#ns-group-opts').hidden = !multi;
  $('#ns-hint').textContent = multi
    ? 'Starts one session per repository, all on one ticket, each told about the others and how to message them.'
    : 'Starts Claude Code in its own tmux session, in auto mode. It shows up here and alerts you when it needs you.';
  $('#ns-worktree-label').textContent = multi ? 'Each in a new git worktree, on one branch' : 'Start in a new git worktree';
  $('#ns-worktree').checked = false;
  $('#ns-branch-field').hidden = true;
  renderPicker();
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

$('#new-session').addEventListener('click', openModal);
$('#ns-single').addEventListener('click', () => { setMode(false); $('#ns-repo-q').focus(); });
$('#ns-multi').addEventListener('click', () => { setMode(true); $('#ns-repo-q').focus(); });
$('#ns-worktree').addEventListener('change', e => {
  $('#ns-branch-field').hidden = !e.target.checked;
  if (e.target.checked) $('#ns-branch').focus();
});
$('#ns-coordinator').addEventListener('change', renderSubmit);
$('#ns-repo-q').addEventListener('input', renderPicker);
// In Single, Enter in the filter picks the first match instead of starting a session in the old pick.
$('#ns-repo-q').addEventListener('keydown', e => {
  if (multi || e.key !== 'Enter') return;
  e.preventDefault();
  $('#ns-repos input')?.click();
});
$('#ns-repos').addEventListener('change', e => {
  const dir = e.target.dataset.dir;
  if (dir === undefined) return;
  if (!multi) {
    // No re-render: it would drop the focus the arrow keys move between the radios.
    chosen = dir;
    $('#ns-picked').textContent = e.target.parentElement.textContent.trim();
    return;
  }
  if (e.target.checked) picked.add(dir); else picked.delete(dir);
  renderPicker();
});
$('#ns-chips').addEventListener('click', e => {
  const dir = e.target.dataset.unpick;
  if (dir === undefined) return;
  picked.delete(dir);
  renderPicker();
});
$('#ns-cancel').addEventListener('click', closeModal);
overlay.addEventListener('click', e => { if (e.target === overlay) closeModal(); });

$('#new-form').addEventListener('submit', async e => {
  e.preventDefault();
  const submit = $('#ns-submit');
  submit.disabled = true;
  const common = {
    ticket: $('#ns-ticket').value.trim(),
    title: $('#ns-title').value.trim(),
    prompt: $('#ns-prompt').value.trim(),
    worktree: $('#ns-worktree').checked,
    branch: $('#ns-branch').value.trim(),
  };
  try {
    let started;
    if (multi) {
      if (!picked.size) throw new Error('Pick at least one repository.');
      started = await api('POST', '/api/groups', {
        ...common, dirs: [...picked], coordinator: $('#ns-coordinator').checked, shareDirs: $('#ns-share').checked,
      });
    } else {
      if (!chosen) throw new Error('Pick a repository.');
      started = [await api('POST', '/api/sessions', { ...common, dir: chosen })];
    }
    closeModal();
    $('#new-form').reset();
    picked.clear();
    chosen = '';
    setMode(multi);
    for (const info of started) apply({ session: info, live: false });
    select(started[0].id);
    toast(started.length === 1 ? `Started ${started[0].tmuxName}` : `Started ${started.length} sessions`,
      'Running in tmux. It shows up here when it needs you.');
  } catch (err) {
    showFormError(errorText(err));
  } finally {
    submit.disabled = false;
  }
});
