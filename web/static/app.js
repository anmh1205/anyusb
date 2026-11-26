"use strict";

const $ = id => document.getElementById(id);
const mesh = "/v1/mesh";
const enc = encodeURIComponent;
const peerPath = id => mesh + "/peers/" + enc(id);
const state = {
  me: null,
  peer: "",
  peers: [],
  devices: [],
  shares: [],
  connections: [],
  discoveries: [],
  requests: [],
  doctor: [],
  localError: "",
  scanning: false,
  polling: false,
  pollError: false,
};
const openTechDetails = new Set();
let noticeTimer = null;

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
  const response = await fetch(path, {
    method,
    headers,
    body: data,
    cache: "no-store",
    signal: AbortSignal.timeout(20000),
  });
  const text = await response.text();
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    value = { message: text.trim() };
  }
  if (response.status === 401) {
    const localAuth = response.headers.get("X-AnyUSB-Local-Auth") === "required" || !value.code;
    if (localAuth && window.anyusbDesktop?.retry) {
      void window.anyusbDesktop.retry();
      const err = new Error("Dịch vụ AnyUSB vừa khởi động lại, đang tự kết nối lại…");
      err.localAuth = true;
      throw err;
    }
    const err = new Error(localAuth
      ? "Phiên AnyUSB hết hạn. Hãy chạy lại anyusb ui hoặc đóng rồi mở lại ứng dụng."
      : (value.message || "Máy từ xa đã từ chối hoặc hủy ghép cặp."));
    err.localAuth = localAuth;
    throw err;
  }
  if (!response.ok) throw new Error(value.message || response.statusText);
  return value;
}

function status(element, text, error = false, { sticky = false } = {}) {
  const node = $(element);
  if (element === "notice") {
    if (noticeTimer) {
      clearTimeout(noticeTimer);
      noticeTimer = null;
    }
    node.hidden = !text;
    node.classList.toggle("error", error);
    $("notice-text").textContent = text;
    if (text && !error && !sticky) {
      noticeTimer = setTimeout(() => {
        if ($("notice-text").textContent === text) node.hidden = true;
      }, 4200);
    }
    return;
  }
  node.hidden = !text;
  node.classList.toggle("error", error);
  node.textContent = text;
}

function action(fn) {
  return async event => {
    event?.preventDefault();
    const control = event?.currentTarget;
    if (control?.tagName === "BUTTON") control.disabled = true;
    try {
      await fn();
    } catch (err) {
      status("notice", err.message, true);
    } finally {
      if (control?.tagName === "BUTTON") control.disabled = false;
    }
  };
}

function el(tag, text = "", className = "") {
  const node = document.createElement(tag);
  node.textContent = text;
  if (className) node.className = className;
  return node;
}

function button(text, handler, { primary = false, danger = false, disabled = false, className = "" } = {}) {
  const classes = [primary ? "primary" : "", danger ? "danger" : "", className].filter(Boolean).join(" ");
  const node = el("button", text, classes);
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

async function copyText(text, labelText = text) {
  try {
    await navigator.clipboard.writeText(text);
    status("notice", "Đã chép: " + labelText);
  } catch {
    status("notice", "Không tự chép được, hãy chọn và nhấn Ctrl+C: " + labelText, true);
  }
}

function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value < 1024) return value + " B";
  const units = ["KB", "MB", "GB"];
  let scaled = value / 1024;
  let unit = 0;
  while (scaled >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit++;
  }
  return scaled.toFixed(scaled >= 10 ? 1 : 2) + " " + units[unit];
}

function label(device) {
  return [device.manufacturer, device.product].filter(Boolean).join(" ") || "Thiết bị USB";
}

function peerName(id) {
  if (!id) return "";
  return state.peers.find(p => p.id === id)?.name || state.peers.find(p => p.id === id)?.endpoint || id.slice(0, 8);
}

const profileLabels = {
  serial: "Serial",
  stlink: "ST-Link",
  jlink: "J-Link",
  "cmsis-dap": "CMSIS-DAP",
  "debug-probe": "Debug probe",
  storage: "Lưu trữ",
  hid: "HID",
  network: "Mạng USB",
  audio: "Âm thanh",
  video: "Video",
  printer: "Máy in",
  dfu: "DFU",
  "cdc-control": "CDC control",
  "cdc-data": "CDC data chưa ghép chức năng",
  unknown: "USB chưa phân loại",
};

