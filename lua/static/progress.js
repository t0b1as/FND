// Persistente Upload-Fortschrittsanzeige.
// Aktive Uploads werden in localStorage gehalten und auf JEDER Seite unten
// als Leiste angezeigt — übersteht Reload und Seitenwechsel. Der Block-Upload
// selbst läuft auf der Upload-Seite; die serverseitige Finalisierung wird hier
// per Status-Poll verfolgt, sodass man die Seite wechseln kann.

(function () {
  const LS_KEY = "fundus_uploads";

  // localStorage ist in manchen Kontexten (Privatmodus, Sandbox, deaktivierte
  // Cookies) blockiert. Dann schlagen set/get still fehl und die Anzeige bliebe
  // bei 0% haengen. Einmal testen; bei Fehlschlag auf ein In-Memory-Objekt
  // ausweichen, das fuer die Dauer der Seite haelt (uebersteht keinen Reload,
  // aber die Live-Anzeige funktioniert).
  let lsOK = false;
  try {
    const probe = "__fundus_ls_probe__";
    localStorage.setItem(probe, "1");
    localStorage.removeItem(probe);
    lsOK = true;
  } catch (e) {
    lsOK = false;
  }
  let memStore = {};
  let lastRender = 0; // Drossel-Zeitstempel für render() (siehe progress())

  function load() {
    if (!lsOK) return memStore;
    try { return JSON.parse(localStorage.getItem(LS_KEY) || "{}"); }
    catch (e) { return memStore; }
  }
  function save(obj) {
    if (!lsOK) { memStore = obj; return; }
    try { localStorage.setItem(LS_KEY, JSON.stringify(obj)); }
    catch (e) { lsOK = false; memStore = obj; }
  }

  // Öffentliche API, die die Upload-Funktionen nutzen, um Fortschritt zu melden.
  window.FundusProgress = {
    // Upload registrieren/aktualisieren
    set(uploadId, data) {
      const all = load();
      all[uploadId] = Object.assign(all[uploadId] || {}, data, { ts: Date.now() });
      save(all);
      render();
    },
    // Block-Fortschritt (0..1) setzen. render() wird gedrosselt (max ~alle
    // 300ms), damit der Upload-Loop nicht bei jedem Block durch DOM-Neuaufbau
    // + localStorage-Schreiben ausgebremst wird.
    progress(uploadId, p) {
      const all = load();
      if (all[uploadId]) {
        all[uploadId].progress = p;
        all[uploadId].ts = Date.now();
        save(all);
        const now = Date.now();
        if (now - lastRender > 300 || p >= 1) {
          lastRender = now;
          render();
        }
      }
    },
    // Auf serverseitige Finalisierung umschalten
    finalizing(uploadId) {
      this.set(uploadId, { state: "finalizing" });
    },
    done(uploadId) {
      const all = load(); delete all[uploadId]; save(all); render();
    },
    remove(uploadId) { this.done(uploadId); },
  };

  function barHtml(id, u) {
    // Während der Finalisierung den echten Chunking-Fortschritt zeigen, falls
    // bekannt — sonst eine unbestimmte 100%-Leiste mit "wird verarbeitet…".
    let pct, label, cls;
    if (u.state === "error") {
      pct = 0;
      label = "Fehler: " + (u.error || "");
      cls = "fp-bar-err";
    } else if (u.state === "finalizing") {
      cls = "fp-bar-fin";
      if (u.chunksTotal > 0) {
        pct = Math.round((u.chunksDone / u.chunksTotal) * 100);
        const phaseLabel = ({
          lesen: "Daten lesen", chunking: "Verarbeiten",
          manifest: "Verzeichnis", index: "Abschluss"
        })[u.phase] || "Verarbeiten";
        label = phaseLabel + " " + u.chunksDone + "/" + u.chunksTotal + " (" + pct + "%)";
      } else {
        pct = 100;
        label = "wird verarbeitet…";
      }
    } else if (u.state === "downloading") {
      // Download-Balken identisch zum Upload-Balken (gleiche Optik/Prozent).
      pct = Math.round((u.progress || 0) * 100);
      label = "↓ " + pct + "%";
      cls = "";
    } else {
      pct = Math.round((u.progress || 0) * 100);
      label = pct + "%";
      cls = "";
    }
    return `<div class="fp-item" data-id="${id}">
      <div class="fp-row">
        <span class="fp-name">${(u.name || "Datei")}</span>
        <span class="fp-label">${label}</span>
        <span class="fp-x" onclick="FundusProgress.remove('${id}')">✕</span>
      </div>
      <div class="fp-track"><div class="fp-bar ${cls}" style="width:${pct}%"></div></div>
    </div>`;
  }

  let container = null;
  function ensureContainer() {
    if (container) return container;
    container = document.createElement("div");
    container.id = "fundus-progress";
    document.body.appendChild(container);
    return container;
  }

  function render() {
    const all = load();
    const ids = Object.keys(all);
    const c = ensureContainer();
    if (ids.length === 0) { c.innerHTML = ""; c.style.display = "none"; return; }
    c.style.display = "block";
    c.innerHTML = ids.map(id => barHtml(id, all[id])).join("");
  }

  // Pollt den Server-Status für Uploads in der Finalisierung.
  async function pollFinalizing() {
    const all = load();
    let changed = false;
    for (const id of Object.keys(all)) {
      const u = all[id];
      if (u.state !== "finalizing") continue;
      try {
        const r = await fetch("/api/v1/files/upload/status?id=" + encodeURIComponent(id));
        if (!r.ok) continue;
        const d = await r.json();
        if (d.state === "done") {
          delete all[id]; changed = true;
        } else if (d.state === "error") {
          u.state = "error"; u.error = d.error || "unbekannt"; changed = true;
        }
      } catch (e) { /* offline → später erneut */ }
    }
    if (changed) { save(all); render(); }
  }

  // Alte Einträge (über 24h) aufräumen, falls mal etwas hängen bleibt.
  function gc() {
    const all = load();
    let changed = false;
    const now = Date.now();
    for (const id of Object.keys(all)) {
      if (all[id].state !== "finalizing" && now - (all[id].ts || 0) > 24 * 3600 * 1000) {
        delete all[id]; changed = true;
      }
    }
    if (changed) save(all);
  }

  document.addEventListener("DOMContentLoaded", () => { gc(); render(); });
  // Falls DOMContentLoaded schon vorbei ist:
  if (document.readyState !== "loading") { gc(); render(); }
  setInterval(pollFinalizing, 2000);
})();
