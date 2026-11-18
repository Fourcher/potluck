"use strict";
// Everything the chat page remembers lives in this browser (IndexedDB): chats, settings,
// usage totals and the folder you gave it. Nothing is stored on the Potluck computers.
const Store = (() => {
  let dbp;
  const open = () => dbp || (dbp = new Promise((res, rej) => {
    const r = indexedDB.open("potluck-chat", 1);
    r.onupgradeneeded = () => { const d = r.result; d.createObjectStore("chats", { keyPath: "id" }); d.createObjectStore("kv"); };
    r.onsuccess = () => res(r.result);
    r.onerror = () => rej(r.error);
  }));
  const tx = async (store, mode, fn) => {
    const d = await open();
    return new Promise((res, rej) => {
      const t = d.transaction(store, mode), s = t.objectStore(store), q = fn(s);
      t.oncomplete = () => res(q && q.result);
      t.onerror = () => rej(t.error);
    });
  };
  return {
    allChats: () => tx("chats", "readonly", s => s.getAll()),
    getChat: id => tx("chats", "readonly", s => s.get(id)),
    putChat: c => tx("chats", "readwrite", s => s.put(c)),
    delChat: id => tx("chats", "readwrite", s => s.delete(id)),
    clearChats: () => tx("chats", "readwrite", s => s.clear()),
    get: k => tx("kv", "readonly", s => s.get(k)),
    set: (k, v) => tx("kv", "readwrite", s => s.put(v, k)),
    del: k => tx("kv", "readwrite", s => s.delete(k)),
  };
})();
