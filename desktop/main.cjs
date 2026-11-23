"use strict";

const { app, BrowserWindow, ipcMain, session } = require("electron");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
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
    createWindow();
  });
  app.on("window-all-closed", () => app.quit());
}
