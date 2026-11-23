"use strict";

const $ = id => document.getElementById(id);
const mesh = "/v1/mesh";
const enc = encodeURIComponent;
const peerPath = id => mesh + "/peers/" + enc(id);
const state = { me: null, peer: "", peers: [], devices: [], shares: [], discoveries: [], requests: [],
  selected: null, firmware: null, monitor: null, job: null, scanning: false, polling: false };

const fragment = location.hash.slice(1);
if (/^[a-f0-9]{64}$/.test(fragment)) {
  sessionStorage.setItem("anyusb-token", fragment);
  history.replaceState(null, "", "/");
}
const token = sessionStorage.getItem("anyusb-token") || "";

async function api(path, method = "GET", data) {
  const headers = { Authorization: "Bearer " + token };
  const upload = data instanceof FormData;
  if (data !== undefined && !upload) {
    headers["Content-Type"] = "application/json";
    data = JSON.stringify(data);
  }
  const response = await fetch(path, { method, headers, body: data, cache: "no-store", signal: AbortSignal.timeout(upload ? 180000 : 20000) });
  const text = await response.text();
  let value;
  try { value = JSON.parse(text); } catch { value = { message: text.trim() }; }
  if (response.status === 401) throw new Error("Phiên AnyUSB hết hạn. Hãy đóng rồi mở lại ứng dụng.");
  if (!response.ok) throw new Error(value.message || response.statusText);
  return value;
}

function status(element, text, error = false) {
  $(element).hidden = !text;
  $(element).classList.toggle("error", error);
  $(element).textContent = text;
}
function action(fn) {
  return async event => {
    event?.preventDefault();
    const control = event?.currentTarget;
    if (control?.tagName === "BUTTON") control.disabled = true;
    try { await fn(); } catch (err) { status("notice", err.message, true); }
    finally { if (control?.tagName === "BUTTON") control.disabled = false; }
  };
}
function el(tag, text = "", className = "") {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}
function button(text, handler, { primary = false, disabled = false } = {}) {
  const node = el("button", text, primary ? "primary" : "");
  node.type = "button";
  node.disabled = disabled;
  node.onclick = action(handler);
  return node;
}
function show(id, key, nodes) {
  const region = $(id);
  const snapshot = JSON.stringify(key);
  if (region.dataset.snapshot !== snapshot) {
    region.dataset.snapshot = snapshot;
    region.replaceChildren(...nodes());
  }
}
function label(device) {
  return [device.manufacturer, device.product].filter(Boolean).join(" ") || "Thiết bị USB";
}
function card(name, detail, statusType, actions) {
  const info = el("div", "", "info");
  const meta = el("p", "", "meta");
  meta.append(el("span", "", "state-dot " + statusType), document.createTextNode(detail));
  info.append(el("h3", name), meta);
  const controls = el("div", "", "actions");
  controls.append(...actions);
  const item = el("article", "", "device-card");
  item.append(info, controls);
  return item;
}
function empty(text) { return el("div", text, "empty"); }

