"use strict";

const $ = id => document.getElementById(id);
const state = { peers: [], me: "", selected: "", monitor: null, refresh: 0 };
const fragment = location.hash.slice(1);
if (/^[a-f0-9]{64}$/.test(fragment)) {
  sessionStorage.setItem("anyusb-token", fragment);
  history.replaceState(null, "", "/");
}
const token = sessionStorage.getItem("anyusb-token") || "";
const enc = encodeURIComponent;
const mesh = "/v1/mesh";
const peerPath = id => mesh + "/peers/" + enc(id);

async function api(path, method = "GET", data) {
  const headers = { Authorization: "Bearer " + token };
  if (data !== undefined && !(data instanceof FormData)) {
    headers["Content-Type"] = "application/json";
    data = JSON.stringify(data);
  }
  const response = await fetch(path, { method, headers, body: data, cache: "no-store" });
  const text = await response.text();
  let value;
  try { value = JSON.parse(text); } catch { value = { message: text.trim() }; }
  if (!response.ok) throw new Error(value.message || response.statusText);
  return value;
}
function notice(text) { $("notice").textContent = text; }
function action(fn) { return async event => { event?.preventDefault(); try { await fn(); } catch (err) { notice(err.message); } }; }
function button(label, fn, disabled = false) {
  const result = document.createElement("button");
  result.type = "button";
  result.textContent = label;
  result.disabled = disabled;
  result.onclick = action(fn);
  return result;
}
function row(table, fields) {
  const tr = document.createElement("tr");
  for (const field of fields) {
    const td = document.createElement("td");
    if (Array.isArray(field)) td.append(...field); else td.textContent = field || "—";
    tr.append(td);
  }
  table.append(tr);
}
function label(device) { return [device.manufacturer, device.product].filter(Boolean).join(" ") || device.id; }
function owner(id) { return id === state.me ? "This machine" : state.peers.find(p => p.id === id)?.name || id || "—"; }

async function refreshLocal() {
  const batch = await Promise.allSettled([api("/v1/devices"), api(mesh + "/shares"), api(mesh + "/peers")]);
  if (batch[2].status === "fulfilled") {
    state.peers = batch[2].value;
    const selected = $("peer").value;
    $("peer").replaceChildren(...state.peers.map(p => new Option(p.name || p.endpoint || p.id, p.id)));
    if (state.peers.some(p => p.id === selected)) $("peer").value = selected;
  }
  const local = $("local");
  local.replaceChildren();
  if (batch[0].status === "rejected") { row(local, [batch[0].reason.message]); return; }
  const shares = batch[1].value || [];
  for (const device of batch[0].value) {
    const shared = shares.find(s => s.id === device.id);
    const sharing = shared?.present;
    row(local, [label(device), device.bus_id, owner(shared?.lease_owner), [
      button(sharing ? "Unshare" : "Share", async () => {
        await api(mesh + "/shares/" + enc(device.id), sharing ? "DELETE" : "PUT");
        await refreshLocal();
      }, Boolean(shared?.lease_owner))
    ]]);
  }
  if (!local.children.length) row(local, ["No physical USB devices found"]);
}

async function refreshRemote() {
  const key = ++state.refresh;
  const peer = $("peer").value;
  const remote = $("remote");
  if (!peer) { remote.replaceChildren(); row(remote, ["Pair with a peer to see its devices"]); return; }
  const [devices, connections] = await Promise.all([
    api(peerPath(peer) + "/devices"), api(mesh + "/connections")
  ]);
  if (key !== state.refresh) return;
  remote.replaceChildren();
  for (const device of devices) {
    const current = connections.find(c => c.device === device.id && c.peer === peer);
    const busy = device.lease_owner && device.lease_owner !== state.me;
    const status = current?.state || (device.present ? "Available" : "Absent");
    const port = current?.import?.serial_ports?.join(", ");
    row(remote, [label(device), port ? status + " · " + port : status, owner(device.lease_owner), [
      button(current ? "Disconnect" : "Connect", async () => {
        if (current) await api(mesh + "/connections/" + enc(current.id), "DELETE");
        else await api(mesh + "/connections", "POST", { peer, device: device.id });
        await refreshRemote();
      }, Boolean(!current && (busy || !device.present || device.lease_mode === "esp"))),
      button("ESP", async () => {
        state.selected = device.id;
        $("target").textContent = label(device);
        $("esp-title").scrollIntoView({ block: "nearest" });
      }, Boolean(!device.present || busy))
    ]]);
  }
  if (!remote.children.length) row(remote, ["No shared devices"]);
}

