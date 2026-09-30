'use strict';

// The notebook belongs to the server. This page holds only what has not been
// saved yet (drafts), what is running, and what went wrong.
let nb = null;                // the notebook as the server last sent it
const drafts = new Map();     // cell index -> {name, expr}: typed, not yet saved
const errors = new Map();     // cell index -> what its last run said
let editing = null;           // index of the text being edited
let pending = null;           // {index, type}: being added, not yet in the notebook
let busy = null;              // {index, controller, message} while a cell runs
let connected = false;
let focusRequest = null;      // {seg, field}: where to put the cursor after the next render

const root = document.getElementById('notebook');
const $ = (id) => document.getElementById(id);

// h builds an element. Text goes in as text; only the server's own rendering of
// the notebook's prose is ever set as HTML.
function h(tag, props = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'text') el.textContent = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k in el && k !== 'list') el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid === null || kid === undefined || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

async function api(method, path, body, signal) {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-Mote': '1' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  let data = {};
  try { data = await res.json(); } catch (_) { /* a reply with no body */ }
  return { status: res.status, data };
}

function say(message) {
  const n = $('notice');
  n.replaceChildren();
  if (message) n.append(message + ' ', h('button', { type: 'button', class: 'icon', text: 'Dismiss', onclick: () => say('') }));
  n.hidden = !message;
}

async function load() {
  const r = await api('GET', '/api/notebook');
  if (r.status !== 200) {
    say(r.data.error || 'The notebook could not be loaded (status ' + r.status + ').');
    return;
  }
  nb = r.data;
  if (nb.warning) say(nb.warning);
  render();
}

// ---- drawing --------------------------------------------------------------

// codeEditor is a textarea over a copy of its text that is coloured. The text
// in the textarea is invisible and the copy shows through it; both wrap the
// same way, and the copy is what gives the editor its height, so they stay
// together without measuring anything.
function codeEditor(props, initial, oninput) {
  const layer = h('pre', { class: 'hl', 'aria-hidden': 'true' });
  const ta = h('textarea', { ...props, class: 'code', value: initial, spellcheck: false, rows: 1 });
  const paint = () => {
    const nodes = tokenize(ta.value).map((t) => (t.cls ? h('span', { class: 'tk-' + t.cls, text: t.text }) : document.createTextNode(t.text)));
    // A last empty line has no height in a pre unless something is on it.
    layer.replaceChildren(...nodes, document.createTextNode('\u200b'));
  };
  ta.addEventListener('input', () => { paint(); if (oninput) oninput(); });
  paint();
  return { el: h('div', { class: 'editor' }, layer, ta), ta };
}

function rememberFocus() {
  const el = document.activeElement;
  const seg = el && el.closest ? el.closest('[data-seg]') : null;
  if (!seg || !el.dataset || !el.dataset.field) return null;
  return { seg: seg.dataset.seg, field: el.dataset.field, start: el.selectionStart, end: el.selectionEnd };
}

function restoreFocus(f) {
  f = focusRequest || f;
  focusRequest = null;
  if (!f) return;
  const el = root.querySelector('[data-seg="' + f.seg + '"] [data-field="' + f.field + '"]');
  if (!el) return;
  el.focus();
  if (f.start !== undefined && f.start !== null && el.setSelectionRange) el.setSelectionRange(f.start, f.end);
}

const stateText = { new: 'not run', fresh: 'up to date', stale: 'out of date', always: 'runs every time' };

function render() {
  if (!nb) return;
  const focus = rememberFocus();
  document.title = nb.name + ' — mote';
  $('title').textContent = nb.name;
  const parts = [];
  if (nb.segments.length === 0 && !pending) {
    parts.push(h('p', { class: 'empty', text: 'This notebook is empty. Add a cell or some text.' }));
  }
  nb.segments.forEach((seg, i) => {
    parts.push(slot(i));
    parts.push(seg.type === 'cell' ? cellView(seg, i) : proseView(seg, i));
  });
  parts.push(slot(nb.segments.length, true));
  root.replaceChildren(...parts);
  restoreFocus(focus);
  updateChrome();
}