function profileText(device) {
  return (device.profiles || [])
    .map(p => profileLabels[p] || p)
    .filter((p, i, all) => all.indexOf(p) === i)
    .join(" · ");
}

function riskText(device) {
  const risks = {
    storage: "Ngắt đột ngột có thể làm hỏng dữ liệu đang ghi.",
    input: "Thiết bị này có thể tạo thao tác nhập trên máy nhận.",
    network: "Thiết bị này có thể thay đổi đường mạng của máy nhận.",
    timing: "Thiết bị thời gian thực cần được kiểm chứng theo đúng workload.",
    topology: "Không chia sẻ USB hub hoặc bộ điều khiển USB.",
  };
  return risks[device.risk] || "";
}

const probeGuidance = {
  stlink: "Kiểm tra bằng STM32CubeProgrammer (chỉ liệt kê probe, không ghi target).",
  jlink: "Kiểm tra bằng J-Link Commander (chỉ ShowEmuList USB, không cập nhật phần mềm probe).",
  "cmsis-dap": "Kiểm tra bằng pyOCD (chỉ liệt kê probe, không halt/flash target).",
};

const networkLabels = {
  LAN: "LAN",
  Tailscale: "Tailscale",
  Saved: "Đã lưu",
};

function card(name, chips, detail, statusType, actions, notes = []) {
  const info = el("div", "", "info");
  const titleRow = el("div", "", "card-title");
  titleRow.append(el("h3", name));
  for (const text of chips) {
    if (text) titleRow.append(el("span", text, "chip"));
  }
  const meta = el("p", "", "meta");
  meta.append(el("span", "", "state-dot " + statusType), document.createTextNode(detail));
  info.append(titleRow, meta, ...notes);
  const controls = el("div", "", "actions");
  controls.append(...actions);
  const item = el("article", "", "device-card");
  item.append(info, controls);
  return item;
}

function empty(text) {
  return el("div", text, "empty");
}

function renderSelfInfo() {
  if (!state.me) return;
  $("agent").textContent = state.me.name || "Máy này";
  const netNode = $("agent-net");
  const ts = state.me.tailscale;
  if (ts === "running") {
    netNode.hidden = false;
    netNode.className = "agent-net";
    netNode.textContent = "Tailscale đã sẵn sàng";
  } else if (ts && ts !== "missing") {
    netNode.hidden = false;
    netNode.className = "agent-net warn";
    netNode.textContent = "Tailscale chưa kết nối (" + ts + ")";
  } else {
    netNode.hidden = false;
    netNode.className = "agent-net";
    netNode.textContent = "Chế độ mạng LAN";
  }

  const addrs = state.me.addresses || [];
  show("self-addresses", addrs, () => {
    if (!addrs.length) return [el("span", "Chưa có địa chỉ LAN/Tailscale khả dụng.", "small")];
    return addrs.map(addr => {
      const chip = el("button", addr + " · Chép", "addr-chip");
      chip.type = "button";
      chip.onclick = () => void copyText(addr, addr);
      return chip;
    });
  });
}

function renderOnboarding() {
  const hiddenByUser = localStorage.getItem("anyusb-hide-guide") === "1";
  const shouldShow = state.peers.length === 0 ? !hiddenByUser : localStorage.getItem("anyusb-force-guide") === "1";
  $("onboarding").hidden = !shouldShow;
}

