"use strict";

const $ = id => document.getElementById(id);
const mesh = "/v1/mesh";
const enc = encodeURIComponent;
const peerPath = id => mesh + "/peers/" + enc(id);
const state = { me: null, peer: "", peers: [], devices: [], shares: [], discoveries: [], requests: [],
  scanning: false, polling: false };

const fragment = location.hash.slice(1);
if (/^[a-f0-9]{64}$/.test(fragment)) {
  sessionStorage.setItem("anyusb-token", fragment);
  history.replaceState(null, "", "/");
}
const token = sessionStorage.getItem("anyusb-token") || "";

async function api(path, method = "GET", data) {
  const headers = { Authorization: "Bearer " + token };
  if (data !== undefined) {
    headers["Content-Type"] = "application/json";
    data = JSON.stringify(data);
  }
  const response = await fetch(path, { method, headers, body: data, cache: "no-store", signal: AbortSignal.timeout(20000) });
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
const profileLabels = { serial: "Serial", stlink: "ST-Link", jlink: "J-Link", "cmsis-dap": "CMSIS-DAP",
  "debug-probe": "Debug probe", storage: "Lưu trữ", hid: "HID", network: "Mạng USB", audio: "Âm thanh",
  video: "Video", printer: "Máy in", dfu: "DFU", unknown: "USB chưa phân loại" };
function profileText(device) {
  return (device.profiles || []).map(p => profileLabels[p] || p).filter((p, i, all) => all.indexOf(p) === i).join(" · ");
}
function riskText(device) {
  const risks = { storage: "Ngắt đột ngột có thể làm hỏng dữ liệu đang ghi.", input: "Thiết bị này có thể tạo thao tác nhập trên máy nhận.",
    network: "Thiết bị này có thể thay đổi đường mạng của máy nhận.", timing: "Thiết bị thời gian thực cần được kiểm chứng theo đúng workload.",
    topology: "Không chia sẻ USB hub hoặc bộ điều khiển USB." };
  return risks[device.risk] || "";
}
const probeGuidance = {
  stlink: "Kiểm tra bằng STM32CubeProgrammer (chỉ liệt kê probe, không ghi target).",
  jlink: "Kiểm tra bằng J-Link Commander (chỉ ShowEmuList USB, không cập nhật phần mềm probe).",
  "cmsis-dap": "Kiểm tra bằng pyOCD (chỉ liệt kê probe, không halt/flash target)."
};
function card(name, detail, statusType, actions, notes = []) {
  const info = el("div", "", "info");
  const meta = el("p", "", "meta");
  meta.append(el("span", "", "state-dot " + statusType), document.createTextNode(detail));
  info.append(el("h3", name), meta, ...notes);
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
      const blocked = device.share_policy === "block";
      const detail = [busy ? "Đang được sử dụng" : shared ? "Máy đã ghép cặp có thể dùng" : blocked ? "Không thể chia sẻ toàn thiết bị" : "Đang cắm", profileText(device)].filter(Boolean).join(" · ");
      const notes = riskText(device) ? [el("p", riskText(device), "meta")] : [];
      return card(label(device), detail, shared ? "good" : "",
        [button(shared ? "Dừng chia sẻ" : "Cho phép dùng", async () => {
          await api(mesh + "/shares/" + enc(device.id), shared ? "DELETE" : "PUT");
          await poll();
        }, { primary: !shared, disabled: busy || blocked })], notes);
    });
  });
}
const linkLabels = { attached: "Đã gắn vào máy này", connecting: "Đang kết nối", reconnecting: "Đang nối lại", lost: "Đang chờ thiết bị trở lại" };
const readinessLabels = { attached: "USB đã gắn, chưa xác minh driver", enumerating: "Đang nhận diện chức năng USB",
  ready: "Driver sẵn sàng", degraded: "Một phần chức năng chưa sẵn sàng", driver_missing: "Thiếu driver",
  error: "Lỗi thiết bị/driver", unknown: "Đã gắn nhưng chưa xác minh" };

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
    if (!entries.some(d => d.id === link.device)) entries.push(link.device_info || { id: link.device, product: link.import?.name || "Thiết bị đã kết nối" });
  }
  show("remote", [selected, shared, links, devices.status], () => {
    if (!entries.length) return [empty(devices.status === "rejected" ? "Không thể liên hệ máy này. AnyUSB sẽ thử lại." : "Máy này chưa chia sẻ USB.")];
    return entries.map(device => {
      const current = links.find(c => c.device === device.id);
      const busy = Boolean(device.lease_owner && device.lease_owner !== state.me.id);
      const ports = current?.import?.serial_ports || [];
      const bindings = current?.import?.local_bindings || [];
      const online = current?.state === "attached";
      const readiness = current?.import?.readiness;
      let summary = current ? linkLabels[current.state] || current.state : device.present ? "Sẵn sàng" : "Chưa cắm USB";
      if (online && readiness) summary += " · " + (readinessLabels[readiness] || readiness);
      if (profileText(device)) summary += " · " + profileText(device);
      if (busy) summary = "Đang được máy khác sử dụng";
      const connect = button(current ? "Ngắt kết nối" : "Kết nối", async () => {
        if (current) await api(mesh + "/connections/" + enc(current.id), "DELETE");
        else {
          let confirmRisk = false;
          if (device.share_policy === "confirm") {
            confirmRisk = window.confirm((riskText(device) || "Thiết bị này cần xác nhận rủi ro.") + "\n\nTiếp tục kết nối?");
            if (!confirmRisk) return;
          }
          await api(mesh + "/connections", "POST", { peer: selected, device: device.id, confirm_risk: confirmRisk });
        }
        await refreshRemote();
      }, { primary: !current, disabled: !current && (!device.present || busy || device.lease_mode === "esp") });
      const actions = [connect];
      const notes = [];
      if (online && ports.length) {
        const localPorts = el("p", "", "local-ports");
        localPorts.append(el("span", "Cổng trên máy này"), ...ports.map(port => el("code", port)));
        notes.push(localPorts, el("p", "Chọn cổng này trong VS Code hoặc IDE của bạn.", "meta"));
      } else if (online) {
        notes.push(el("p", "USB đã được gắn vào hệ điều hành; thiết bị này không cần COM/tty. Hãy dùng driver và công cụ gốc của thiết bị.", "meta"));
      } else if (current) {
        notes.push(el("p", "AnyUSB sẽ tự nối lại khi máy từ xa, mạng hoặc thiết bị sẵn sàng; không cần bấm Kết nối lại.", "meta"));
      }
      if (online && bindings.length) {
        const localBindings = el("p", "", "local-ports");
        localBindings.append(el("span", "Liên kết hệ điều hành"), ...bindings.map(binding => el("code", binding.kind + ":" + binding.id + (binding.status && binding.status !== "ready" ? " [" + binding.status + "]" : ""))));
        notes.push(localBindings);
      }
      const probeProfile = (device.profiles || []).find(profile => probeGuidance[profile]);
      if (online && probeProfile) {
        notes.push(el("p", probeGuidance[probeProfile], "meta"));
        actions.push(button("Kiểm tra " + profileLabels[probeProfile], async () => {
          const result = await api(mesh + "/connections/" + enc(current.id) + "/probe", "POST", { profile: probeProfile });
          const detail = result.ok
            ? profileLabels[probeProfile] + " đã nhận đúng serial " + (result.serial || "")
            : (result.error || "Công cụ gốc chưa xác nhận đúng thiết bị.");
          status("notice", detail, !result.ok);
        }));
      }
      if (riskText(device)) notes.push(el("p", riskText(device), "meta"));
      if (current?.error) notes.push(el("p", current.error, "link-error"));
      return card(label(device), summary, online || device.present && !current ? "good" : current ? "wait" : "", actions, notes);
    });
  });
}
async function selectPeer(id) {
  if (state.peer !== id) {
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
    if (paired && !state.peer) await selectPeer(paired.peer.id);
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
  $("checks").replaceChildren(...checks.map(check => el("li", (check.ok ? "OK" : check.optional ? "Tùy chọn" : "Cần kiểm tra") + " · " + check.name + " · " + check.detail)));
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
