// Anmeldepflicht in der Oberfläche (R659).
//
// Der Node weist ohne Anmeldung jede Anfrage ab. Damit daraus nicht eine Seite
// voller Fehlermeldungen wird, legt sich hier eine Erklärung darüber: was man
// tun kann und wie man hineinkommt.
//
// Die Sperre selbst sitzt im Node. Dieses Skript ist die Erklärung dazu, kein
// Schutz – wer es abschaltet, kommt trotzdem an keine Daten.
(function () {
  'use strict';

  const originalFetch = window.fetch;   // ungefiltert, für eigene Abfragen

  // Auf der Anmeldeseite selbst nichts tun.
  const offen = ['/login', '/setup', '/shared'];
  if (offen.some(function (p) { return location.pathname.indexOf(p) === 0; })) return;

  function overlayZeigen() {
    if (document.getElementById('auth-gate')) return;
    const d = document.createElement('div');
    d.id = 'auth-gate';
    d.innerHTML =
      '<div class="ag-box">' +
      '<div class="ag-titel">Bitte anmelden</div>' +
      '<p>Fundus ist dein eigener Knoten im Netz. Ohne Konto lässt sich nichts ' +
      'ansehen oder tun – es gibt keinen Gastzugang.</p>' +
      '<div class="ag-weg"><b>Mit E-Mail und Passwort</b><br>' +
      'Du wählst beides selbst. Daraus entsteht dein Konto – es wird nirgends ' +
      'hinterlegt und nicht geprüft. Dieselbe Eingabe führt immer zum selben Konto, ' +
      'auf jedem Gerät und auf jedem Knoten.</div>' +
      '<div class="ag-weg"><b>Mit deinen Schlüsselwörtern</b><br>' +
      'Die 30 Wörter, die dir beim ersten Anmelden gezeigt wurden. Sie führen zum ' +
      'selben Konto wie E-Mail und Passwort.</div>' +
      '<p class="ag-warn">Bewahre beides gut auf. Es gibt niemanden, der dein ' +
      'Passwort zurücksetzen könnte – dein Konto entsteht rechnerisch daraus.</p>' +
      '<button class="btn" id="ag-los">Anmelden</button>' +
      '</div>';
    document.body.appendChild(d);
    document.documentElement.style.overflow = 'hidden';
    const b = document.getElementById('ag-los');
    if (b) b.onclick = anmeldenOeffnen;
  }

  function overlayWeg() {
    const d = document.getElementById('auth-gate');
    if (d) d.remove();
    document.documentElement.style.overflow = '';
  }

  // R660: Solange das Anmeldefenster offen ist, darf sich die Sperre NICHT
  // wieder darüberlegen. Genau das passierte: Das Fenster fragt beim Öffnen den
  // Anmeldestand ab, der Node weist ab, und der Wächter unten hat daraufhin
  // sofort wieder gesperrt – man sah das Fenster nie.
  let _amAnmelden = false;

  function anmeldenOeffnen() {
    _amAnmelden = true;
    overlayWeg();
    // Das Anmeldefenster direkt öffnen (R660) – nicht erst über das Abzeichen,
    // das nur ein Menü aufklappt.
    if (typeof window.walletOpenDialog === 'function') {
      window.walletOpenDialog();
      warteAufAnmeldung();
      return;
    }
    const badge = document.getElementById('wallet-badge');
    if (badge) { badge.click(); warteAufAnmeldung(); return; }
    location.href = '/messenger';   // dort gibt es ein Anmeldefeld
  }

  // Regelmäßig nachsehen: Sobald die Anmeldung steht, ist alles gut. Bricht
  // jemand ab und das Fenster ist wieder zu, kommt die Erklärung zurück.
  function warteAufAnmeldung() {
    let versuche = 0;
    const takt = setInterval(async function () {
      versuche++;
      let drin = false;
      try {
        const r = await originalFetch('/api/v1/identity/me', { credentials: 'same-origin' });
        if (r.ok) { const d = await r.json(); drin = !!(d && d.fundus_id); }
      } catch (e) {}
      if (drin) { clearInterval(takt); _amAnmelden = false; overlayWeg(); return; }
      // Fenster zu und immer noch nicht angemeldet? Dann wieder sperren.
      const offen = document.getElementById('wallet-dialog') ||
                    document.getElementById('wallet-menu');
      if ((!offen && versuche > 2) || versuche > 150) {
        clearInterval(takt); _amAnmelden = false; overlayZeigen();
      }
    }, 2000);
  }

  async function pruefen() {
    if (_amAnmelden) return;
    try {
      const r = await fetch('/api/v1/identity/me', { credentials: 'same-origin' });
      if (!r.ok) { overlayZeigen(); return; }
      const d = await r.json();
      if (d && d.fundus_id) overlayWeg(); else overlayZeigen();
    } catch (e) {
      overlayZeigen();   // kein Node erreichbar: ebenfalls sperren
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', pruefen);
  else pruefen();

  // Weist der Node später ab (Sitzung abgelaufen), sofort wieder sperren.
  window.fetch = function () {
    return originalFetch.apply(this, arguments).then(function (r) {
      if (!_amAnmelden && r && r.status === 401 &&
          String(r.url || '').indexOf('/api/') >= 0) overlayZeigen();
      return r;
    });
  };
})();
