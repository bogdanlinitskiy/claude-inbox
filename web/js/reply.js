'use strict';

// renderReply redraws the answer panel only when it changed: every update of any session renders, and a redraw
// would move the caret of the reply being typed. A redraw that keeps the text keeps the caret too.
function renderReply(s) {
  const html = actionBarHtml(s) + replyHtml(s);
  const key = `${s.id}\n${html}`;
  if (key === replyKey) return;
  replyKey = key;
  const reply = $('#reply');
  const old = $('#reply-input', reply);
  const hadFocus = document.activeElement === old;
  const caret = old && { value: old.value, start: old.selectionStart, end: old.selectionEnd, scroll: old.scrollTop };
  reply.innerHTML = html;
  fitBar();
  const input = $('#reply-input', reply);
  if (!input) return;
  input.value = drafts.get(s.id) || '';
  if (!hadFocus) return;
  input.focus();
  if (caret.value === input.value) {
    input.setSelectionRange(caret.start, caret.end);
    input.scrollTop = caret.scroll;
  }
}

// A free-text answer to a question goes into a one-line field on Claude's screen, so it cannot take line breaks.
function sendForm(placeholder, withInterrupt, multiline = true) {
  return `<form class="send" id="send-form" autocomplete="off">
    <label class="sr" for="reply-input">Reply to this session</label>
    <textarea id="reply-input" rows="1" ${multiline ? '' : 'data-single-line'} placeholder="${esc(placeholder)}"></textarea>
    <button class="btn primary" type="submit">Send</button>
    ${withInterrupt ? '<button class="btn" type="button" data-act="interrupt">Interrupt</button>' : ''}
  </form>`;
}

function deliveryHtml(s) {
  if (s.delivery === 'pending') return '<div class="delivery">Sending… waiting for Claude to react.</div>';
  if (locked(s) && s.delivery !== 'failed') return '<div class="delivery">Nothing to answer on the screen: the answer most likely reached Claude already. The buttons come back when it asks again.</div>';
  if (s.delivery === 'failed') return '<div class="delivery failed">Not sent. Claude did not react; check the terminal.</div>';
  return '';
}

// The ask header reads as a sentence: who is asking, then the tool as a quiet mono tag, then how long it
// has been waiting — the first thing you want when you come back to the screen. askOf is the session the
// panel is being built for; replyHtml and viewOnlyHtml set it, so every ask() gets the elapsed label
// without threading it through a dozen call sites.
let askOf = null;

function ask(kindClass, label, body) {
  const [who, ...rest] = String(label).split(' · ');
  const tag = rest.join(' · ');
  const ago = askOf ? `<span class="ago">${esc(SINCE_LABEL[askOf.state.kind] || 'waiting')} ${since(askOf.state.changedAt)}</span>` : '';
  const tool = tag ? `<span class="tool">${esc(tag)}</span>` : '';
  return `<div class="ask ${kindClass}"><div class="ask-head"><span class="who">${esc(who)}</span>${tool}${ago}</div>${body}</div>`;
}

// locked: an answer to this state is on its way, or found nothing left to answer; a second click would land in
// Claude's prompt as text.
function locked(s) {
  return s.delivery === 'pending' || nothingOnScreen.get(s.id) === s.state.seq;
}

const off = s => (locked(s) ? ' disabled' : '');

function optionButtons(s, options) {
  return `<div class="opts">${options.map((o, i) => `<button type="button" class="opt" data-opt="${i}"${off(s)}><kbd>${i + 1}</kbd>${esc(o.label)}${o.description ? `<span class="note">${esc(o.description)}</span>` : ''}</button>`).join('')}</div>`;
}

// viewOnlyHtml shows what a session outside tmux waits on; the app cannot type into it.
function viewOnlyHtml(s) {
  askOf = s;
  const st = s.state;
  const note = '<p class="ask-note">This session does not run in tmux, so the app cannot type into it. Answer it where it runs.</p>';
  switch (st.kind) {
    case 'question': {
      const q = st.questions[0];
      const options = `<ol class="opt-list">${q.options.map(o => `<li>${esc(o.label)}${o.description ? `<span class="note">${esc(o.description)}</span>` : ''}</li>`).join('')}</ol>`;
      return ask('k-question', q.header ? `Claude is asking · ${q.header}` : 'Claude is asking', `<p class="ask-text">${esc(q.question)}</p>${options}${note}`);
    }
    case 'permission': {
      const p = st.permission || {};
      return ask('k-permission', `Permission needed · ${p.tool || ''}`, `${p.description ? `<p class="ask-text">${esc(p.description)}</p>` : ''}<pre class="cmd">${esc(p.detail)}</pre>${note}`);
    }
    case 'unknown':
      return ask('k-unknown', 'Check the session', `<p class="ask-text">The session waits on something the app does not recognize.</p>${note}`);
    case 'done':
      return ask(`k-done${st.failed ? ' failed' : ''}`, st.failed ? 'Stopped with an error' : 'Finished', `<p class="ask-text">${esc(st.lastMessage || '(no message)')}</p>${note}`);
    default:
      return note;
  }
}

