// Update-Knopf in der Kopfzeile: laufende Revision grau; liegt auf GitHub eine
// neuere, erscheint deren Nummer in Fundus-Grün mit Leuchten. Klick → Einstellungen.
(function () {
  "use strict";
  var btn = document.getElementById("nav-update");
  if (!btn) return;
  // Revision des laufenden PROGRAMMS über /api/v1/status (gibt es in allen
  // Versionen – auch ein altes Programm ohne /update/info meldet sich dort).
  async function programRev() {
    try {
      var r = await fetch("/api/v1/status", { credentials: "same-origin", cache: "no-store" });
      if (!r.ok) return "";
      var st = await r.json();
      var m = String(st.version || "").match(/R?(\d+)/);
      return m ? "R" + m[1] : "";
    } catch (e) { return ""; }
  }
  async function check() {
    var prog = await programRev();
    var ui = (btn.getAttribute("data-ui") || "").trim();
    if (prog && ui && prog !== ui) {
      btn.textContent = "⚠ " + prog + " ≠ " + ui;
      btn.classList.remove("has-update");
      btn.classList.add("mismatch");
      btn.title = "Programm " + prog + " passt nicht zur Oberfläche " + ui +
        " – Update erneut installieren (Einstellungen → Software-Update)";
      return;
    }
    btn.classList.remove("mismatch");
    try {
      var r = await fetch("/api/v1/update/info", { credentials: "same-origin", cache: "no-store" });
      if (!r.ok) return;
      var d = await r.json();
      var cur = d.current || btn.textContent;
      if (d.available && d.available !== cur) {
        btn.textContent = d.available;
        btn.classList.add("has-update");
        btn.title = "Update verfügbar: " + d.available + " (installiert: " + cur + ") – zum Installieren klicken";
      } else {
        btn.textContent = cur;
        btn.classList.remove("has-update");
        btn.title = "Software-Update – installiert: " + cur;
      }
    } catch (e) {}
  }
  check();
  setInterval(check, 60000);
})();