function navButton(name, detail, active, handler) {
  const node = button("", handler);
  node.className = "nav-device" + (active ? " active" : "");
  node.setAttribute("aria-current", active ? "page" : "false");
  node.append(el("span", name), el("small", detail, detail === "Ngoại tuyến" ? "offline" : "available"));
  return node;
}
function renderNav() {
  $("this-machine").classList.toggle("active", !state.peer);
  $("this-machine").setAttribute("aria-current", state.peer ? "false" : "page");
  const online = new Map(state.discoveries.map(p => [p.id, p.network]));
  show("peers", [state.peers, state.peer, state.discoveries], () => state.peers.length
    ? state.peers.map(p => navButton(p.name || p.endpoint, online.get(p.id) || "Ngoại tuyến", state.peer === p.id, () => selectPeer(p.id)))
    : [el("p", "Chưa có máy nào", "small")]);
  const available = state.discoveries.filter(p => !state.peers.some(saved => saved.id === p.id));
  show("discoveries", [available, state.requests], () => available.map(p => {
    const last = state.requests.findLast(r => !r.incoming && r.peer.id === p.id);
    const waiting = last?.status === "pending";
    const detail = waiting ? "Đang chờ" : last?.status === "declined" ? "Đã từ chối · Bấm để thử lại" : p.network;
    const node = navButton(p.name || p.endpoint, detail, false, async () => {
      await api(mesh + "/pair-requests", "POST", { endpoint: p.endpoint, id: p.id });
      await poll();
    });
    node.disabled = waiting;
    node.setAttribute("aria-label", "Ghép cặp với " + (p.name || p.endpoint));
    return node;
  }));
}
function renderRequests() {
  const requests = state.requests.filter(r => r.status === "pending");
  $("pairing").hidden = !requests.length;
  show("requests", requests, () => requests.map(r => {
    const message = el("div");
    message.append(el("h3", (r.peer.name || r.peer.endpoint) + (r.incoming ? " muốn ghép cặp" : " đang chờ đồng ý")),
      el("span", r.code, "request-code"),
      el("p", r.incoming ? "Chỉ đồng ý nếu mã trên hai máy trùng nhau." : "Đối chiếu mã rồi bấm đồng ý trên máy kia."));
    const controls = el("div", "", "actions");
    if (r.incoming) {
      const decide = approve => async () => {
        await api(mesh + "/pair-requests/" + enc(r.id), "POST", { approve });
        await poll();
      };
      controls.append(button("Từ chối", decide(false)), button("Mã trùng, đồng ý", decide(true), { primary: true }));
    }
    const item = el("article", "", "request");
    item.append(message, controls);
    return item;
  }));
}
function renderLocal(error) {
  show("local", [state.devices, state.shares, error], () => {
    if (error) return [empty(error + " · Xem Chẩn đoán để kiểm tra driver.")];
    if (!state.devices.length) return [empty("Cắm USB vào máy này để dùng hoặc chia sẻ.")];
    return state.devices.map(device => {
      const shared = state.shares.find(s => s.id === device.id);
      const busy = Boolean(shared?.lease_owner);
      return card(label(device), busy ? "Đang được sử dụng" : shared ? "Máy đã ghép cặp có thể dùng" : "Đang cắm", shared ? "good" : "",
        [button(shared ? "Dừng chia sẻ" : "Cho phép dùng", async () => {
          await api(mesh + "/shares/" + enc(device.id), shared ? "DELETE" : "PUT");
          await poll();
        }, { primary: !shared, disabled: busy })]);
    });
  });
}
const linkLabels = { attached: "Đã kết nối", connecting: "Đang kết nối", reconnecting: "Đang nối lại", lost: "Đang chờ thiết bị trở lại" };

