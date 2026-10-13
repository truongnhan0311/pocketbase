// Drives headless Chrome over CDP: login via localStorage, open #/extdb, click a table + a row, report console errors.
import fs from "node:fs";
const [, , cdpPort, baseUrl, authJson, outDir] = process.argv;
const ver = await (await fetch(`http://127.0.0.1:${cdpPort}/json/version`)).json();
const ws = new WebSocket(ver.webSocketDebuggerUrl);
await new Promise((r) => ws.addEventListener("open", r));
let id = 0; const pending = new Map(); const errors = [];
ws.addEventListener("message", (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
  if (msg.method === "Runtime.exceptionThrown") errors.push("exception: " + msg.params.exceptionDetails?.exception?.description);
  if (msg.method === "Runtime.consoleAPICalled" && ["error", "warning"].includes(msg.params.type)) errors.push(msg.params.type + ": " + msg.params.args.map((a) => a.value ?? a.description).join(" "));
});
const send = (method, params = {}, sessionId) => new Promise((r) => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params, sessionId })); });
const { result: { targetId } } = await send("Target.createTarget", { url: "about:blank" });
const { result: { sessionId } } = await send("Target.attachToTarget", { targetId, flatten: true });
const s = (m, p) => send(m, p, sessionId);
await s("Runtime.enable"); await s("Page.enable");
await s("Emulation.setDeviceMetricsOverride", { width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const evalJs = async (expr) => (await s("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true })).result?.result?.value;
const shot = async (name) => { const r = await s("Page.captureScreenshot", { format: "png" }); fs.writeFileSync(`${outDir}/${name}.png`, Buffer.from(r.result.data, "base64")); };

await s("Page.navigate", { url: baseUrl + "/_/" }); await sleep(1500);
await evalJs(`localStorage.setItem("__pb_superusers__/_", ${JSON.stringify(authJson)}); true`);
await s("Page.reload"); await sleep(2000);
await s("Page.navigate", { url: baseUrl + "/_/#/logs" }); await sleep(2000);
console.log("logs page header present:", await evalJs(`!!document.querySelector('.app-header')`));
await s("Page.navigate", { url: baseUrl + "/_/#/extdb?db=main" }); await sleep(2500);
console.log("extdb page header present:", await evalJs(`!!document.querySelector('.app-header')`), "select label:", await evalJs(`document.querySelector('.extdb-sidebar .select, .extdb-sidebar [class*=select]')?.textContent?.trim()?.slice(0,40)`));
console.log("nav has External DB:", await evalJs(`!!Array.from(document.querySelectorAll('.app-main-nav a')).find(a => a.textContent.includes('External DB'))`));
console.log("tables in sidebar:", await evalJs(`document.querySelectorAll('.extdb-sidebar .nav-item').length`));
await shot("1_tables");
await evalJs(`Array.from(document.querySelectorAll('.extdb-sidebar .nav-item')).find(a => a.title === 'admin_role')?.click(); true`); await sleep(1500);
console.log("rows:", await evalJs(`document.querySelectorAll('.extdb-table tbody tr.row-handle').length`), "total:", await evalJs(`document.querySelector('.page-footer')?.textContent`));
await shot("2_admin_role");
await evalJs(`document.querySelector('.extdb-table tbody tr.row-handle')?.click(); true`); await sleep(1000);
console.log("modal open:", await evalJs(`!!document.querySelector('.extdb-record-modal')`), "inputs:", await evalJs(`document.querySelectorAll('.extdb-record-modal input[type=text], .extdb-record-modal textarea').length`));
await shot("3_modal");
// edit note and save
await evalJs(`(() => { const el = document.querySelector('.extdb-record-modal [name=note]'); const nul = document.getElementById(el.id + '.null'); if (nul && nul.checked) { nul.click(); } el.value = 'edited via ui'; el.dispatchEvent(new Event('input', {bubbles:true})); return true; })()`); await sleep(300);
console.log("save enabled:", await evalJs(`!document.querySelector('.extdb-record-modal button[type=submit]').disabled`));
await evalJs(`document.querySelector('.extdb-record-modal button[type=submit]').click(); true`); await sleep(1500);
console.log("toast:", await evalJs(`document.querySelector('.toast, .toasts-wrapper')?.textContent?.trim()?.slice(0,120)`));
// search filter via url
await s("Page.navigate", { url: baseUrl + "/_/#/extdb?db=main&table=admin_role&search=" + encodeURIComponent("role = admin") }); await sleep(2500);
console.log("filter 'role = admin' rows:", await evalJs(`document.querySelectorAll('.extdb-table tbody tr.row-handle').length`));
await s("Page.navigate", { url: baseUrl + "/_/#/extdb?db=main&table=admin_role&search=" + encodeURIComponent("note is null") }); await sleep(2500);
console.log("filter 'note is null' rows:", await evalJs(`document.querySelectorAll('.extdb-table tbody tr.row-handle').length`));
await s("Page.navigate", { url: baseUrl + "/_/#/extdb?db=main&table=admin_role&search=fb092da0" }); await sleep(2500);
console.log("search 'fb092da0' rows:", await evalJs(`document.querySelectorAll('.extdb-table tbody tr.row-handle').length`));
await shot("4_filter");
console.log("ERRORS:", JSON.stringify(errors, null, 1));
ws.close(); process.exit(0);
