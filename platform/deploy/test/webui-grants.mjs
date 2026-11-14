// Checks the repository Access tab through the real WebUI in a headless Chromium-family browser
// (Chrome, Chromium or Edge), the way an owner uses it: find the user in the picker and grant
// Read, change it to Write in the list, revoke it with ×, undo the revoke, revoke again. After
// each action the grant on the server is read back, and any error toast or script error fails
// the check. The API calls in e2e.sh can't catch a WebUI that sends a request the server
// refuses (a revoke once failed with "Request body is invalid" this way).
//
// Needs Node 22 or later (built-in WebSocket) and no packages. Called by e2e.sh:
//   node webui-grants.mjs   with LORE_URL, LORE_ADMIN_USER, LORE_ADMIN_PASSWORD, REPO_ID,
//   SUBJECT_ID, SUBJECT_EMAIL and optionally LORE_E2E_BROWSER (the browser executable).
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const need = (name) => process.env[name] || fail(`${name} isn't set`);
function fail(message) {
  console.error(`     FAIL: ${message}`);
  process.exit(1);
}
const base = need("LORE_URL").replace(/\/$/, "");
const adminUser = need("LORE_ADMIN_USER");
const adminPassword = need("LORE_ADMIN_PASSWORD");
const repoId = need("REPO_ID");
const subjectId = need("SUBJECT_ID");
const subjectEmail = need("SUBJECT_EMAIL");

function findBrowser() {
  if (process.env.LORE_E2E_BROWSER) return process.env.LORE_E2E_BROWSER;
  const candidates = process.platform === "win32"
    ? [`${process.env["ProgramFiles(x86)"]}\\Microsoft\\Edge\\Application\\msedge.exe`, `${process.env.ProgramFiles}\\Microsoft\\Edge\\Application\\msedge.exe`,
      `${process.env.ProgramFiles}\\Google\\Chrome\\Application\\chrome.exe`]
    : ["/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/microsoft-edge",
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"];
  return candidates.find((file) => file && fs.existsSync(file));
}

const browserPath = findBrowser() || fail("no Chrome, Chromium or Edge found; set LORE_E2E_BROWSER");
const profile = fs.mkdtempSync(path.join(os.tmpdir(), "lore-e2e-browser-"));
const port = 9400 + Math.floor(Math.random() * 400);
const browser = spawn(browserPath, ["--headless=new", "--disable-gpu", "--no-sandbox", "--no-first-run", `--remote-debugging-port=${port}`,
  `--user-data-dir=${profile}`, "--window-size=1440,1000", "about:blank"], { stdio: "ignore" });
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
let socket;
process.on("exit", () => {
  socket?.close();
  browser.kill();
  try { fs.rmSync(profile, { recursive: true, force: true }); } catch { /* the browser may still hold it */ }
});

for (let attempt = 0; attempt < 60 && !socket; attempt++) {
  try {
    const targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
    const page = targets.find((target) => target.type === "page");
    if (page) socket = new WebSocket(page.webSocketDebuggerUrl);
  } catch { await wait(500); }
}
if (!socket) fail(`the browser (${browserPath}) didn't start`);
await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });

let nextId = 0;
const pending = new Map();
const scriptErrors = [];
socket.onmessage = (message) => {
  const data = JSON.parse(message.data);
  if (data.id && pending.has(data.id)) { pending.get(data.id)(data); pending.delete(data.id); }
  if (data.method === "Runtime.exceptionThrown") scriptErrors.push(data.params.exceptionDetails.exception?.description || data.params.exceptionDetails.text);
};
const send = (method, params = {}) => new Promise((resolve) => { pending.set(++nextId, resolve); socket.send(JSON.stringify({ id: nextId, method, params })); });
const evaluate = async (expression) => {
  const answer = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (answer.result?.exceptionDetails) throw new Error(answer.result.exceptionDetails.exception?.description || answer.result.exceptionDetails.text);
  return answer.result?.result?.value;
};
// until polls a page expression until it's truthy and returns its value.
const until = async (what, expression, seconds = 20) => {
  for (let elapsed = 0; elapsed < seconds * 4; elapsed++) {
    try {
      const value = await evaluate(expression);
      if (value) return value;
    } catch { /* the page is navigating */ }
    await wait(250);
  }
  fail(`timed out waiting for ${what}`);
};
const ok = (message) => console.log(`     ok: ${message}`);

await send("Runtime.enable");
await send("Page.enable");

