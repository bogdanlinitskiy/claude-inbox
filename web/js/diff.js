'use strict';

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
  shownDiff = { id: s.id, uncommitted: d.uncommitted || [] };
  try {
    pane.innerHTML = diffHtml(d, s);
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

// gitButtons are Commit, Push and Open PR. Push and Open PR need a branch of its own: the server refuses the
// default branch and a detached HEAD, so the page does not offer them there.
function gitButtons(d, s) {
  const n = d.uncommitted?.length || 0;
  const feature = s.branch && s.branch !== d.base.replace(/^origin\//, '');
  return (n ? `<button type="button" class="btn small" data-act="commit">Commit ${n} ${n === 1 ? 'file' : 'files'}</button>` : '')
    + (feature ? '<button type="button" class="btn small" data-act="push">Push</button>' : '')
    + (feature && pageConfig.pr ? '<button type="button" class="btn small" data-act="open-pr">Open PR</button>' : '');
}

function diffHtml(d, s) {
  d.files = d.files || [];
  const add = d.files.reduce((n, f) => n + f.added, 0);
  const del = d.files.reduce((n, f) => n + f.deleted, 0);
  const head = `<div class="diff-head"><span>Against <span class="mono">${esc(d.base)}</span></span><span><span class="add">+${add}</span> <span class="del">−${del}</span> in ${d.files.length} files</span>
    ${d.truncated ? '<span>Cut short: the diff is too large to show whole.</span>' : ''}<span class="diff-acts">${gitButtons(d, s)}<button type="button" class="btn small" data-act="refresh-diff">Refresh</button></span></div>`;
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

// toggle does not bubble, hence the capture.
$('#pane').addEventListener('toggle', e => {
  const path = e.target.dataset?.path;
  if (path === undefined) return;
  if (e.target.open) openDiffFiles.add(path); else openDiffFiles.delete(path);
}, true);

// The commit dialog takes the files uncommitted when the Diff tab last loaded, and commits exactly those.
const commitDialog = $('#commit-dialog');
let committing = null;

function openCommit(s) {
  if (shownDiff?.id !== s.id || !shownDiff.uncommitted.length) return;
  committing = { s, paths: shownDiff.uncommitted };
  const n = committing.paths.length;
  $('#commit-title').textContent = `Commit ${n} ${n === 1 ? 'file' : 'files'}`;
  $('#commit-files').textContent = committing.paths.join('\n');
  $('#commit-error').hidden = true;
  $('#commit-submit').disabled = false;
  // The house subject is "<branch> Sentence".
  const message = $('#commit-message');
  message.value = s.branch ? `${s.branch} ` : '';
  commitDialog.showModal();
  message.focus();
  message.setSelectionRange(message.value.length, message.value.length);
}

$('#commit-cancel').addEventListener('click', () => commitDialog.close());

$('#commit-form').addEventListener('submit', async e => {
  e.preventDefault();
  if (!committing) return;
  const { s, paths } = committing;
  const submit = $('#commit-submit');
  submit.disabled = true;
  try {
    const r = await api('POST', `/api/session/${encodeURIComponent(s.id)}/commit`, { message: $('#commit-message').value, paths });
    commitDialog.close();
    toast(`Committed in ${short(s.repo)}`, r.output.trim().split('\n')[0]);
    if (paneKey === `${s.id}:diff`) loadDiff(s);
  } catch (err) {
    const box = $('#commit-error');
    box.textContent = errorText(err);
    box.hidden = false;
    submit.disabled = false;
  }
});

// gitRun disables the button while the request runs, so a second click does not push twice.
async function gitRun(button, run) {
  button.disabled = true;
  try {
    await run();
  } finally {
    button.disabled = false;
  }
}

function pushBranch(s, button) {
  return gitRun(button, async () => {
    try {
      const r = await api('POST', `/api/session/${encodeURIComponent(s.id)}/push`);
      toast(`Pushed ${s.branch}`, r.output.trimEnd().split('\n').slice(-RUN_TOAST_LINES).join('\n'), null, RUN_TOAST_MS);
    } catch (err) {
      toast('Push failed', errorText(err), null, RUN_TOAST_MS);
    }
  });
}

// openPR opens the tab within the click, before the request: a tab opened after an await is a popup the
// browser blocks. It gets the pull request's address once the server has pushed and found or made it.
function openPR(s, button) {
  const tab = window.open('', '_blank');
  if (tab) tab.opener = null;
  return gitRun(button, async () => {
    try {
      const r = await api('POST', `/api/session/${encodeURIComponent(s.id)}/pr`);
      if (tab) tab.location = r.url; else toast('Pull request', r.url, null, RUN_TOAST_MS);
    } catch (err) {
      tab?.close();
      toast('Could not open the pull request', errorText(err), null, RUN_TOAST_MS);
    }
  });
}
