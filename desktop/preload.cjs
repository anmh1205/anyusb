"use strict";

const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("anyusbDesktop", {
  retry: () => ipcRenderer.invoke("anyusb:retry"),
});
