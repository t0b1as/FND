// Update-Knopf in der Kopfzeile: laufende Revision grau; liegt auf GitHub eine
// neuere, erscheint deren Nummer in Fundus-Grün mit Leuchten. Klick → Einstellungen.
(function () {
  "use strict";
  var btn = document.getElementById("nav-update");
  if (!btn) return;
  async function check() {
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
