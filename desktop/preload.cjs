"use strict";

const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("anyusbDesktop", {
  chooseFirmware: () => ipcRenderer.invoke("anyusb:firmware"),
  retry: () => ipcRenderer.invoke("anyusb:retry"),
});
