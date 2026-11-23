"use strict";

const { app, BrowserWindow, dialog, ipcMain, session } = require("electron");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
const fs = require("node:fs/promises");
const path = require("node:path");
const { pathToFileURL } = require("node:url");

const execute = promisify(execFile);
const startupPage = pathToFileURL(path.join(__dirname, "startup.html")).href;
let window;
let dashboardOrigin = "";
app.setName("AnyUSB");

function dashboardURL(value) {
  const url = new URL(value.trim());
  if (url.protocol !== "http:" || url.hostname !== "127.0.0.1" || !url.port || url.pathname !== "/" ||
      url.username || url.password || url.search || !/^#[a-f0-9]{64}$/.test(url.hash)) {
    throw new Error("Invalid dashboard URL");
  }
  return url;
}

async function openDashboard() {
  const target = window;
  if (!target || target.isDestroyed()) return;
  dashboardOrigin = "";
  const [file, args] = process.platform === "win32"
    ? [path.join(path.dirname(process.execPath), "anyusb.exe"), ["desktop-url"]]
    : ["/usr/bin/pkexec", ["--disable-internal-agent", "/usr/lib/anyusb/open-dashboard"]];
  try {
    const { stdout } = await execute(file, args, { windowsHide: true, timeout: 6000, maxBuffer: 8192 });
    if (target.isDestroyed()) return;
    const url = dashboardURL(stdout);
    dashboardOrigin = url.origin;
    await target.loadURL(url.href);
  } catch {
    dashboardOrigin = "";
    if (!target.isDestroyed()) await target.loadURL(startupPage);
  }
}

function isMainFrame(event) {
  return window && event.sender === window.webContents && event.senderFrame === window.webContents.mainFrame;
}

function relativeFirmwarePath(value) {
  if (typeof value !== "string") throw new Error("Tên file firmware không hợp lệ.");
  const name = value.replaceAll("\\", "/").replace(/^\.\//, "");
  if (!name || path.posix.isAbsolute(name) || name.includes(":") || name.includes("\0") ||
      name === ".." || name.startsWith("../") || path.posix.normalize(name) !== name) {
    throw new Error("Các file nạp phải nằm trong thư mục của manifest.");
  }
  return name;
}

async function chooseFirmware() {
  const result = await dialog.showOpenDialog(window, {
    title: "Chọn flasher_args.json", properties: ["openFile"], filters: [{ name: "ESP-IDF manifest", extensions: ["json"] }],
  });
  if (result.canceled) return null;
  const entry = await fs.realpath(result.filePaths[0]);
  const root = path.dirname(entry);
  const info = await fs.stat(entry);
  if (!info.isFile() || info.size > 64 * 1024) throw new Error("Manifest ESP-IDF không hợp lệ.");
  const manifest = JSON.parse(await fs.readFile(entry, "utf8"));
  const values = Object.values(manifest.flash_files || {});
  if (!values.length || values.length > 64) throw new Error("Manifest cần từ 1 đến 64 vùng nạp.");
  const files = [];
  let bytes = 0;
  for (const name of new Set(values)) {
    const source = await fs.realpath(path.join(root, relativeFirmwarePath(name)));
    const rel = path.relative(root, source);
    if (rel === ".." || rel.startsWith(".." + path.sep) || path.isAbsolute(rel)) {
      throw new Error("Các file nạp phải nằm trong thư mục của manifest.");
    }
    const file = await fs.stat(source);
    bytes += file.size;
    if (!file.isFile() || !file.size || bytes > 127 * 1024 * 1024) throw new Error("File firmware không hợp lệ hoặc vượt giới hạn 127 MiB.");
    files.push({ name, data: new Uint8Array(await fs.readFile(source)) });
  }
  return { name: path.basename(entry), manifest, files };
}

function createWindow() {
  window = new BrowserWindow({
    width: 1080, height: 750, minWidth: 620, minHeight: 490, title: "AnyUSB", autoHideMenuBar: true,
    backgroundColor: "#faf9f6",
    webPreferences: { preload: path.join(__dirname, "preload.cjs"), nodeIntegration: false, contextIsolation: true, sandbox: true },
  });
  window.removeMenu();
  window.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
  window.webContents.on("will-navigate", (event, target) => {
    if (target !== startupPage && target !== dashboardOrigin + "/") event.preventDefault();
  });
  window.webContents.on("will-redirect", event => event.preventDefault());
  window.on("closed", () => { window = null; });
  void openDashboard();
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.on("second-instance", () => {
    if (window?.isMinimized()) window.restore();
    window?.focus();
  });
  app.whenReady().then(() => {
    session.defaultSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
    session.defaultSession.setPermissionCheckHandler(() => false);
    ipcMain.handle("anyusb:retry", event => {
      if (isMainFrame(event) && event.senderFrame.url === startupPage) void openDashboard();
    });
    ipcMain.handle("anyusb:firmware", event => {
      if (!isMainFrame(event) || !dashboardOrigin || event.senderFrame.url !== dashboardOrigin + "/") {
        throw new Error("Unexpected firmware request");
      }
      return chooseFirmware();
    });
    createWindow();
  });
  app.on("window-all-closed", () => app.quit());
}