async function refreshRemote() {
  const selected = state.peer;
  if (!selected) return;
  const [devices, connections] = await Promise.allSettled([
    api(peerPath(selected) + "/devices"), api(mesh + "/connections")
  ]);
  if (selected !== state.peer) return;
  const shared = devices.value || [];
  const links = (connections.value || []).filter(c => c.peer === selected);
  const entries = [...shared];
  for (const link of links) {
    if (!entries.some(d => d.id === link.device)) entries.push({ id: link.device, product: link.import?.name || "Thiết bị đã kết nối" });
  }
  show("remote", [selected, shared, links, devices.status], () => {
    if (!entries.length) return [empty(devices.status === "rejected" ? "Không thể liên hệ máy này. AnyUSB sẽ thử lại." : "Máy này chưa chia sẻ USB.")];
    return entries.map(device => {
      const current = links.find(c => c.device === device.id);
      const busy = Boolean(device.lease_owner && device.lease_owner !== state.me.id);
      const ports = current?.import?.serial_ports || [];
      const online = current?.state === "attached";
      let summary = current ? linkLabels[current.state] || current.state : device.present ? "Sẵn sàng" : "Chưa cắm USB";
      if (busy) summary = "Đang được máy khác sử dụng";
      if (ports.length) summary += " · " + ports.join(", ");
      const connect = button(current ? "Ngắt kết nối" : "Kết nối", async () => {
        if (current) await api(mesh + "/connections/" + enc(current.id), "DELETE");
        else await api(mesh + "/connections", "POST", { peer: selected, device: device.id });
        await refreshRemote();
      }, { primary: !current, disabled: !current && (!device.present || busy || device.lease_mode === "esp") });
      const esp = button("ESP", () => {
        state.selected = device;
        $("target").textContent = label(device);
        $("esp-panel").hidden = false;
        $("esp-panel").scrollIntoView({ block: "nearest" });
      }, { disabled: !device.present || busy || Boolean(current) });
      return card(label(device), summary, online || device.present && !current ? "good" : current ? "wait" : "", [connect, esp]);
    });
  });
}
function closeESP() {
  state.monitor?.close();
  state.selected = null;
  $("esp-panel").hidden = true;
}
async function selectPeer(id) {
  if (state.peer !== id) {
    closeESP();
    $("remote").dataset.snapshot = "";
    $("remote").replaceChildren();
  }
  state.peer = id;
  $("local-section").hidden = Boolean(id);
  $("remote-section").hidden = !id;
  const peer = state.peers.find(p => p.id === id);
  $("page-title").textContent = peer ? peer.name || "USB từ xa" : "Thiết bị USB";
  $("page-detail").textContent = peer ? "USB đã được chia sẻ từ máy này" : "Thiết bị đang cắm vào " + (state.me?.name || "máy này");
  renderNav();
  await refreshRemote();
}
async function poll() {
  if (state.polling) return;
  state.polling = true;
  try {
    const [local, shares, peers, requests] = await Promise.allSettled([
      api("/v1/devices"), api(mesh + "/shares"), api(mesh + "/peers"), api(mesh + "/pair-requests"),
    ]);
    state.devices = local.value || [];
    state.shares = shares.value || [];
    const previous = state.peers;
    state.peers = peers.value || state.peers;
    state.requests = requests.value || [];
    renderLocal(local.reason?.message);
    renderRequests();
    renderNav();
    const paired = state.requests.find(r => !r.incoming && r.status === "approved" &&
      !previous.some(p => p.id === r.peer.id) && state.peers.some(p => p.id === r.peer.id));
    if (paired && !state.selected) await selectPeer(paired.peer.id);
    else await refreshRemote();
  } finally {
    state.polling = false;
  }
}
async function discover() {
  if (state.scanning) return;
  state.scanning = true;
  $("scan").textContent = "Đang tìm trên LAN và Tailscale…";
  try {
    state.discoveries = await api(mesh + "/discovery");
    renderNav();
    $("scan").textContent = state.discoveries.length ? "" : "Chưa thấy máy AnyUSB đang mở.";
  } catch (err) {
    $("scan").textContent = err.message;
  } finally {
    state.scanning = false;
  }
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
  if (!state.peer || !state.selected) throw new Error("Chọn thiết bị ESP trước.");
  return peerPath(state.peer) + "/esp/" + enc(state.selected.id);
}
function setFirmware(bundle) {
  state.firmware = bundle;
  $("firmware-name").textContent = bundle.name + " · " + bundle.files.length + " file";
}
async function pickFolder(input) {
  const files = Array.from(input.files);
  const manifest = files.find(file => file.name === "flasher_args.json");
  if (!manifest) throw new Error("Chọn thư mục chứa flasher_args.json.");
  const index = manifest.webkitRelativePath.lastIndexOf("/");
  const root = manifest.webkitRelativePath.substring(0, index + 1);
  const spec = JSON.parse(await manifest.text());
  const parts = Object.values(spec.flash_files || {});
  if (!parts.length) throw new Error("Manifest chưa có file nạp.");
  const matched = [...new Set(parts)].map(name => {
    const file = files.find(f => f.webkitRelativePath === root + name.replaceAll("\\", "/").replace(/^\.\//, ""));
    if (!file) throw new Error("Thiếu file " + name);
    return { name, blob: file };
  });
  setFirmware({ name: manifest.name, manifest: spec, files: matched });
}
function firmwareUpload() {
  if (!state.firmware) throw new Error("Chọn flasher_args.json trước.");
  const upload = new FormData();
  upload.append("manifest", JSON.stringify(state.firmware.manifest));
  for (const file of state.firmware.files) upload.append("files", file.blob || new Blob([file.data]), file.name);
  return upload;
}
function updateESPControls() {
  for (const control of document.querySelectorAll("[data-op],#choose-firmware,#close-esp")) control.disabled = Boolean(state.job);
  $("monitor").disabled = Boolean(state.job);
}
async function espOperation(op) {
  if (state.job || state.monitor) throw new Error("Đóng tác vụ hoặc Serial Monitor trước.");
  const path = selectedPath();
  const peer = state.peer;
  const query = settings();
  query.set("op", op);
  const upload = (op === "flash" || op === "verify") ? firmwareUpload() : undefined;
  $("logs").textContent = "";
  state.job = {};
  updateESPControls();
  status("esp-status", upload ? "Đang gửi firmware…" : "Đang mở cổng ESP…");
  try {
    const job = await api(path + "/jobs?" + query, "POST", upload);
    const logs = socket(peerPath(peer) + "/jobs/" + enc(job.id) + "/logs");
    state.job = logs;
    logs.onmessage = event => {
      const frame = JSON.parse(event.data);
      for (const item of frame.logs || []) appendLog(item.text);
      if (frame.job.status === "running") {
        status("esp-status", "Đang chạy trên máy cắm USB…");
      } else {
        const ok = frame.job.status === "succeeded";
        status("esp-status", ok ? "Hoàn tất" : "ESP báo lỗi " + (frame.job.error || ""), !ok);
        state.job = null;
        updateESPControls();
      }
    };
    logs.onclose = () => {
      if (state.job === logs) {
        state.job = null;
        updateESPControls();
        status("esp-status", "Mất luồng nhật ký. Tác vụ trên máy cắm USB có thể vẫn đang chạy.", true);
      }
    };
  } catch (err) {
    state.job = null;
    updateESPControls();
    status("esp-status", err.message, true);
  }
}
function monitorSend(message) {
  if (state.monitor?.readyState !== WebSocket.OPEN) throw new Error("Mở Serial Monitor trước.");
  state.monitor.send(JSON.stringify(message));
}

$("this-machine").onclick = action(() => selectPeer(""));
$("discover").onclick = action(discover);
$("refresh").onclick = action(async () => { await poll(); await discover(); });
$("pair").onsubmit = action(async () => {
  const peer = await api(mesh + "/pair", "POST", Object.fromEntries(new FormData($("pair"))));
  $("pair").elements.code.value = "";
  await poll();
  if (peer.id) await selectPeer(peer.id);
});
$("invite").onclick = action(async () => {
  const invite = await api(mesh + "/invite", "POST");
  $("invite-code").textContent = invite.code + " · " + new Date(invite.expires).toLocaleTimeString();
});
$("doctor").onclick = action(async () => {
  const checks = await api("/v1/doctor");
  $("checks").replaceChildren(...checks.map(check => el("li", (check.ok ? "OK" : "Cần kiểm tra") + " · " + check.name + " · " + check.detail)));
});
$("close-esp").onclick = closeESP;
$("choose-firmware").onclick = action(async () => {
  if (window.anyusbDesktop) {
    const bundle = await window.anyusbDesktop.chooseFirmware();
    if (bundle) setFirmware(bundle);
  } else {
    $("firmware-folder").click();
  }
});
$("firmware-folder").onchange = action(() => pickFolder($("firmware-folder")));
for (const b of document.querySelectorAll("[data-op]")) b.onclick = () => espOperation(b.dataset.op).catch(err => status("esp-status", err.message, true));
$("monitor").onclick = action(async () => {
  if (state.monitor) { state.monitor.close(); return; }
  $("logs").textContent = "";
  const decoder = new TextDecoder();
  const ws = socket(selectedPath() + "/monitor?" + settings());
  state.monitor = ws;
  ws.onopen = () => {
    $("monitor").textContent = "Đóng Serial Monitor";
    $("serial").hidden = false;
    status("esp-status", "Serial Monitor đang mở");
  };
  ws.onmessage = event => {
    const message = JSON.parse(event.data);
    if (message.error) status("esp-status", message.error, true);
    if (message.data) appendLog(decoder.decode(Uint8Array.from(atob(message.data), c => c.charCodeAt(0)), { stream: true }));
  };
  ws.onerror = () => status("esp-status", "Không mở được Serial Monitor.", true);
  ws.onclose = () => {
    if (state.monitor === ws) {
      state.monitor = null;
      $("monitor").textContent = "Mở Serial Monitor";
      $("serial").hidden = true;
      if ($("esp-status").textContent === "Serial Monitor đang mở") status("esp-status", "Đã đóng Serial Monitor.");
    }
  };
});
$("serial").onsubmit = action(async () => {
  const input = $("serial").elements.text;
  const data = new TextEncoder().encode(input.value + "\n");
  if (data.length > 16384) throw new Error("Dữ liệu gửi vượt quá 16 KiB.");
  monitorSend({ data: btoa(String.fromCharCode(...data)) });
  input.value = "";
});
$("apply-serial").onclick = action(async () => {
  const serial = $("serial").elements;
  monitorSend({ baud: Number(settings().get("baud")), dtr: serial.dtr.checked, rts: serial.rts.checked });
});

action(async () => {
  if (!token) { status("notice", "Hãy mở AnyUSB từ menu ứng dụng để bắt đầu."); return; }
  state.me = await api(mesh + "/info");
  $("agent").textContent = state.me.name || "Máy này";
  await selectPeer("");
  await poll();
  void discover();
  window.setInterval(() => poll().catch(err => status("notice", err.message, true)), 2600);
  window.setInterval(() => void discover(), 12000);
})();