function doctorRemediation(check) {
  const detail = (check.detail || "").toLowerCase();
  if (check.name === "usbip2_ude" || check.name === "usbip2_filter" || check.name === "VBoxUSBMon") {
    return check.name + ": Driver USB/IP chưa chạy. Nếu bạn vừa cài AnyUSB trên Windows, hãy khởi động lại máy tính.";
  }
  if (check.name === "usbipd") {
    return "usbipd: Dịch vụ chia sẻ USB đang dừng. Hãy khởi động lại dịch vụ usbipd hoặc khởi động lại Windows.";
  }
  if (check.name === "usbip" && detail.includes("not found for kernel")) {
    return "usbip: Thiếu công cụ khớp kernel đang chạy. Chạy: sudo apt install linux-tools-$(uname -r)";
  }
  if (check.name === "usbip-host" || check.name === "vhci-hcd") {
    return check.name + ": Thiếu module kernel (" + check.detail + ").";
  }
  if (check.name === "TCP 3240 peer isolation") {
    return "Cần tắt hoặc giới hạn rule tường lửa TCP 3240 từ máy khác để bảo vệ kết nối AnyUSB.";
  }
  return check.name + ": " + check.detail;
}

function renderHealthBanner() {
  const issues = (state.doctor || []).filter(c => !c.ok && !c.optional);
  $("health-banner").hidden = issues.length === 0;
  if (!issues.length) return;
  show("health-list", issues, () => issues.map(c => el("li", doctorRemediation(c))));
}

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
  const shareCount = state.shares.length;
  const connCount = state.connections.length;
  const localBadge = [];
  if (shareCount) localBadge.push(shareCount + " chia sẻ");
  if (connCount) localBadge.push(connCount + " đang nhận");
  $("local-badge").textContent = localBadge.join(" · ");

  const online = new Map(state.discoveries.map(p => [p.id, networkLabels[p.network] || p.network]));
  show("peers", [state.peers, state.peer, state.discoveries, state.connections], () => {
    if (!state.peers.length) return [el("p", "Chưa có máy nào", "small")];
    return state.peers.map(p => {
      const peerConns = state.connections.filter(c => c.peer === p.id);
      const netState = online.get(p.id) || "Ngoại tuyến";
      const detail = peerConns.length ? netState + " · " + peerConns.length + " USB" : netState;
      return navButton(p.name || p.endpoint, detail, state.peer === p.id, () => selectPeer(p.id));
    });
  });

  const available = state.discoveries.filter(p => !state.peers.some(saved => saved.id === p.id));
  show("discoveries", [available, state.requests], () => available.map(p => {
    const last = state.requests.findLast(r => !r.incoming && r.peer.id === p.id);
    const waiting = last?.status === "pending";
    const detail = waiting ? "Đang chờ" : last?.status === "declined" ? "Đã từ chối · Thử lại" : (networkLabels[p.network] || p.network);
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
    message.append(
      el("h3", (r.peer.name || r.peer.endpoint) + (r.incoming ? " muốn ghép cặp" : " đang chờ đồng ý")),
      el("span", r.code, "request-code"),
      el("p", r.incoming ? "Chỉ đồng ý nếu mã 4 số trên hai máy trùng nhau." : "Kiểm tra mã trên máy kia rồi bấm Đồng ý trên máy đó.")
    );
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
  const outgoing = state.connections;
  $("active-outgoing").hidden = outgoing.length === 0 || Boolean(state.peer);
  if (outgoing.length && !state.peer) {
    show("active-outgoing", [outgoing, state.peers], () => {
      const text = el("span", "Máy này đang nhận " + outgoing.length + " USB từ xa:");
      const links = el("div", "", "outgoing-links");
      const seenPeers = new Set();
      for (const conn of outgoing) {
        if (seenPeers.has(conn.peer)) continue;
        seenPeers.add(conn.peer);
        links.append(button("Mở " + peerName(conn.peer), () => selectPeer(conn.peer)));
      }
      return [text, links];
    });
  }

  show("local", [state.devices, state.shares, state.peers, error], () => {
    if (error) return [empty(error + " · Xem Chẩn đoán để kiểm tra driver.")];
    if (!state.devices.length) return [empty("Cắm USB vào máy này để chia sẻ cho máy khác.")];
    return state.devices.map(device => {
      const shared = state.shares.find(s => s.id === device.id);
      const busy = Boolean(shared?.lease_owner);
      const holder = busy ? peerName(shared.lease_owner) : "";
      const blocked = device.share_policy === "block";
      const chips = [
        device.vendor_id && device.product_id ? device.vendor_id + ":" + device.product_id : "",
        device.bus_id ? "Port " + device.bus_id : "",
        device.serial ? "SN " + device.serial : "",
      ];
      const detail = [
        busy ? "Đang dùng bởi " + holder : blocked ? "USB Hub · Không thể chia sẻ toàn thiết bị" : shared ? "Đang chia sẻ (chờ kết nối)" : "Chỉ dùng trên máy này",
        profileText(device),
      ].filter(Boolean).join(" · ");
      const notes = [];
      if (riskText(device)) notes.push(el("p", riskText(device), "meta"));
      if (busy) {
        notes.push(el("p", "Để dừng chia sẻ ngay, hãy ngắt kết nối ở máy " + holder + " hoặc bấm Buộc nhả.", "meta"));
      }
      const actions = [button(shared ? "Dừng chia sẻ" : "Cho phép dùng", async () => {
        await api(mesh + "/shares/" + enc(device.id), shared ? "DELETE" : "PUT");
        status("notice", shared ? "Đã dừng chia sẻ " + label(device) : "Đã cho phép máy khác dùng " + label(device));
        await poll();
      }, { primary: !shared, disabled: busy || (!shared && blocked) })];
      if (busy) {
        actions.push(button("Buộc nhả", async () => {
          if (!window.confirm("Buộc máy " + holder + " nhả thiết bị và chặn máy đó giành lại trong 60 giây?")) return;
          await api(mesh + "/shares/" + enc(device.id) + "/force-release", "POST");
          status("notice", "Đã buộc nhả thiết bị khỏi " + holder + " (tạm khóa 60 giây).");
          await poll();
        }, { danger: true }));
      }
      const dot = busy ? "wait" : shared && !blocked ? "good" : "";
      return card(label(device), chips, detail, dot, actions, notes);
    });
  });

  const absentShares = state.shares.filter(s => !s.present && !state.devices.some(d => d.id === s.id));
  $("local-absent-wrap").hidden = absentShares.length === 0 || Boolean(state.peer);
  show("local-absent", absentShares, () => absentShares.map(s => {
    return card(
      "Thiết bị đã rút cáp",
      [s.id.slice(0, 8)],
      "Vẫn đang bật chia sẻ; sẽ sẵn sàng lại khi cắm đúng cổng USB cũ.",
      "",
      [button("Dừng chia sẻ", async () => {
        await api(mesh + "/shares/" + enc(s.id), "DELETE");
        status("notice", "Đã xóa chia sẻ thiết bị đã rút cáp.");
        await poll();
      })]
    );
  }));
}

const linkLabels = {
  attached: "Đã gắn vào máy này",
  connecting: "Đang kết nối",
  reconnecting: "Đang tự nối lại",
  lost: "Đang chờ thiết bị trở lại",
  blocked: "Cần bạn xử lý",
};
const phaseLabels = {
  peer_lookup: "tìm máy",
  lease: "xin quyền thiết bị",
  tunnel: "mở tunnel",
  usbip_attach: "gắn USB/IP",
  os_enumeration: "hệ điều hành nhận driver",
  ready: "sẵn sàng",
  backoff: "chờ thử lại",
  blocked: "cần người dùng xử lý",
};
const readinessLabels = {
  attached: "USB đã gắn, chưa xác minh driver",
  enumerating: "Đang nhận diện chức năng USB",
  ready: "Driver sẵn sàng",
  degraded: "Một phần chức năng chưa sẵn sàng",
  driver_missing: "Thiếu driver",
  error: "Lỗi thiết bị/driver",
  unknown: "Đã gắn nhưng chưa xác minh",
};

function renderPeerBar(selected) {
  const peer = state.peers.find(p => p.id === selected);
  $("peer-bar").hidden = !peer;
  if (!peer) {
    $("peer-edit-form").hidden = true;
    return;
  }
  const disc = state.discoveries.find(d => d.id === peer.id);
  const pill = $("peer-status-pill");
  if (disc) {
    pill.className = "pill good";
    pill.textContent = "Trực tuyến · " + (networkLabels[disc.network] || disc.network);
  } else {
    pill.className = "pill";
    pill.textContent = "Đang kiểm tra kết nối";
  }
  $("peer-endpoint").textContent = peer.endpoint || "Chưa đặt địa chỉ";
}

async function refreshRemote() {
  const selected = state.peer;
  if (!selected) return;
  renderPeerBar(selected);
  const [devices, connections] = await Promise.allSettled([
    api(peerPath(selected) + "/devices"),
    api(mesh + "/connections"),
  ]);
  if (selected !== state.peer) return;
  if (connections.status === "fulfilled" && Array.isArray(connections.value)) {
    state.connections = connections.value;
  }
  const shared = devices.value || [];
  const links = state.connections.filter(c => c.peer === selected);
  const entries = [...shared];
  for (const link of links) {
    if (!entries.some(d => d.id === link.device)) {
      entries.push(link.device_info || { id: link.device, product: link.import?.name || "Thiết bị đã kết nối" });
    }
  }
  if (devices.status === "fulfilled") {
    const pill = $("peer-status-pill");
    if (pill && pill.textContent === "Đang kiểm tra kết nối") {
      pill.className = "pill good";
      pill.textContent = "Trực tuyến";
    }
  } else if (devices.status === "rejected" && !$("peer-status-pill").classList.contains("good")) {
    const pill = $("peer-status-pill");
    pill.className = "pill wait";
    pill.textContent = "Không liên hệ được";
  }

  const snapshotKey = [selected, shared, links, devices.status, [...openTechDetails]];
  const region = $("remote");
  const snapshot = JSON.stringify(snapshotKey);
  if (region.dataset.snapshot === snapshot) return;
  region.dataset.snapshot = snapshot;

  if (!entries.length) {
    const msg = devices.status === "rejected"
      ? (devices.reason?.message || "Không thể liên hệ máy này. AnyUSB sẽ tự thử lại.")
      : "Máy này chưa chia sẻ thiết bị USB nào. Trên máy kia, vào \"Máy này\" và bấm \"Cho phép dùng\".";
    region.replaceChildren(empty(msg));
    return;
  }

  region.replaceChildren(...entries.map(device => {
    const current = links.find(c => c.device === device.id);
    const busy = Boolean(device.lease_owner && device.lease_owner !== state.me?.id);
    const ports = current?.import?.serial_ports || [];
    const bindings = current?.import?.local_bindings || [];
    const online = current?.state === "attached";
    const readiness = current?.import?.readiness;
    const chips = [
      device.vendor_id && device.product_id ? device.vendor_id + ":" + device.product_id : "",
      device.serial ? "SN " + device.serial : "",
    ];

    let summary = current
      ? linkLabels[current.state] || current.state
      : device.present ? "Sẵn sàng kết nối" : "Đang rút cáp USB ở máy chia sẻ";
    if (online && readiness) summary += " · " + (readinessLabels[readiness] || readiness);
    if (current?.phase && current.phase !== "ready") summary += " · " + (phaseLabels[current.phase] || current.phase);
    if (current?.attempt) summary += " · lần " + current.attempt;
    if (profileText(device)) summary += " · " + profileText(device);
    if (!current && device.share_policy === "block") summary = "Không thể chia sẻ toàn thiết bị";
    if (busy) summary = "Đang được máy " + peerName(device.lease_owner) + " sử dụng";

    const connect = button(current ? "Ngắt kết nối" : "Kết nối", async () => {
      if (current) {
        await api(mesh + "/connections/" + enc(current.id), "DELETE");
        status("notice", "Đã ngắt kết nối " + label(device));
      } else {
        let confirmRisk = false;
        if (device.share_policy === "confirm") {
          confirmRisk = window.confirm((riskText(device) || "Thiết bị này cần xác nhận rủi ro.") + "\n\nTiếp tục kết nối?");
          if (!confirmRisk) return;
        }
        await api(mesh + "/connections", "POST", { peer: selected, device: device.id, confirm_risk: confirmRisk });
        status("notice", "Đang kết nối " + label(device) + "…");
      }
      await refreshRemote();
    }, { primary: !current, disabled: !current && (!device.present || busy || device.lease_mode === "esp" || device.share_policy === "block") });

    const actions = [connect];
    if (current?.state === "blocked" && current.last_failure?.code === "risk_required" && device.present && !busy) {
      actions.unshift(button("Xác nhận và kết nối", async () => {
        if (!window.confirm(riskText(device) || "Thiết bị này cần xác nhận rủi ro.")) return;
        await api(mesh + "/connections", "POST", { peer: selected, device: device.id, confirm_risk: true });
        await refreshRemote();
      }, { primary: true }));
    }

    const notes = [];
    if (online && ports.length) {
      const localPorts = el("div", "", "local-ports");
      localPorts.append(el("span", "Cổng trên máy này:"));
      for (const port of ports) {
        localPorts.append(el("code", port), button("Chép", () => copyText(port, port), { className: "copy-btn" }));
      }
      notes.push(localPorts, el("p", "Chọn cổng này trong VS Code, ESP-IDF hoặc Arduino IDE của bạn.", "meta"));
    } else if (online) {
      notes.push(el("p", "USB đã được gắn vào hệ điều hành; thiết bị này không cần COM/tty. Hãy dùng driver và công cụ gốc của thiết bị.", "meta"));
    } else if (current && current.state !== "blocked") {
      notes.push(el("p", "AnyUSB sẽ tự nối lại khi máy từ xa, mạng hoặc thiết bị sẵn sàng; không cần bấm Kết nối lại.", "meta"));
    }

    if (online && readiness === "driver_missing") {
      notes.push(el("p", "Hệ điều hành trên máy này chưa có driver cho thiết bị. Hãy cài driver USB của nhà sản xuất rồi đợi vài giây.", "link-error"));
    }

    const probeProfile = (device.profiles || []).find(profile => probeGuidance[profile]);
    if (online && probeProfile) {
      notes.push(el("p", probeGuidance[probeProfile], "meta"));
      actions.push(button("Kiểm tra " + profileLabels[probeProfile], async () => {
        const result = await api(mesh + "/connections/" + enc(current.id) + "/probe", "POST", { profile: probeProfile });
        const detail = result.ok
          ? profileLabels[probeProfile] + " đã nhận đúng serial " + (result.serial || "")
          : (result.error || "Công cụ gốc chưa xác nhận đúng thiết bị.");
        status("notice", detail, !result.ok, { sticky: true });
      }));
    }

    if (riskText(device)) notes.push(el("p", riskText(device), "meta"));
    if (current?.error) notes.push(el("p", current.error, "link-error"));

    const hasTech = current || bindings.length;
    if (hasTech) {
      const details = document.createElement("details");
      details.className = "tech-details";
      const shouldDefaultOpen = readiness === "driver_missing" || readiness === "error" || readiness === "degraded" || current?.state === "blocked";
      details.open = openTechDetails.has(device.id) || shouldDefaultOpen;
      details.ontoggle = () => {
        if (details.open) openTechDetails.add(device.id);
        else openTechDetails.delete(device.id);
      };
      const summaryEl = el("summary", "Chi tiết kỹ thuật & đường truyền");
      details.append(summaryEl);

      if (online && bindings.length) {
        const localBindings = el("p", "", "local-ports");
        const descriptions = bindings.map(binding => [
          binding.kind + ": " + binding.id,
          binding.driver,
          readinessLabels[binding.status] || binding.status,
        ].filter(Boolean).join(" · "));
        localBindings.append(el("span", "Liên kết hệ điều hành"), ...descriptions.map(value => el("code", value)));
        details.append(localBindings);
      }
      if (current?.next_retry_at) {
        details.append(el("p", "Thử lại lúc " + new Date(current.next_retry_at).toLocaleTimeString(), "meta"));
      }
      if (current?.last_failure) {
        details.append(el("p", current.last_failure.code + " · " + current.last_failure.operation, "meta"));
      }
      if (current?.stats && (current.stats.upload_bytes || current.stats.download_bytes || current.stats.reconnect_count)) {
        const up = current.stats.upload_bytes || 0;
        const down = current.stats.download_bytes || 0;
        details.append(el("p",
          "Tunnel ↑ " + formatBytes(up) + " (" + up + " B) · ↓ " + formatBytes(down) + " (" + down + " B)" +
          " · nối lại " + (current.stats.reconnect_count || 0) + " lần" +
          (current.stats.disconnect_reason ? " · " + current.stats.disconnect_reason : ""), "meta"));
      }
      if (current?.stats?.last_progress) {
        details.append(el("p", "Truyền dữ liệu gần nhất lúc " + new Date(current.stats.last_progress).toLocaleTimeString(), "meta"));
      }
      if (current?.stats?.lease_age_ms > 0) {
        details.append(el("p", "Đã giữ quyền sử dụng " + Math.floor(current.stats.lease_age_ms / 1000) + " giây", "meta"));
      }
      notes.push(details);
    }

    const dot = online && readiness === "ready" ? "good"
      : online && (readiness === "driver_missing" || readiness === "error") ? "bad"
      : online || (device.present && !current) ? "good"
      : current?.state === "blocked" ? "bad"
      : current ? "wait" : "";

    return card(label(device), chips, summary, dot, actions, notes);
  }));
}

async function selectPeer(id) {
  if (state.peer !== id) {
    $("remote").dataset.snapshot = "";
    $("remote").replaceChildren();
    $("peer-edit-form").hidden = true;
  }
  state.peer = id;
  $("local-section").hidden = Boolean(id);
  $("remote-section").hidden = !id;
  const peer = state.peers.find(p => p.id === id);
  $("page-title").textContent = peer ? peer.name || "USB từ xa" : "Thiết bị USB trên máy này";
  $("page-detail").textContent = peer
    ? "Chọn thiết bị muốn gắn về máy của bạn"
    : "Bật \"Cho phép dùng\" cho thiết bị bạn muốn chia sẻ với máy khác";
  renderNav();
  renderLocal(state.localError);
  if (id) await refreshRemote();
  else $("peer-bar").hidden = true;
}

async function checkDoctor() {
  try {
    state.doctor = await api("/v1/doctor");
    renderHealthBanner();
  } catch {
    // Keep last doctor state if poll failed transiently.
  }
}

async function poll() {
  if (state.polling) return;
  state.polling = true;
  try {
    const results = await Promise.allSettled([
      api("/v1/devices"),
      api(mesh + "/shares"),
      api(mesh + "/peers"),
      api(mesh + "/pair-requests"),
      api(mesh + "/connections"),
    ]);
    const [local, shares, peers, requests, connections] = results;
    const authFailure = results.find(r => r.status === "rejected" && r.reason?.localAuth);
    if (authFailure) throw authFailure.reason;
    if (results.every(r => r.status === "rejected")) throw results[0].reason;

    state.localError = local.status === "rejected" ? (local.reason?.message || "Lỗi đọc thiết bị USB") : "";
    state.devices = local.value || [];
    state.shares = shares.value || [];
    const previous = state.peers;
    state.peers = peers.value || state.peers;
    state.requests = requests.value || [];
    if (connections.status === "fulfilled" && Array.isArray(connections.value)) {
      state.connections = connections.value;
    }
    if (state.peer && !state.peers.some(p => p.id === state.peer)) {
      await selectPeer("");
    }
    if (state.pollError) {
      state.pollError = false;
      status("notice", "");
    }
    renderOnboarding();
    renderLocal(state.localError);
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
    renderPeerBar(state.peer);
    const unpairedCount = state.discoveries.filter(p => !state.peers.some(saved => saved.id === p.id)).length;
    $("scan").textContent = unpairedCount
      ? ""
      : "Chưa thấy máy mới trên LAN/Tailscale. Bạn có thể ghép cặp thủ công bên dưới.";
  } catch (err) {
    $("scan").textContent = err.message;
  } finally {
    state.scanning = false;
  }
}

$("notice-close").onclick = () => status("notice", "");
$("hide-guide").onclick = () => {
  localStorage.setItem("anyusb-hide-guide", "1");
  localStorage.removeItem("anyusb-force-guide");
  renderOnboarding();
};
$("toggle-guide").onclick = () => {
  localStorage.removeItem("anyusb-hide-guide");
  const isHidden = $("onboarding").hidden;
  if (isHidden) localStorage.setItem("anyusb-force-guide", "1");
  else {
    localStorage.removeItem("anyusb-force-guide");
    localStorage.setItem("anyusb-hide-guide", "1");
  }
  renderOnboarding();
};

$("health-recheck").onclick = action(async () => {
  await checkDoctor();
  const issues = (state.doctor || []).filter(c => !c.ok && !c.optional);
  if (!issues.length) status("notice", "Tất cả dịch vụ và driver đều đã sẵn sàng.");
});

$("this-machine").onclick = action(() => selectPeer(""));
$("discover").onclick = action(discover);
$("refresh").onclick = action(async () => {
  await Promise.all([poll(), discover(), checkDoctor()]);
});

$("peer-edit-toggle").onclick = () => {
  const peer = state.peers.find(p => p.id === state.peer);
  if (!peer) return;
  $("peer-endpoint-input").value = peer.endpoint || "";
  $("peer-edit-form").hidden = !$("peer-edit-form").hidden;
  if (!$("peer-edit-form").hidden) $("peer-endpoint-input").focus();
};
$("peer-edit-cancel").onclick = () => {
  $("peer-edit-form").hidden = true;
};
$("peer-edit-form").onsubmit = action(async () => {
  const endpoint = $("peer-endpoint-input").value.trim();
  const updated = await api(peerPath(state.peer), "PUT", { endpoint });
  $("peer-edit-form").hidden = true;
  status("notice", "Đã cập nhật địa chỉ cho " + (updated.name || updated.endpoint));
  await poll();
});
$("peer-revoke").onclick = action(async () => {
  const peer = state.peers.find(p => p.id === state.peer);
  if (!peer) return;
  if (!window.confirm("Hủy ghép cặp với máy " + (peer.name || peer.endpoint) + "? Các kết nối USB với máy này sẽ bị ngắt.")) return;
  await api(peerPath(peer.id), "DELETE");
  status("notice", "Đã hủy ghép cặp với " + (peer.name || peer.endpoint));
  await selectPeer("");
  await poll();
});

$("pair").onsubmit = action(async () => {
  const peer = await api(mesh + "/pair", "POST", Object.fromEntries(new FormData($("pair"))));
  $("pair").elements.code.value = "";
  status("notice", "Đã ghép cặp thành công với " + (peer.name || peer.endpoint));
  await poll();
  if (peer.id) await selectPeer(peer.id);
});
$("invite").onclick = action(async () => {
  const invite = await api(mesh + "/invite", "POST");
  $("invite-code").textContent = "Mã mời: " + invite.code + " · hết hạn lúc " + new Date(invite.expires).toLocaleTimeString();
});
$("doctor").onclick = action(async () => {
  const checks = await api("/v1/doctor");
  state.doctor = checks;
  renderHealthBanner();
  $("checks").replaceChildren(...checks.map(check =>
    el("li", (check.ok ? "OK" : check.optional ? "Tùy chọn" : "Cần kiểm tra") + " · " + check.name + " · " + check.detail)));
});
$("diagnostics").onclick = action(async () => {
  const bundle = await api("/v1/diagnostics");
  const link = document.createElement("a");
  const url = URL.createObjectURL(new Blob([JSON.stringify(bundle, null, 2) + "\n"], { type: "application/json" }));
  link.href = url;
  link.download = "anyusb-diagnostics-" + new Date().toISOString().replaceAll(":", "-") + ".json";
  link.click();
  URL.revokeObjectURL(url);
});

action(async () => {
  if (!token) {
    status("notice", "Hãy mở AnyUSB từ menu ứng dụng để bắt đầu.", true);
    return;
  }
  state.me = await api(mesh + "/info");
  renderSelfInfo();
  await selectPeer("");
  await poll();
  void discover();
  void checkDoctor();
  window.setInterval(() => poll().catch(err => {
    state.pollError = true;
    status("notice", err.message, true);
  }), 2600);
  window.setInterval(() => void discover(), 12000);
  window.setInterval(() => {
    api(mesh + "/info").then(info => { state.me = info; renderSelfInfo(); }).catch(() => {});
  }, 15000);
})();