function updateChrome() {
  const cells = nb ? nb.segments.some((s) => s.type === 'cell') : false;
  $('run-all').disabled = !!busy || !cells;
  $('stop').hidden = !busy;
  const c = $('conn');
  c.className = 'conn ' + (connected ? 'ok' : 'bad');
  c.textContent = busy ? 'running cell ' + cellNumber(busy.index) + (busy.message ? ': ' + busy.message : '…')
    : connected ? 'connected' : 'reconnecting…';
}

function cellNumber(i) {
  return nb && nb.segments[i] ? nb.segments[i].number : '';
}

// slot is the place between two segments: a strip to add to, or the editor for
// what is being added there.
function slot(i, last) {
  if (pending && pending.index === i) return pendingEditor();
  return h('div', { class: 'adder' + (last ? ' last' : '') },
    h('button', { type: 'button', text: '+ cell', 'aria-label': 'Add a cell here', onclick: () => startAdding(i, 'cell') }),
    h('button', { type: 'button', text: '+ text', 'aria-label': 'Add text here', onclick: () => startAdding(i, 'prose') }));
}

function startAdding(i, type) {
  pending = { index: i, type };
  focusRequest = { seg: 'new', field: 'expr' };
  render();
}

function pendingEditor() {
  const isCell = pending.type === 'cell';
  const onkeydown = (e) => {
    if (e.key === 'Escape') { pending = null; render(); }
    else if (isCell && e.key === 'Enter' && (e.shiftKey || e.ctrlKey || e.metaKey)) { e.preventDefault(); add(true); }
  };
  const expr = isCell
    ? codeEditor({ 'data-field': 'expr', placeholder: 'chat "what is the capital of France"', 'aria-label': 'The new cell', onkeydown }, '').el
    : h('textarea', { class: 'text', rows: 4, 'data-field': 'expr', placeholder: 'Some text, in Markdown', 'aria-label': 'The new text', onkeydown });
  const name = isCell ? h('input', { class: 'name', placeholder: 'name', 'data-field': 'name', 'aria-label': 'Name for its output' }) : null;
  return h('div', { class: 'seg pending', 'data-seg': 'new' },
    isCell ? h('div', { class: 'cell-bar' }, h('span', { class: 'prompt', text: 'new cell' }), name) : null,
    expr,
    h('div', { class: 'row' },
      isCell ? h('button', { type: 'button', class: 'primary', text: 'Add and run', onclick: () => add(true) }) : null,
      h('button', { type: 'button', class: isCell ? '' : 'primary', text: 'Add', onclick: () => add(false) }),
      h('button', { type: 'button', text: 'Cancel', onclick: () => { pending = null; render(); } })));
}

function proseView(seg, i) {
  if (editing === i) {
    const ta = h('textarea', {
      class: 'text', 'data-field': 'expr', value: seg.markdown, rows: Math.max(4, seg.markdown.split('\n').length + 1),
      'aria-label': 'The text, in Markdown',
      onkeydown: (e) => {
        if (e.key === 'Escape') { editing = null; render(); }
        else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); saveProse(i, ta.value); }
      },
    });
    return h('section', { class: 'seg', 'data-seg': i }, ta,
      h('div', { class: 'row prose-tools' },
        h('button', { type: 'button', class: 'primary', text: 'Save', onclick: () => saveProse(i, ta.value) }),
        h('button', { type: 'button', text: 'Cancel', onclick: () => { editing = null; render(); } })));
  }
  const body = h('div', { class: 'prose' });
  body.innerHTML = seg.html; // rendered by the server, which drops anything that could run
  return h('section', { class: 'seg', 'data-seg': i }, body, tools(i, [
    h('button', { type: 'button', class: 'icon', text: 'Edit', onclick: () => { editing = i; focusRequest = { seg: String(i), field: 'expr' }; render(); } }),
  ], 'text'));
}

function tools(i, extra, what) {
  const last = nb.segments.length - 1;
  return h('div', { class: 'prose-tools cell-tools' }, extra,
    h('button', { type: 'button', class: 'icon', text: '↑', title: 'Move up', 'aria-label': 'Move ' + what + ' up', disabled: i === 0 || !!busy, onclick: () => move(i, i - 1) }),
    h('button', { type: 'button', class: 'icon', text: '↓', title: 'Move down', 'aria-label': 'Move ' + what + ' down', disabled: i === last || !!busy, onclick: () => move(i, i + 1) }),
    h('button', { type: 'button', class: 'icon danger', text: 'Delete', 'aria-label': 'Delete ' + what, disabled: !!busy, onclick: () => remove(i, what) }));
}

