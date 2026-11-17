/* PayPal und Warenkorb im Marktplatz (R922).
   1 FND = 1 EUR. Das Geld geht direkt an den Verkäufer (PayPal-Adresse aus
   seinem Angebot). Den Betrag rechnet der Node selbst aus – der Browser
   schickt nur Angebot, Anzahl und Versandwunsch.
   Der Warenkorb liegt in diesem Browser (localStorage) und wird je Verkäufer
   bezahlt: Eine PayPal-Zahlung geht immer an genau einen Empfänger. */
(function () {
  "use strict";
  var WK_SCHLUESSEL = "fnd.warenkorb";
  var infoVersprechen = null, sdkVersprechen = null;

  function euro(v) { return (Math.round(v * 100) / 100).toLocaleString("de-DE", { minimumFractionDigits: 2, maximumFractionDigits: 2 }) + " €"; }
  function esc(t) { return String(t == null ? "" : t).replace(/[&<>"']/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]; }); }

  function info() {
    if (!infoVersprechen) infoVersprechen = fetch("/api/v1/marktplatz/paypal").then(function (r) { return r.json(); }).catch(function () { return { bereit: false }; });
    return infoVersprechen;
  }
  function sdk() {
    if (window.paypal) return Promise.resolve(window.paypal);
    if (!sdkVersprechen) sdkVersprechen = info().then(function (i) {
      if (!i.bereit) throw new Error("PayPal ist auf diesem Node nicht eingerichtet.");
      return new Promise(function (ok, nein) {
        var sk = document.createElement("script");
        sk.src = "https://www.paypal.com/sdk/js?client-id=" + encodeURIComponent(i.client_id) + "&currency=EUR&intent=capture&components=buttons&locale=de_DE";
        sk.onload = function () { window.paypal ? ok(window.paypal) : nein(new Error("PayPal nicht geladen")); };
        sk.onerror = function () { sdkVersprechen = null; nein(new Error("PayPal ist gerade nicht erreichbar.")); };
        document.head.appendChild(sk);
      });
    });
    return sdkVersprechen;
  }
  function post(url, body) {
    return fetch(url, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      .then(function (r) { return r.json().catch(function () { return {}; }).then(function (d) { if (!r.ok || d.error) throw new Error(d.error || ("Fehler " + r.status)); return d; }); });
  }

  // knopf(ziel, positionen(), {fertig(ergebnis), meldung(text)})
  function knopf(ziel, positionen, opt) {
    opt = opt || {};
    var melde = opt.meldung || function () {};
    var letzter = "";
    return sdk().then(function (pp) {
      return pp.Buttons({
        style: { layout: "horizontal", tagline: false, height: 44 },
        createOrder: function () {
          letzter = ""; melde("");
          return post("/api/v1/marktplatz/paypal/bestellung", { positionen: positionen() })
            .then(function (d) { return d.id; })
            .catch(function (e) { letzter = e.message; throw e; });
        },
        onApprove: function (data) {
          melde("Zahlung wird bestätigt …");
          return post("/api/v1/marktplatz/paypal/abschluss", { id: data.orderID })
            .then(function (x) { melde(""); if (opt.fertig) opt.fertig(x); })
            .catch(function (e) { melde("✗ " + e.message); });
        },
        onCancel: function () { melde("Abgebrochen – es wurde nichts abgebucht."); },
        onError: function (err) {
          melde("✗ " + (letzter || String((err && (err.message || err)) || "unbekannter Fehler").slice(0, 200)) + " Es wurde nichts abgebucht.");
          letzter = "";
        }
      }).render(ziel);
    });
  }

  // ── Warenkorb ────────────────────────────────────────────────────────────
  function wkLesen() { try { var l = JSON.parse(localStorage.getItem(WK_SCHLUESSEL) || "[]"); return Array.isArray(l) ? l : []; } catch (e) { return []; } }
  function wkSchreiben(l) { try { localStorage.setItem(WK_SCHLUESSEL, JSON.stringify(l)); } catch (e) {} wkAnzeigen(); }
  function wkHinzu(p) {
    var l = wkLesen(), da = l.filter(function (x) { return x.id === p.id; })[0];
    if (da) { da.anzahl = Math.min((da.anzahl || 1) + (p.anzahl || 1), p.max || 99); da.versand = p.versand; }
    else l.push(p);
    wkSchreiben(l);
    return l;
  }
  function wkAnzahl() { return wkLesen().reduce(function (s, x) { return s + (x.anzahl || 1); }, 0); }
  // Zähler in der Navigation (Element mit data-warenkorb-zahl)
  function wkAnzeigen() {
    var n = wkAnzahl();
    [].forEach.call(document.querySelectorAll("[data-warenkorb-zahl]"), function (e) { e.textContent = n ? String(n) : ""; e.style.display = n ? "" : "none"; });
  }
  document.addEventListener("DOMContentLoaded", wkAnzeigen);
  window.addEventListener("storage", function (e) { if (e.key === WK_SCHLUESSEL) wkAnzeigen(); });

  window.FundusMarktPayPal = { info: info, knopf: knopf, euro: euro, esc: esc,
    warenkorb: { lesen: wkLesen, schreiben: wkSchreiben, hinzu: wkHinzu, anzahl: wkAnzahl, anzeigen: wkAnzeigen } };
})();
