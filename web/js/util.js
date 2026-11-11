'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const short = repo => pageConfig.stripPrefix && repo.startsWith(pageConfig.stripPrefix) ? repo.slice(pageConfig.stripPrefix.length) : repo;
const narrow = matchMedia('(max-width: 880px)');
const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');

function readSetting(key, fallback) {
  try {
    const v = localStorage.getItem('inbox.' + key);
    return v === null ? fallback : JSON.parse(v);
  } catch {
    return fallback;
  }
}

function writeSetting(key, value) {
  try { localStorage.setItem('inbox.' + key, JSON.stringify(value)); } catch { /* storage blocked: the default applies next time */ }
}

const parseTime = s => (s ? Date.parse(s.replace(' ', 'T') + 'Z') : NaN);

function fmt(ms) {
  if (!Number.isFinite(ms)) return '';
  const s = Math.max(0, Math.floor(ms / SEC));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

const since = s => `<span data-since="${parseTime(s)}">${fmt(Date.now() - parseTime(s))}</span>`;

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = new Error(data?.message || `${res.status} ${res.statusText}`);
    err.status = res.status;
    err.fields = data?.errors || {};
    err.code = data?.code;
    if (res.status === 401) showBanner(err.message);
    throw err;
  }
  return data;
}

function errorText(err) {
  const field = Object.values(err.fields || {})[0];
  return field || err.message;
}

function showBanner(text) {
  const b = $('#banner');
  b.textContent = text;
  b.hidden = false;
}

function toast(title, body, id, ms = TOAST_MS) {
  const box = $('#toasts');
  const t = document.createElement(id ? 'button' : 'div');
  t.className = 'toast';
  if (id) t.type = 'button';
  t.innerHTML = `<b>${esc(title)}</b>${body ? `<span>${esc(body)}</span>` : ''}`;
  if (id) t.addEventListener('click', () => { if (sessions.has(id)) select(id); t.remove(); });
  box.appendChild(t);
  setTimeout(() => t.remove(), ms);
  while (box.children.length > MAX_TOASTS) box.firstElementChild.remove();
}
