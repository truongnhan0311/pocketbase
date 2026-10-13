// Drives headless Chrome over CDP: Logs page source switch to the extdb "familylink" logs.
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
const rows = () => evalJs(`document.querySelectorAll('.logs-table tbody tr[role=button]').length`);
const firstMsg = () => evalJs(`document.querySelector('.logs-table tbody tr[role=button] .col-field-name-message')?.textContent?.slice(0,120)`);
const total = () => evalJs(`document.querySelector('.total-logs')?.textContent`);

await s("Page.navigate", { url: baseUrl + "/_/" }); await sleep(1500);
await evalJs(`localStorage.setItem("__pb_superusers__/_", ${JSON.stringify(authJson)}); true`);
await s("Page.reload"); await sleep(2000);
await s("Page.navigate", { url: baseUrl + "/_/#/logs" }); await sleep(2500);
console.log("source buttons:", await evalJs(`Array.from(document.querySelectorAll('.logs-sources button')).map(b => b.textContent + (b.className.includes('secondary') ? '' : '*')).join(', ')`));
console.log("pb rows:", await rows(), "| total:", await total());
await evalJs(`Array.from(document.querySelectorAll('.logs-sources button')).find(b => b.textContent == 'Familylink').click(); true`); await sleep(3000);
console.log("hash:", await evalJs(`location.hash`));
console.log("familylink rows:", await rows(), "| total:", await total(), "| superuser toggle hidden:", await evalJs(`document.querySelector('.logs-include-superuser-requests').hidden`));
console.log("first row:", await firstMsg());
await shot("logs_1_familylink");
await evalJs(`document.querySelector('.logs-table tbody tr[role=button]').click(); true`); await sleep(1500);
console.log("modal rows:", await evalJs(`Array.from(document.querySelectorAll('.log-preview-modal th')).map(t => t.textContent).join(' ')`));
await shot("logs_2_modal");
await evalJs(`document.querySelector('.log-preview-modal .modal-footer .btn.transparent').click(); true`); await sleep(800);
// filter via url
await s("Page.navigate", { url: baseUrl + "/_/#/logs?source=familylink&filter=" + encodeURIComponent("data.status >= 400") }); await sleep(3000);
console.log("filter status>=400 rows:", await rows(), "| total:", await total(), "| first:", await firstMsg());
await shot("logs_3_filter");
await s("Page.navigate", { url: baseUrl + "/_/#/logs?source=familylink&filter=" + encodeURIComponent("Busuanga") }); await sleep(4000);
console.log("free text Busuanga rows:", await rows(), "| total:", await total());
// back to pocketbase
await evalJs(`Array.from(document.querySelectorAll('.logs-sources button')).find(b => b.textContent == 'PocketBase').click(); true`); await sleep(2500);
console.log("back to pb hash:", await evalJs(`location.hash`), "| rows:", await rows(), "| toggle visible:", await evalJs(`!document.querySelector('.logs-include-superuser-requests').hidden`));
console.log("ERRORS:", JSON.stringify(errors, null, 1));
ws.close(); process.exit(0);