function replyHtml(s) {
  askOf = s;
  if (!s.inTmux) return viewOnlyHtml(s);
  const st = s.state;
  const delivery = deliveryHtml(s);
  switch (st.kind) {
    case 'question': {
      const q = st.questions[0];
      if (!st.canAnswer) {
        return ask('k-unknown', 'Answer in the terminal', `<p class="ask-text">This question has several parts or allows several answers. Answer it in the Terminal tab.</p>`) + delivery;
      }
      if (st.fromScreen) {
        return ask('k-question', 'Claude Code is asking · read from the screen', `<p class="ask-text">${esc(q.question)}</p>${optionButtons(s, q.options)}
          <p class="ask-note">No hook reported this; the app read it from the terminal. Pick an entry, or answer in the Terminal tab.</p>`) + delivery;
      }
      return ask('k-question', q.header ? `Claude is asking · ${q.header}` : 'Claude is asking', `<p class="ask-text">${esc(q.question)}</p>${optionButtons(s, q.options)}`)
        + delivery + sendForm('Or type your own answer…', false, false);
    }
    case 'permission': {
      const p = st.permission || {};
      return ask('k-permission', `Permission needed · ${p.tool || ''}`,
        `${p.description ? `<p class="ask-text">${esc(p.description)}</p>` : ''}<pre class="cmd">${esc(p.detail)}</pre>
         <div class="opts"><button type="button" class="opt" data-allow="true"${off(s)}>Allow</button><button type="button" class="opt" data-allow="false"${off(s)}>Deny</button></div>`)
        + delivery + sendForm('Or deny and tell it what to do instead…', false);
    }
    case 'trust':
      return ask('k-trust', 'Trust this folder?',
        `<p class="ask-text">Claude Code has not worked in ${esc(s.dir)} before and asks whether to trust it. It can read, edit and run files there.</p>
         <div class="opts"><button type="button" class="opt" data-trust="true"${off(s)}>Trust this folder</button><button type="button" class="opt" data-trust="false"${off(s)}>Don't trust (Claude exits)</button></div>`) + delivery;
    case 'unknown':
      return ask('k-unknown', 'Check the terminal', '<p class="ask-text">The session waits on something the app does not recognize. Answer it in the Terminal tab.</p>');
    case 'working':
      return delivery + sendForm('Message this session. It reads it after the current step.', true);
    case 'done':
      return delivery + sendForm('Ask for something else…', false);
    case 'idle':
      return delivery + sendForm('Type a prompt…', false);
    default:
      return '';
  }
}

// One send per session at a time, so a second Enter or click before the first returns does not send it twice.
async function sendOnce(s, send) {
  if (sending.has(s.id)) return;
  sending.add(s.id);
  try {
    await send();
  } finally {
    sending.delete(s.id);
  }
}

function sent(s, info) {
  drafts.delete(s.id);
  if (s.id === selectedId) replyKey = '';
  apply({ session: info, live: false });
}

function sendAnswer(s, body) {
  return sendOnce(s, async () => {
    try {
      sent(s, await api('POST', `/api/session/${encodeURIComponent(s.id)}/answer`, { seq: s.state.seq, ...body }));
    } catch (err) {
      if (err.code === NOTHING_ON_SCREEN) {
        nothingOnScreen.set(s.id, s.state.seq);
        if (s.id === selectedId) renderDetail();
      } else {
        toast('Answer not sent', errorText(err));
      }
      refresh(s.id);
    }
  });
}

function sendMessage(s, text) {
  return sendOnce(s, async () => {
    try {
      sent(s, await api('POST', `/api/session/${encodeURIComponent(s.id)}/message`, { text }));
    } catch (err) {
      toast('Message not sent', errorText(err));
    }
  });
}

async function refresh(id) {
  try {
    apply({ session: await api('GET', `/api/session/${encodeURIComponent(id)}`), live: false });
  } catch (err) {
    if (err.status === 404) apply({ session: { id }, removed: true });
  }
}

function submitText(s, text) {
  if (!text.trim()) return;
  if (s.state.kind === 'question' || s.state.kind === 'permission') {
    sendAnswer(s, { text });
  } else {
    sendMessage(s, text);
  }
}

function pickOption(s, i) {
  if (!s.inTmux || s.state.kind !== 'question' || !s.state.canAnswer || !s.state.questions[0].options[i]) return;
  sendAnswer(s, { option: i });
}