function cellView(seg, i) {
  const draft = drafts.get(i);
  const isBusy = busy && busy.index === i;
  const editor = codeEditor({
    'data-field': 'expr',
    'aria-label': 'Cell ' + seg.number,
    onkeydown: (e) => {
      if (e.key === 'Enter' && (e.shiftKey || e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        run(i, { next: e.shiftKey });
      }
    },
  }, draft ? draft.expr : seg.expr, () => draftChanged(i, seg));
  const name = h('input', {
    class: 'name', 'data-field': 'name', value: draft ? draft.name : seg.name, placeholder: 'name',
    'aria-label': 'Name for the output of cell ' + seg.number, oninput: () => draftChanged(i, seg),
  });
  const state = draft ? 'edited' : seg.state;
  return h('section', { class: 'seg cell state-' + seg.state + (isBusy ? ' running' : ''), 'data-seg': i },
    h('div', { class: 'cell-bar' },
      h('span', { class: 'prompt', text: 'In [' + seg.number + ']' }),
      name,
      h('span', { class: 'badge', text: draft ? 'edited' : stateText[state] || state }),
      h('span', { class: 'spacer' }),
      tools(i, [], 'cell ' + seg.number),
      h('button', { type: 'button', class: 'primary', text: '▶ Run', disabled: !!busy, 'aria-label': 'Run cell ' + seg.number, onclick: () => run(i, {}) })),
    editor.el,
    seg.problem ? h('div', { class: 'problem', text: '⚠ ' + seg.problem }) : null,
    seg.changes ? h('div', { class: 'danger-note', text: 'This cell can change things, so you are asked before it runs.' }) : null,
    isBusy ? h('div', { class: 'working', role: 'status', text: busy.message || 'starting…' }) : outputView(seg, i));
}

function draftChanged(i, seg) {
  const el = root.querySelector('[data-seg="' + i + '"]');
  const name = el.querySelector('[data-field="name"]').value;
  const expr = el.querySelector('[data-field="expr"]').value;
  if (name === seg.name && expr === seg.expr) drafts.delete(i);
  else drafts.set(i, { name, expr });
  const badge = el.querySelector('.badge');
  badge.textContent = drafts.has(i) ? 'edited' : stateText[seg.state] || seg.state;
}

function outputView(seg, i) {
  const err = errors.get(i);
  if (err) return h('div', { class: 'output error', role: 'alert' }, h('pre', { text: err }));
  const out = seg.output;
  if (!out) return null;
  const media = (out.media || []).map((m) => {
    const el = m.kind === 'image' ? h('img', { src: m.url, alt: m.path, loading: 'lazy' })
      : m.kind === 'audio' ? h('audio', { src: m.url, controls: true, preload: 'metadata' })
      : h('video', { src: m.url, controls: true, preload: 'metadata' });
    return h('figure', { class: 'media' }, el, h('figcaption', { class: 'outside', text: m.path }));
  });
  return h('div', { class: 'output' },
    media.length ? h('div', { class: 'media' }, media) : (out.text ? h('pre', { text: out.text }) : null),
    (out.outside || []).length ? h('div', { class: 'outside', text: 'Not shown, since it is outside the served folder (see --root): ' + out.outside.join(', ') }) : null);
}

// ---- doing ----------------------------------------------------------------

// change asks the server for an edit. It says whether it happened.
async function change(req) {
  const r = await api('POST', '/api/edit', { rev: nb.rev, ...req });
  if (r.status === 200) {
    nb = r.data;
    return true;
  }
  if (r.status === 409 && r.data.notebook) {
    nb = r.data.notebook;
    say('The notebook changed elsewhere, so this page now shows the current version. Try again.');
    drafts.clear();
    errors.clear();
    render();
    return false;
  }
  say(r.data.error || 'The change could not be made (status ' + r.status + ').');
  return false;
}

// commit saves what has been typed into cells, so that what is run, moved or
// deleted is what is on the screen.
async function commit() {
  for (const [i, d] of [...drafts.entries()]) {
    if (!(await change({ op: 'set_cell', index: i, name: d.name, expr: d.expr }))) return false;
    drafts.delete(i);
    errors.delete(i);
  }
  return true;
}

async function add(andRun) {
  const p = pending;
  const el = root.querySelector('[data-seg="new"]');
  const expr = el.querySelector('[data-field="expr"]').value;
  const nameEl = el.querySelector('[data-field="name"]');
  if (!expr.trim()) { say('There is nothing to add yet.'); return; }
  if (!(await commit())) return;
  errors.clear();
  const ok = await change(p.type === 'cell'
    ? { op: 'insert', index: p.index, type: 'cell', name: nameEl ? nameEl.value.trim() : '', expr }
    : { op: 'insert', index: p.index, type: 'prose', markdown: expr });
  if (!ok) return;
  pending = null;
  render();
  if (andRun && p.type === 'cell') run(p.index, {});
}

async function saveProse(i, markdown) {
  if (await change({ op: 'set_prose', index: i, markdown })) {
    editing = null;
    render();
  }
}

async function move(from, to) {
  if (!(await commit())) return;
  errors.clear();
  if (await change({ op: 'move', index: from, to })) render();
}

async function remove(i, what) {
  if (!confirm('Delete this ' + what.replace(/ \d+$/, '') + '?')) return;
  if (!(await commit())) return;
  errors.clear();
  if (await change({ op: 'delete', index: i })) render();
}

async function run(i, opts) {
  if (busy) return;
  if (!(await commit())) return;
  const controller = new AbortController();
  busy = { index: i, controller, message: '' };
  errors.delete(i);
  render();
  try {
    const body = { rev: nb.rev, index: i, approve: false, unlessUnchanged: !!opts.skip };
    let r = await api('POST', '/api/run', body, controller.signal);
    if (r.status === 409 && r.data.approval) {
      if (!confirm('This cell can change things on your machine. Run it?')) {
        errors.set(i, 'Not run.');
        return false;
      }
      r = await api('POST', '/api/run', { ...body, approve: true }, controller.signal);
    }
    if (r.status === 200) {
      nb = r.data.notebook;
      if (opts.next) focusRequest = nextCell(i);
    } else if (r.data.notebook) {
      nb = r.data.notebook;
      errors.set(i, r.data.error || 'The cell failed.');
    } else {
      errors.set(i, r.data.error || 'The cell could not be run (status ' + r.status + ').');
    }
  } catch (e) {
    errors.set(i, e.name === 'AbortError' ? 'Stopped.' : 'Lost touch with the server: ' + e.message);
  } finally {
    busy = null;
    render();
  }
  return !errors.has(i);
}

function nextCell(i) {
  for (let j = i + 1; j < nb.segments.length; j++) {
    if (nb.segments[j].type === 'cell') return { seg: String(j), field: 'expr' };
  }
  return null;
}

async function runAll() {
  if (busy) return;
  for (let i = 0; nb && i < nb.segments.length; i++) {
    if (nb.segments[i].type !== 'cell') continue;
    if ((await run(i, { skip: true })) === false) return; // stop where it went wrong
  }
}

// ---- events ---------------------------------------------------------------

function listen() {
  const es = new EventSource('/api/events');
  es.onopen = () => { connected = true; updateChrome(); };
  es.onerror = () => { connected = false; updateChrome(); };
  es.onmessage = (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch (_) { return; }
    if (ev.type === 'status' && busy) {
      busy.message = ev.message;
      updateChrome();
      const w = root.querySelector('.working');
      if (w) w.textContent = ev.message;
    } else if (ev.type === 'changed' && !busy && nb && ev.rev !== nb.rev) {
      load(); // another page, or an editor, changed the notebook
    }
  };
}

$('run-all').addEventListener('click', runAll);
$('stop').addEventListener('click', () => { if (busy) busy.controller.abort(); });
window.addEventListener('beforeunload', (e) => {
  if (drafts.size || editing !== null || pending) e.preventDefault();
});

load().then(listen);