// Sign in through Keycloak's form.
await send("Page.navigate", { url: `${base}/web/v1/login` });
await until("the Keycloak sign-in form", "!!document.querySelector('#username')", 40);
await evaluate(`(() => {
  document.querySelector('#username').value = ${JSON.stringify(adminUser)};
  document.querySelector('#password').value = ${JSON.stringify(adminPassword)};
  document.querySelector('form').submit();
})()`);
await until("the WebUI after sign-in", `location.href.startsWith(${JSON.stringify(base)}) && fetch('/web/v1/session').then((r) => r.json()).then((s) => s.authenticated)`, 40);
ok(`${adminUser} signed in through the WebUI`);

// Helpers that run in the page, installed after each navigation. React listens to its own
// events, so values go through the native setters.
const installHelpers = () => evaluate(`(() => {
  window.__lore = {
    grant: () => fetch('/web/v1/repositories/${repoId}/grants/').then((r) => r.json())
      .then((body) => (body.items || []).find((g) => g.subjectType === 'user' && g.subject === ${JSON.stringify(subjectId)}) || null),
    row: () => [...document.querySelectorAll('.access-grid table tr')].find((tr) => tr.textContent.includes(${JSON.stringify(subjectEmail)})),
    errorToast: () => [...document.querySelectorAll('.toast.err')].map((t) => t.textContent).join(' | '),
    toasts: () => [...document.querySelectorAll('.toast')].map((t) => t.textContent),
    type: (input, text) => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, text);
      input.dispatchEvent(new Event('input', { bubbles: true }));
    },
    choose: (select, value) => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(select, value);
      select.dispatchEvent(new Event('change', { bubbles: true }));
    },
  };
  return true;
})()`);

// checkGrant waits for the server to hold the expected permission (null: no grant) and fails on
// an error toast shown meanwhile.
const checkGrant = async (expected, action) => {
  for (let elapsed = 0; elapsed < 60; elapsed++) {
    const errorToast = await evaluate("window.__lore.errorToast()");
    if (errorToast) fail(`${action}: the WebUI showed an error: ${errorToast}`);
    const grant = await evaluate("window.__lore.grant()");
    if ((grant?.permission ?? null) === expected) return;
    await wait(250);
  }
  fail(`${action}: the server's grant isn't ${expected ?? "removed"}`);
};

await send("Page.navigate", { url: `${base}/repositories/${repoId}/access` });
await until("the Access tab's picker", "!!document.querySelector('.picker input')", 30);
await installHelpers();
if (await evaluate("window.__lore.grant()")) fail(`${subjectEmail} already has a grant before the check`);

// 1. Grant Read through the picker.
await evaluate(`window.__lore.type(document.querySelector('.picker input'), ${JSON.stringify(subjectEmail)})`);
await until(`${subjectEmail} in the picker's suggestions`, `[...document.querySelectorAll('.combo-item')].some((b) => b.textContent.includes(${JSON.stringify(subjectEmail)}))`);
await evaluate(`(() => {
  window.__lore.choose(document.querySelector('.picker select'), 'read');
  [...document.querySelectorAll('.combo-item')].find((b) => b.textContent.includes(${JSON.stringify(subjectEmail)})).click();
})()`);
await checkGrant("read", "granting Read from the picker");
await until("the new grant in the list", "!!window.__lore.row()");
ok("picker: Read granted, the user is listed");

// 2. Change it to Write in the list.
await evaluate("window.__lore.choose(window.__lore.row().querySelector('select'), 'write')");
await checkGrant("write", "changing the permission to Write");
await until("Write shown in the list", "window.__lore.row()?.querySelector('select').value === 'write'");
ok("list: permission changed to Write");

// 3. Revoke with ×.
await evaluate("window.__lore.row().querySelector('button').click()");
await checkGrant(null, "revoking with ×");
await until("the row to disappear", "!window.__lore.row()");
ok("list: revoked with ×, the row is gone");

// 4. Undo from the toast brings the same grant back.
await until("the revoke toast with Undo", "!!document.querySelector('.toast:not(.err) button')");
await evaluate("document.querySelector('.toast:not(.err) button').click()");
await checkGrant("write", "undoing the revoke");
await until("the row to come back", "!!window.__lore.row()");
ok("toast: Undo restored Write");

// 5. Revoke again, leaving the repository as it was.
await evaluate("window.__lore.row().querySelector('button').click()");
await checkGrant(null, "revoking again");
ok("revoked again; the repository has no grant for the user");

if (scriptErrors.length) fail(`script errors in the WebUI: ${scriptErrors.join(" | ")}`);
process.exit(0);
