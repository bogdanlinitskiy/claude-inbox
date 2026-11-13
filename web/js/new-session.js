'use strict';

const overlay = $('#overlay');
// Multi starts one session per picked repository, as a group (docs/guide.md, "Several repositories at once").
let multi = false;
const picked = new Set();

async function openModal() {
  // A native <dialog>: the browser owns the backdrop, the focus trap, Esc, and the inertness of the page
  // behind it, and css/overlay.css animates it in and out with @starting-style.
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
  select.innerHTML = runs(repoList).map(([title, list]) => {
    const options = list.map(option).join('');
    return title ? `<optgroup label="${esc(title)}">${options}</optgroup>` : options;
  }).join('') || '<option value="">No repositories yet: run claude in one, or set roots in the config</option>';
  renderPicker();
  (multi ? $('#ns-repo-q') : select).focus();
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

// renderPicker draws the Multi list. A workspace runs across repositories, so only repositories are offered, each
// once even when it is listed under two headings. The picks stay visible as chips above the filtered list.
function renderPicker() {
  if (!repoList) return;
  const q = $('#ns-repo-q').value.trim().toLowerCase();
  const seen = new Set();
  const repos = repoList.filter(r => !r.workspace && !seen.has(r.dir) && seen.add(r.dir));
  const html = runs(repos).map(([title, list]) => {
    const hits = list.filter(r => r.name.toLowerCase().includes(q));
    if (!hits.length) return '';
    return (title ? `<div class="pick-head">${esc(title)}</div>` : '') + hits.map(r =>
      `<label class="check"><input type="checkbox" data-dir="${esc(r.dir)}"${picked.has(r.dir) ? ' checked' : ''}> ${esc(r.name)}</label>`).join('');
  }).join('');
  $('#ns-repos').innerHTML = html || `<p class="hint pick-none">${q ? `No repository matches “${esc(q)}”.` : 'No repositories yet.'}</p>`;
  const names = new Map(repos.map(r => [r.dir, r.name]));
  $('#ns-chips').innerHTML = [...picked].map(dir => `<span class="chip">${esc(names.get(dir) || dir)}<button type="button" data-unpick="${esc(dir)}" aria-label="Remove ${esc(names.get(dir) || dir)}">×</button></span>`).join('');
  $('#ns-picked').textContent = `${picked.size} of ${repos.length}`;
  renderSubmit();
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
  $('#ns-repo-field').hidden = multi;
  $('#ns-repo').required = !multi;
  $('#ns-repos-field').hidden = !multi;
  $('#ns-group-opts').hidden = !multi;
  $('#ns-hint').textContent = multi
    ? 'Starts one session per repository, all on one ticket, each told about the others and how to message them.'
    : 'Starts Claude Code in its own tmux session, in auto mode. It shows up here and alerts you when it needs you.';
  $('#ns-worktree-label').textContent = multi ? 'Each in a new git worktree, on one branch' : 'Start in a new git worktree';
  $('#ns-worktree').checked = false;
  $('#ns-branch-field').hidden = true;
  renderSubmit();
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
$('#ns-single').addEventListener('click', () => setMode(false));
$('#ns-multi').addEventListener('click', () => { setMode(true); $('#ns-repo-q').focus(); });
$('#ns-worktree').addEventListener('change', e => {
  $('#ns-branch-field').hidden = !e.target.checked;
  if (e.target.checked) $('#ns-branch').focus();
});
$('#ns-coordinator').addEventListener('change', renderSubmit);
$('#ns-repo-q').addEventListener('input', renderPicker);
$('#ns-repos').addEventListener('change', e => {
  const dir = e.target.dataset.dir;
  if (dir === undefined) return;
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
      started = [await api('POST', '/api/sessions', { ...common, dir: $('#ns-repo').value })];
    }
    closeModal();
    $('#new-form').reset();
    picked.clear();
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
