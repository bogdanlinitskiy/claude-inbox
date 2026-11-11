'use strict';

const overlay = $('#overlay');

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
