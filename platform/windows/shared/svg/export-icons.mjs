import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const source = path.dirname(fileURLToPath(import.meta.url));
const destination = path.resolve(source, '..');
const sizes = [16, 20, 24, 32, 48, 256];
const icons = fs.readdirSync(source)
  .filter((name) => name.endsWith('.svg') && name !== 'lore-glyph.svg')
  .map((name) => path.basename(name, '.svg'))
  .sort();
const edge = [
  process.env.LORE_EDGE,
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
].filter(Boolean).find((candidate) => fs.existsSync(candidate));
if (!edge) throw new Error('Microsoft Edge was not found. Set LORE_EDGE to msedge.exe.');

const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'lore-icon-export-'));
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function debuggerPort() {
  const file = path.join(temporary, 'DevToolsActivePort');
  for (let attempt = 0; attempt < 100; attempt++) {
    if (fs.existsSync(file)) return Number(fs.readFileSync(file, 'utf8').split(/\r?\n/)[0]);
    await delay(100);
  }
  throw new Error('Edge did not open its DevTools port.');
}

async function connect(url) {
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });
  let sequence = 0;
  const pending = new Map();
  const events = new Map();
  socket.addEventListener('message', ({ data }) => {
    const message = JSON.parse(data);
    if (message.id) {
      const request = pending.get(message.id);
      pending.delete(message.id);
      if (message.error) request?.reject(new Error(message.error.message));
      else request?.resolve(message.result);
    } else if (events.has(message.method)) {
      events.get(message.method)(message.params);
      events.delete(message.method);
    }
  });
  return {
    call(method, params = {}) {
      return new Promise((resolve, reject) => {
        const id = ++sequence;
        pending.set(id, { resolve, reject });
        socket.send(JSON.stringify({ id, method, params }));
      });
    },
    wait(method) {
      return new Promise((resolve) => events.set(method, resolve));
    },
    close() {
      socket.close();
    },
  };
}

function validatePng(data, size, name) {
  if (data.length < 33 || data.readUInt32BE(0) !== 0x89504e47 ||
      data.readUInt32BE(16) !== size || data.readUInt32BE(20) !== size || data[25] !== 6) {
    throw new Error(`Invalid RGBA PNG export: ${name} ${size}px`);
  }
  return data;
}

function writeIco(name) {
  const images = sizes.map((size) => validatePng(
    fs.readFileSync(path.join(destination, 'png', String(size), `${name}.png`)), size, name));
  const header = Buffer.alloc(6 + images.length * 16);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(images.length, 4);
  let offset = header.length;
  images.forEach((data, index) => {
    const entry = 6 + index * 16;
    const size = sizes[index];
    header[entry] = size === 256 ? 0 : size;
    header[entry + 1] = size === 256 ? 0 : size;
    header.writeUInt16LE(1, entry + 4);
    header.writeUInt16LE(32, entry + 6);
    header.writeUInt32LE(data.length, entry + 8);
    header.writeUInt32LE(offset, entry + 12);
    offset += data.length;
  });
  const directory = path.join(destination, 'ico');
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, `${name}.ico`), Buffer.concat([header, ...images]));
}

const browser = spawn(edge, [
  '--headless', '--disable-gpu', '--hide-scrollbars', '--no-first-run',
  '--remote-debugging-port=0', `--user-data-dir=${temporary}`, 'about:blank',
], { stdio: 'ignore', windowsHide: true });
let page;
try {
  const port = await debuggerPort();
  const targets = await fetch(`http://127.0.0.1:${port}/json/list`).then((response) => response.json());
  page = await connect(targets.find((target) => target.type === 'page').webSocketDebuggerUrl);
  await page.call('Page.enable');
  await page.call('Emulation.setDefaultBackgroundColorOverride', { color: { r: 0, g: 0, b: 0, a: 0 } });
  for (const name of icons) {
    const svg = fs.readFileSync(path.join(source, `${name}.svg`), 'utf8');
    for (const size of sizes) {
      await page.call('Emulation.setDeviceMetricsOverride', {
        width: size, height: size, deviceScaleFactor: 1, mobile: false,
      });
      const html = `<!doctype html><style>html,body{margin:0;background:transparent;overflow:hidden}svg{display:block;width:${size}px;height:${size}px}</style>${svg}`;
      const loaded = page.wait('Page.loadEventFired');
      await page.call('Page.navigate', { url: `data:text/html;base64,${Buffer.from(html).toString('base64')}` });
      await loaded;
      const screenshot = await page.call('Page.captureScreenshot', {
        format: 'png', fromSurface: true, captureBeyondViewport: false,
        clip: { x: 0, y: 0, width: size, height: size, scale: 1 },
      });
      const data = validatePng(Buffer.from(screenshot.data, 'base64'), size, name);
      const directory = path.join(destination, 'png', String(size));
      fs.mkdirSync(directory, { recursive: true });
      fs.writeFileSync(path.join(directory, `${name}.png`), data);
    }
    writeIco(name);
  }
  console.log(`Exported ${icons.length} ICO files and ${icons.length * sizes.length} PNG files.`);
} finally {
  if (page) {
    try { await Promise.race([page.call('Browser.close'), delay(1000)]); } catch {}
    page.close();
  }
  if (browser.exitCode === null) browser.kill();
  fs.rmSync(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
}
