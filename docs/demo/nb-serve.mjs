// Records docs/demo/nb-serve.gif: `mote nb serve` driven in a headless
// Chromium through the DevTools protocol, one screenshot every 200 ms, turned
// into a GIF by ffmpeg. Frames that do not change are held for at most 1.5 s,
// so waiting on a model is shortened; everything else is as it happened.
//
//   node --experimental-websocket docs/demo/nb-serve.mjs URL   (Node 22: no flag)
//
// URL is the address `mote nb serve` prints; see README.md for the notebook.
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const url = process.argv[2];
const out = process.argv[3] || 'docs/demo/nb-serve.gif';
const browser = process.env.CHROME || '/usr/bin/chromium';
const W = 1000, H = 860, HOLD = 1.5, EVERY = 200;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const profile = mkdtempSync(join(tmpdir(), 'mote-chrome-'));
const chrome = spawn(browser, ['--headless=new', '--no-sandbox', '--disable-gpu', '--hide-scrollbars',
  '--remote-debugging-port=9334', `--user-data-dir=${profile}`, `--window-size=${W},${H}`, 'about:blank'], { stdio: 'ignore' });

let ws;
for (let i = 0; i < 50 && !ws; i++) {
  try {
    const t = (await (await fetch('http://127.0.0.1:9334/json')).json()).find((x) => x.type === 'page');
    if (t) ws = new WebSocket(t.webSocketDebuggerUrl);
  } catch (_) { await sleep(200); }
}
await new Promise((r) => (ws.onopen = r));
let id = 0;
const waiting = new Map();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && waiting.has(msg.id)) { waiting.get(msg.id)(msg.result || {}); waiting.delete(msg.id); }
  if (msg.method === 'Page.javascriptDialogOpening') send('Page.handleJavaScriptDialog', { accept: true });
};
const send = (method, params = {}) => new Promise((r) => { const i = ++id; waiting.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
const ev = async (expression) => (await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true })).result?.value;
const until = async (expression, ms = 180000) => {
  for (const end = Date.now() + ms; Date.now() < end; await sleep(150)) if (await ev(expression)) return;
  throw new Error('timed out: ' + expression);
};

await send('Page.enable');
await send('Emulation.setDeviceMetricsOverride', { width: W, height: H, deviceScaleFactor: 1, mobile: false });
await send('Page.navigate', { url });
await until(`!!document.querySelector('.cell')`);
await sleep(600);

// Screenshots, taken while the steps below run.
const dir = mkdtempSync(join(tmpdir(), 'mote-frames-'));
const frames = [];
let recording = true;
const recorder = (async () => {
  let last = '';
  while (recording) {
    const at = Date.now();
    const { data } = await send('Page.captureScreenshot', { format: 'png' });
    const hash = createHash('sha1').update(data).digest('hex');
    if (hash !== last) {
      const file = join(dir, `f${String(frames.length).padStart(5, '0')}.png`);
      writeFileSync(file, Buffer.from(data, 'base64'));
      frames.push({ file, at });
      last = hash;
    }
    await sleep(Math.max(0, EVERY - (Date.now() - at)));
  }
})();

// Keep the cell that is running, or the one being written, in view.
const follow = (sel) => ev(`document.querySelector(${JSON.stringify(sel)})?.scrollIntoView({ block: 'center', behavior: 'instant' })`);
const typeSlowly = async (text) => {
  for (const ch of text) { await send('Input.insertText', { text: ch }); await sleep(95); }
};

await sleep(1500);
await ev(`document.getElementById('run-all').click()`);
for (let i = 0; i < 3; i++) {
  await until(`document.querySelectorAll('.cell')[${i}].classList.contains('running') || !!document.querySelectorAll('.cell')[${i}].querySelector('.output')`);
  await ev(`document.querySelectorAll('.cell')[${i}].scrollIntoView({ block: 'center', behavior: 'instant' })`);
  await until(`!document.querySelectorAll('.cell')[${i}].classList.contains('running') && !!document.querySelectorAll('.cell')[${i}].querySelector('.output')`);
}
await sleep(1500);

// Write a cell, coloured as it is typed, and run it.
await ev(`[...document.querySelectorAll('.adder.last button')].find(b => b.textContent === '+ cell').click()`);
await follow('[data-seg="new"]');
await ev(`document.querySelector('[data-seg="new"] textarea').focus()`);
await typeSlowly('chat "Who records the demo? Name only. {{talk}}"');
await sleep(800);
await ev(`[...document.querySelectorAll('[data-seg="new"] button')].find(b => b.textContent === 'Add and run').click()`);
await until(`document.querySelectorAll('.cell').length === 4 && document.querySelectorAll('.cell')[3].classList.contains('running')`);
await ev(`document.querySelectorAll('.cell')[3].scrollIntoView({ block: 'center', behavior: 'instant' })`);
await until(`!document.querySelectorAll('.cell')[3].classList.contains('running') && !!document.querySelectorAll('.cell')[3].querySelector('.output')`);
await sleep(2500);

recording = false;
await recorder;
ws.close();
chrome.kill();

// Each frame shows until the next, but a still one no longer than HOLD.
const list = frames.map((f, i) => {
  const next = i + 1 < frames.length ? frames[i + 1].at : f.at + 2500;
  return `file '${f.file}'\nduration ${Math.min((next - f.at) / 1000, HOLD).toFixed(3)}`;
});
list.push(`file '${frames[frames.length - 1].file}'`);
writeFileSync(join(dir, 'list.txt'), list.join('\n') + '\n');
execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'concat', '-safe', '0', '-i', join(dir, 'list.txt'),
  '-vf', 'fps=8,scale=880:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff:max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle',
  out], { stdio: 'inherit' });
rmSync(dir, { recursive: true, force: true });
rmSync(profile, { recursive: true, force: true });
console.log(`${out}: ${frames.length} frames`);
process.exit(0);