function socket(path) {
  const address = new URL(path, location.href);
  address.protocol = "ws:";
  address.searchParams.set("token", token);
  return new WebSocket(address);
}
function appendLog(text) {
  const log = $("logs");
  log.textContent = (log.textContent + text).slice(-120000);
  log.scrollTop = log.scrollHeight;
}
function settings() {
  const form = $("esp-form").elements;
  const query = new URLSearchParams({ baud: form.baud.value || "115200" });
  if (form.port.value) query.set("port", form.port.value);
  return query;
}
function selectedPath() {
  if (!$("peer").value || !state.selected) throw new Error("Select a remote device with the ESP button first");
  return peerPath($("peer").value) + "/esp/" + enc(state.selected);
}
async function firmware() {
  const form = $("esp-form").elements;
  const files = Array.from(form.firmware.files);
  if (!files.length) throw new Error("Select the firmware files");
  const upload = new FormData();
  if (form.manifest.files.length) {
    const manifest = JSON.parse(await form.manifest.files[0].text());
    for (const name of new Set(Object.values(manifest.flash_files || {}))) {
      const basename = name.split(/[/\\]/).pop();
      const matches = files.filter(f => f.name === basename);
      if (matches.length !== 1) throw new Error("Select exactly one file named " + basename);
      upload.append("files", matches[0], name);
    }
    upload.append("manifest", JSON.stringify(manifest));
  } else {
    if (files.length !== 1) throw new Error("A multi-file upload needs an ESP-IDF manifest");
    upload.append("files", files[0], files[0].name);
    upload.append("manifest", JSON.stringify({ flash_files: { [form.offset.value]: files[0].name } }));
  }
  return upload;
}
async function espOperation(op) {
  const path = selectedPath();
  if (op === "erase" && !window.confirm("Erase the selected board's flash?")) return;
  const peer = $("peer").value;
  const query = settings();
  query.set("op", op);
  const upload = ["flash", "verify"].includes(op) ? await firmware() : undefined;
  $("logs").textContent = "";
  const job = await api(path + "/jobs?" + query, "POST", upload);
  notice("ESP operation started");
  const logs = socket(peerPath(peer) + "/jobs/" + enc(job.id) + "/logs");
  logs.onmessage = event => {
    const frame = JSON.parse(event.data);
    for (const log of frame.logs || []) appendLog(log.text);
    if (frame.job.status !== "running") notice("ESP operation " + frame.job.status + (frame.job.error ? " · " + frame.job.error : ""));
  };
  logs.onerror = () => notice("Log stream disconnected. Host job " + job.id + " may still be running.");
}

function monitorSend(message) {
  if (state.monitor?.readyState !== WebSocket.OPEN) throw new Error("Open the monitor first");
  state.monitor.send(JSON.stringify(message));
}
$("pair").onsubmit = action(async () => {
  await api(mesh + "/pair", "POST", Object.fromEntries(new FormData($("pair"))));
  $("pair").elements.code.value = "";
  await refreshLocal();
  await refreshRemote();
  notice("Peer paired");
});
$("invite").onclick = action(async () => {
  const invite = await api(mesh + "/invite", "POST");
  $("invite-code").textContent = invite.code + " · expires at " + new Date(invite.expires).toLocaleTimeString();
});
$("discover").onclick = action(async () => {
  const results = await api(mesh + "/discovery");
  const list = $("discoveries");
  list.replaceChildren();
  for (const peer of results.filter(p => p.id !== state.me)) {
    const li = document.createElement("li");
    li.append(button(peer.name + " · " + peer.endpoint, async () => { $("pair").elements.endpoint.value = peer.endpoint; $("pair").elements.code.focus(); }));
    list.append(li);
  }
  if (!list.children.length) notice("No other AnyUSB peers found on the LAN");
});
$("peer").onchange = action(async () => {
  state.selected = "";
  $("target").textContent = "Select a remote device";
  state.monitor?.close();
  await refreshRemote();
});
$("refresh").onclick = action(async () => { await refreshLocal(); await refreshRemote(); notice(""); });
$("doctor").onclick = action(async () => {
  const checks = await api("/v1/doctor");
  const list = $("checks");
  list.replaceChildren(...checks.map(check => {
    const li = document.createElement("li");
    li.textContent = (check.ok ? "OK" : "Check") + " · " + check.name + " · " + check.detail;
    return li;
  }));
});
for (const b of document.querySelectorAll("[data-op]")) b.onclick = action(() => espOperation(b.dataset.op));
$("monitor").onclick = action(async () => {
  if (state.monitor) { state.monitor.close(); return; }
  $("logs").textContent = "";
  const decoder = new TextDecoder();
  const ws = socket(selectedPath() + "/monitor?" + settings());
  state.monitor = ws;
  ws.onopen = () => { $("monitor").textContent = "Close monitor"; notice("Serial monitor connected"); };
  ws.onmessage = event => {
    const m = JSON.parse(event.data);
    if (m.error) { notice(m.error); return; }
    if (m.data) appendLog(decoder.decode(Uint8Array.from(atob(m.data), c => c.charCodeAt(0)), { stream: true }));
  };
  ws.onerror = () => notice("Monitor connection failed");
  ws.onclose = () => { if (state.monitor === ws) { state.monitor = null; $("monitor").textContent = "Open monitor"; } };
});
$("serial").onsubmit = action(async () => {
  const input = $("serial").elements.text;
  const data = new TextEncoder().encode(input.value + "\n");
  if (data.length > 16384) throw new Error("Serial input exceeds 16 KiB");
  monitorSend({ data: btoa(String.fromCharCode(...data)) });
  input.value = "";
});
$("apply-serial").onclick = action(async () => {
  const serial = $("serial").elements;
  monitorSend({ baud: Number(settings().get("baud")), dtr: serial.dtr.checked, rts: serial.rts.checked });
});

action(async () => {
  if (!token) { notice("Run anyusb ui and open the authorized URL from the CLI"); return; }
  const info = await api(mesh + "/info");
  state.me = info.id;
  $("agent").textContent = info.name || "Local agent";
  await refreshLocal();
  await refreshRemote();
  window.setInterval(() => { if ($("peer").value && !document.hidden) refreshRemote().catch(() => {}); }, 3000);
})();
