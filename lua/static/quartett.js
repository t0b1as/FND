// Flugquartett als PDF (R877) – im Browser mit jsPDF (static/jspdf.umd.min.js).
// Daten: /api/v1/aeronautik/quartett. Formate:
//   "karten": Einzelseiten im Endformat plus 3 mm Beschnitt, nach jeder
//             Vorderseite ihre Rückseite (R890); "druckerei": dasselbe, danach
//             vom Node in PDF/X-1a (CMYK) umgewandelt. Kartengröße 63 × 88 oder
//             59 × 91 mm; zuerst eine Titelkarte; FLIGHT QUAD 32 oder 64.
//   "a4":     Karten in Originalgröße auf A4 (R880): je 4 liegende Streifen
//             176 × 63 mm untereinander – links die Rückseite, rechts die
//             Vorderseite, deren Oberkante an der Falzlinie liegt. Ausschneiden,
//             an der Oberkante nach hinten falten, kleben. Seitlich umgedreht
//             steht die Rückseite aufrecht.
(function () {
  "use strict";
  var B = 63, H = 88;

  var KAT_FARBE = {
    hoehe_max: [192, 57, 43], hoehe_min: [230, 126, 34], geschw_max: [41, 98, 192], geschw_min: [52, 152, 219],
    steig_max: [39, 154, 96], sink_max: [22, 160, 133], km_max: [125, 60, 152], km_min: [155, 89, 182]
  };
  // ── Sprache (R894): Deutsch oder Englisch für alles auf den Karten ──────
  var SPR = "de";
  var EN = {
    "Höhe": "Altitude", "Geschwindigkeit": "Speed", "Sinken/Steigen": "Descent/Climb", "Entfernung": "Distance",
    "MINIMUM": "MINIMUM", "MAXIMUM": "MAXIMUM", "Rekord · ": "Record · ", "am Boden": "on ground",
    "Beim Rekord: ": "At record: ", "Kurs ": "Track ", "Halter ": "Owner ", "Airline ": "Airline ", "Erfasst ": "Seen ",
    " Meldungen": " messages", "Quellen: ": "Sources: ", "Empfang": "Reception", "Flugzeug": "Aircraft", "Route": "Route",
    "Route unbekannt": "Route unknown", "ohne Kennzeichen": "no registration", "Unbekannter Typ": "Unknown type", "Typ ": "Type ",
    "Standort unbekannt": "Location unknown", "Die Quartette": "The quartets", "Spielregeln": "How to play",
    " Flugzeuge aufgezeichnet · ADS-B-Empfang ": " aircraft recorded · ADS-B receiver ",
    "Typen adsbdb/hexdb · Routen adsb.lol": "Types adsbdb/hexdb · Routes adsb.lol",
    "bearbeitet": "modified", "bearbeitet, ": "modified, ", "zugeschnitten": "cropped",
    "SBS-Datenstrom": "SBS stream",
    "regeln": "Shuffle and deal all cards; each player keeps a face-down pile. On your turn, name a value of your top card " +
      "and its column, e.g. \u201cAltitude, maximum\u201d. For maximum the highest value wins, for minimum the lowest, for " +
      "descent/climb the stronger one. The winner takes all revealed cards and plays next. Whoever collects all cards wins. " +
      "Quartet variant: collect four cards of one number (A\u2013D).",
    "a4kopf": "Cards at actual size {B} × {H} mm. Cut at the corners, fold back along the dashed top edge, glue together.",
    "seite": "Page {n} of {m}",
    // Kategorien
    "hoehe_max": "Highest altitude", "hoehe_min": "Lowest altitude", "geschw_max": "Fastest", "geschw_min": "Slowest",
    "steig_max": "Strongest climb", "sink_max": "Strongest descent", "km_max": "Farthest", "km_min": "Closest",
    // Bauarten
    "grossraum": "Wide-body jet", "vierstrahler": "Four-engine jet", "mittelstrecke": "Narrow-body jet", "heckjet": "Rear-engine jet",
    "turboprop": "Turboprop", "geschaeftsjet": "Business jet", "kleinflugzeug": "Light aircraft", "hubschrauber": "Helicopter",
    "militaer": "Military jet", "unbekannt": "Aircraft"
  };
  var DE = {
    "regeln": "Karten mischen und gleichmäßig verteilen; jeder hält seinen Stapel verdeckt. Wer am Zug ist, nennt einen Wert " +
      "seiner obersten Karte und die Spalte, etwa „Höhe, Maximum“. Bei Maximum gewinnt der größte Wert, bei Minimum der kleinste, " +
      "bei Sinken/Steigen jeweils der stärkere. Der Gewinner bekommt alle aufgedeckten Karten und ist wieder am Zug. " +
      "Wer alle Karten hat, gewinnt. Quartett-Variante: vier Karten einer Nummer (A–D) sammeln.",
    "a4kopf": "Karten in Originalgröße {B} × {H} mm. An den Ecken ausschneiden, an der gestrichelten Oberkante nach hinten falten, zusammenkleben.",
    "seite": "Seite {n} von {m}",
    "hoehe_max": "Höchste Höhe", "hoehe_min": "Niedrigste Höhe", "geschw_max": "Schnellstes", "geschw_min": "Langsamstes",
    "steig_max": "Stärkstes Steigen", "sink_max": "Stärkstes Sinken", "km_max": "Weiteste Entfernung", "km_min": "Nächste Entfernung"
  };
  // T: Text in der gewählten Sprache; Schlüssel ist der deutsche Text.
  function T(s, werte) {
    var r = SPR === "en" ? (EN[s] != null ? EN[s] : s) : (DE[s] != null ? DE[s] : s);
    if (werte) Object.keys(werte).forEach(function (k) { r = r.split("{" + k + "}").join(werte[k]); });
    return r;
  }
  function katTitel(schl, ersatz) { return T(schl) !== schl ? T(schl) : (ersatz || schl); }
  // Zeitraumtitel aus dem Code: 20261114 · 2026W46 · 202611 · 2026
  var MONATE = { de: ["Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"],
    en: ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"] };
  function zeitraumTitel(code, ersatz) {
    code = String(code || ""); var en = SPR === "en", m;
    if (/^\d{8}$/.test(code)) return en ? (+code.slice(6)) + " " + MONATE.en[+code.slice(4, 6) - 1].slice(0, 3) + " " + code.slice(0, 4) : "Tag " + code.slice(6) + "." + code.slice(4, 6) + "." + code.slice(0, 4);
    if ((m = code.match(/^(\d{4})W(\d{2})$/))) return (en ? "Week " : "KW ") + (+m[2]) + "/" + m[1];
    if (/^\d{6}$/.test(code)) return MONATE[en ? "en" : "de"][+code.slice(4) - 1] + " " + code.slice(0, 4);
    if (/^\d{4}$/.test(code)) return (en ? "Year " : "Jahr ") + code;
    return ersatz || code;
  }
  // Texte vom Node (Urheberzeile, Quellen) auf Englisch
  function nodeText(s) {
    if (SPR !== "en" || !s) return s;
    return String(s).replace(/^Foto: /, "Photo: ").replace(/^KI-erzeugt, kein Foto/, "AI-generated, not a photo")
      .replace(/ · bearbeitet, /, " · modified, ").replace(/ · bearbeitet$/, " · modified").replace(/ · zugeschnitten$/, " · cropped")
      .replace("SBS-Datenstrom", "SBS stream").replace("unbekannt", "unknown");
  }

  var KLASSE_NAME = {
    grossraum: "Großraumjet", vierstrahler: "Vierstrahliger Jet", mittelstrecke: "Mittelstreckenjet", heckjet: "Jet mit Hecktriebwerken",
    turboprop: "Turboprop", geschaeftsjet: "Geschäftsreisejet", kleinflugzeug: "Kleinflugzeug", hubschrauber: "Hubschrauber",
    militaer: "Militärjet", unbekannt: "Flugzeug"
  };

  // ── Silhouetten (Seitenansicht, Nase links, Feld 240 × 90) ───────────────
  function rumpf(x0, x1, y, h, heckHoch) {
    var r = h / 2, n = x0 + h * 1.4;
    return "M" + x0 + "," + (y + r * 0.35) + " C" + x0 + "," + (y - r * 0.8) + " " + (n - h * 0.6) + "," + (y - r) + " " + n + "," + (y - r) +
      " L" + (x1 - h * 2.2) + "," + (y - r) + " L" + x1 + "," + (y - r - (heckHoch || 0)) + " L" + x1 + "," + (y - r * 0.2 - (heckHoch || 0)) +
      " L" + (x1 - h * 2.6) + "," + (y + r) + " L" + (x0 + h * 0.9) + "," + (y + r) + " C" + (x0 + 2) + "," + (y + r) + " " + x0 + "," + (y + r * 0.8) + " " + x0 + "," + (y + r * 0.35) + " Z";
  }
  function flosse(x, yo, hoch, breit) { return "M" + (x - breit) + "," + yo + " L" + (x - breit * 0.35) + "," + (yo - hoch) + " L" + x + "," + (yo - hoch) + " L" + (x + 1) + "," + yo + " Z"; }
  function kapsel(x, y, l, d) { return "M" + (x + d / 2) + "," + (y - d / 2) + " h" + (l - d) + " a" + d / 2 + "," + d / 2 + " 0 0 1 0," + d + " h" + (d - l) + " a" + d / 2 + "," + d / 2 + " 0 0 1 0," + (-d) + " Z"; }
  function ellipse(cx, cy, rx, ry) { return "M" + (cx - rx) + "," + cy + " a" + rx + "," + ry + " 0 1 0 " + 2 * rx + ",0 a" + rx + "," + ry + " 0 1 0 " + (-2 * rx) + ",0 Z"; }

  var SILHOUETTE = {
    grossraum: function () { return [rumpf(8, 228, 50, 20, 2), flosse(226, 41, 34, 26), ellipse(212, 42, 16, 2.5), ellipse(112, 58, 34, 3.5), kapsel(88, 64, 26, 11)]; },
    vierstrahler: function () { return [rumpf(6, 230, 50, 20, 2), "M22,42 C30,28 60,26 92,32 L92,42 Z", flosse(228, 41, 36, 28), ellipse(214, 42, 17, 2.5), ellipse(112, 58, 38, 3.5), kapsel(84, 64, 22, 10), kapsel(120, 63, 22, 9)]; },
    mittelstrecke: function () { return [rumpf(22, 220, 50, 15, 1), flosse(218, 43, 27, 22), ellipse(206, 44, 13, 2), ellipse(112, 55, 28, 3), kapsel(96, 60, 20, 9)]; },
    heckjet: function () { return [rumpf(24, 214, 50, 13, 0), flosse(212, 44, 30, 22), ellipse(204, 14, 16, 2), kapsel(166, 42, 26, 9), ellipse(110, 55, 26, 2.5)]; },
    turboprop: function () { return [rumpf(28, 214, 52, 15, 2), flosse(212, 45, 27, 22), ellipse(200, 45, 13, 2), "M86,42 h56 l-2,4 h-54 Z", kapsel(84, 50, 26, 8), ellipse(83, 50, 1.5, 11)]; },
    geschaeftsjet: function () { return [rumpf(46, 198, 52, 11, 0), flosse(196, 47, 26, 18), ellipse(189, 21, 14, 1.8), kapsel(152, 45, 24, 8), ellipse(118, 56, 20, 2)]; },
    kleinflugzeug: function () { return [rumpf(58, 190, 52, 13, 4), flosse(188, 44, 22, 16), ellipse(178, 46, 10, 1.6), "M96,41 h46 l-1,3 h-45 Z", "M104,44 L114,58 L116,58 L107,44 Z", ellipse(57, 54, 1.4, 12),
      ellipse(112, 66, 3.5, 3.5), ellipse(170, 64, 2.5, 2.5)]; },
    hubschrauber: function () { return ["M70,58 C70,38 92,32 118,34 C132,35 140,42 142,50 L206,48 L208,53 L142,58 C138,66 126,68 108,68 C86,68 70,66 70,58 Z",
      "M30,26 h176 v3 h-176 Z", "M112,29 h8 v7 h-8 Z", ellipse(208, 44, 7, 7), "M82,74 h64 v2.5 h-64 Z", "M92,66 h3 v9 h-3 Z", "M128,66 h3 v9 h-3 Z"]; },
    militaer: function () { return [rumpf(18, 214, 52, 11, 0), "M60,46 C68,36 84,36 92,46 Z", "M118,56 L196,56 L200,60 L150,62 Z", flosse(210, 47, 28, 26), "M8,52 L22,50 L22,54 Z"]; }
  };
  SILHOUETTE.unbekannt = SILHOUETTE.mittelstrecke;

  var bildCache = {};
  function silhouetteBild(klasse, farbe, gedreht) {
    var schl = klasse + "|" + farbe + "|" + (gedreht ? 1 : 0);
    if (bildCache[schl]) return Promise.resolve(bildCache[schl]);
    var pfade = (SILHOUETTE[klasse] || SILHOUETTE.unbekannt)();
    var svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 90" width="960" height="360">' +
      pfade.map(function (d) { return '<path d="' + d + '" fill="' + farbe + '"/>'; }).join("") + "</svg>";
    return new Promise(function (ok, fehler) {
      var img = new Image();
      img.onload = function () {
        var cv = document.createElement("canvas"); cv.width = 960; cv.height = 360;
        var g = cv.getContext("2d");
        if (gedreht) { g.translate(960, 360); g.rotate(Math.PI); }
        g.drawImage(img, 0, 0, 960, 360);
        bildCache[schl] = cv.toDataURL("image/png"); ok(bildCache[schl]);
      };
      img.onerror = fehler;
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
    });
  }

  // ── Formatierung ─────────────────────────────────────────────────────────
  function tausend(n) { return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, SPR === "en" ? "," : "."); }
  function komma(n, st) { return SPR === "en" ? n.toFixed(st) : n.toFixed(st).replace(".", ","); }
  function wertText(art, w) {
    if (!w) return "–";
    switch (art) {
      case "hoehe": return tausend(w.v) + " m";
      case "geschw": return tausend(w.v) + " km/h";
      // Gedankenstrich statt Minuszeichen: U+2212 fehlt in der PDF-Standardschrift.
      case "steig": return (w.v > 0 ? "+" : w.v < 0 ? "–" : "") + komma(Math.abs(w.v), 1) + " m/s";
      case "km": return komma(w.v, 3) + " km"; // R913: auf den Meter – für die nächste Entfernung
    }
    return String(w.v);
  }
  function rekordText(k) {
    var v = (k.moment || {}).v, e = k.einheit || "";
    if (v == null) return katTitel(k.kategorie, k.kategorie_titel);
    var s = e === "m" || e === "km/h" ? tausend(v) : e === "m/s" ? (v > 0 ? "+" : "–") + komma(Math.abs(v), 1) : komma(v, e === "km" ? 3 : 1);
    return katTitel(k.kategorie, k.kategorie_titel) + " " + s + " " + e;
  }
  function kuerzen(doc, t, breite) {
    t = String(t || "");
    if (doc.getTextWidth(t) <= breite) return t;
    while (t.length > 1 && doc.getTextWidth(t + "…") > breite) t = t.slice(0, -1);
    return t + "…";
  }

  // Urheberzeile „Foto: Name · Lizenz · Wikimedia Commons“: Zu lang? Dann
  // NUR den Namen kürzen – Lizenz und Quelle müssen vollständig bleiben.
  function urheberZeile(doc, t, breite) {
    if (doc.getTextWidth(t) <= breite) return t;
    var teile = String(t).split(" · ");
    if (teile.length < 3) return kuerzen(doc, t, breite);
    var name = teile[0], rest = " · " + teile.slice(1).join(" · ");
    while (name.length > 8 && doc.getTextWidth(name + "…" + rest) > breite) name = name.slice(0, -1);
    return name + "…" + rest;
  }

  // Nur Zeichen, die die PDF-Standardschrift kennt (WinAnsi). Andere – „ă“ in
  // Coandă, „İ“ in İstanbul, „ș“ – schalten jsPDF auf eine andere Kodierung um:
  // Die Buchstaben stehen dann gesperrt, und die Breite stimmt nicht mehr
  // (kein Kürzen, Text läuft über den Rand). Deshalb auf die Grundform zurück.
  var WINANSI_EXTRA = "€‚ƒ„…†‡ˆ‰Š‹ŒŽ‘’“”•–—˜™š›œžŸ";
  function sicher(s) {
    s = String(s == null ? "" : s);
    var raus = "";
    for (var i = 0; i < s.length; i++) {
      var ch = s[i], c = ch.charCodeAt(0);
      if ((c >= 32 && c <= 126) || (c >= 160 && c <= 255) || WINANSI_EXTRA.indexOf(ch) >= 0) { raus += ch; continue; }
      var grund = ch.normalize("NFD").replace(/[\u0300-\u036f]/g, "");
      grund = { "ı": "i", "Ł": "L", "ł": "l", "Đ": "D", "đ": "d", "Ħ": "H", "ħ": "h", "ŀ": "l", "Ŀ": "L", "ĸ": "k", "ſ": "s" }[grund] || grund;
      raus += /^[\x20-\x7e]+$/.test(grund) ? grund : (c === 10 ? "" : "?");
    }
    return raus;
  }
  // Ein Dokument so einrichten, dass JEDE Textausgabe und Breitenmessung durch
  // sicher() läuft – an einer Stelle, damit kein Feld vergessen wird.
  var RAND = 3; // mm Beschnitt für die Druckerei (R889)
  // R901: 59 × 91 wird mit 5 mm Eckenradius gestanzt – die unterste Zeile
  // rückt etwas nach innen, damit sie auch an der Rundung 3 mm Abstand hält.
  function eckAbstand() { return B === 59 && H === 91 ? 4.6 : 4; }
  // ── Druckfarben (R893) ───────────────────────────────────────────────
  // Tabellen aus quartett-farben.js (vom Ghostscript-CMYK-Profil berechnet).
  var DF = null;
  function druckTabellen() {
    if (DF || !window.FundusDruckFarben) return DF;
    var q = window.FundusDruckFarben, b = function (s) { var r = atob(s), u = new Uint8Array(r.length); for (var i = 0; i < r.length; i++) u[i] = r.charCodeAt(i); return u; };
    DF = { n: q.n, cmyk: b(q.cmyk), foto: b(q.foto) };
    return DF;
  }
  // Trilinear zwischen den Gitterpunkten; kanaele 4 (CMYK) oder 3 (Vorab-RGB).
  function nachschlagen(tab, kanaele, r, g, b, aus) {
    var n = DF.n, s = (n - 1) / 255;
    var fr = r * s, fg = g * s, fb = b * s;
    var r0 = Math.min(n - 2, fr | 0), g0 = Math.min(n - 2, fg | 0), b0 = Math.min(n - 2, fb | 0);
    var tr = fr - r0, tg = fg - g0, tb = fb - b0;
    for (var k = 0; k < kanaele; k++) aus[k] = 0;
    for (var dr = 0; dr < 2; dr++) for (var dg = 0; dg < 2; dg++) for (var db = 0; db < 2; db++) {
      var w = (dr ? tr : 1 - tr) * (dg ? tg : 1 - tg) * (db ? tb : 1 - tb);
      if (!w) continue;
      var o = (((r0 + dr) * n + (g0 + dg)) * n + (b0 + db)) * kanaele;
      for (var k2 = 0; k2 < kanaele; k2++) aus[k2] += w * tab[o + k2];
    }
    return aus;
  }
  // RGB → CMYK (0…1) für Vektoren: Grau → reines K, Weiß → keine Farbe.
  function druckCMYK(r, g, b) {
    if (Math.max(r, g, b) - Math.min(r, g, b) <= 24) {
      var grau = (r + g + b) / 3;
      return grau >= 250 ? [0, 0, 0, 0] : [0, 0, 0, Math.round((1 - grau / 255) * 1000) / 1000];
    }
    var c = nachschlagen(DF.cmyk, 4, r, g, b, [0, 0, 0, 0]);
    return c.map(function (v) { return Math.round(v / 255 * 1000) / 1000; });
  }
  // R901: Tiefschwarz nach Vorgabe von WIRmachenDRUCK (59 × 91): dunkle
  // Flächen bekommen mindestens 40 % Cyan unter das Schwarz, reines Schwarz
  // wird C40 M0 Y0 K100. Nur Flächen – Schrift und Linien bleiben reines K,
  // sonst blitzt bei Passerversatz Cyan an den Kanten.
  function tiefschwarz(c) {
    if (!(B === 59 && H === 91) || c[3] < 0.9) return c;
    if (!c[0] && !c[1] && !c[2]) return [0.4, 0, 0, 1];
    return [Math.max(c[0], 0.4), c[1], c[2], c[3]];
  }
  // Bild für den Druck: jeden Bildpunkt durch die Fototabelle (R893).
  function bildDruckfarben(g, w, h) {
    if (!druckTabellen()) return;
    var d = g.getImageData(0, 0, w, h), p = d.data, z = [0, 0, 0];
    for (var i = 0; i < p.length; i += 4) {
      nachschlagen(DF.foto, 3, p[i], p[i + 1], p[i + 2], z);
      p[i] = z[0] + 0.5 | 0; p[i + 1] = z[1] + 0.5 | 0; p[i + 2] = z[2] + 0.5 | 0;
    }
    g.putImageData(d, 0, 0);
  }

  // R914: Inter (dieselbe Schrift wie FUNDUS auf der Seite) im PDF einbetten.
  var INTER = false;
  function schriftAnmelden(doc) {
    var f = window.FUNDUS_SCHRIFT;
    INTER = false;
    if (!f) return;
    try {
      ["black", "bold", "normal"].forEach(function (st) {
        doc.addFileToVFS("Inter-" + st + ".ttf", f[st]);
        doc.addFont("Inter-" + st + ".ttf", "Inter", st);
      });
      INTER = true;
    } catch (e) { INTER = false; }
  }
  function sicherMachen(doc, opt) {
    schriftAnmelden(doc);
    var text = doc.text, breite = doc.getTextWidth;
    if (opt && opt.druck && druckTabellen()) {
      // Alle Vektorfarben direkt als CMYK – Ghostscript lässt CMYK unverändert.
      ["setFillColor", "setDrawColor", "setTextColor"].forEach(function (name) {
        var orig = doc[name];
        doc[name] = function (r, g, b) {
          if (arguments.length === 1 && typeof r === "number") { g = r; b = r; }
          else if (arguments.length < 3 || typeof r !== "number") return orig.apply(this, arguments);
          var c = druckCMYK(r, g, b);
          if (name === "setFillColor") c = tiefschwarz(c);
          return orig.call(this, c[0], c[1], c[2], c[3]);
        };
      });
    } else if (opt && opt.neutral) {
      // Graue Schrift auf reines Grau ziehen: nach der Umwandlung mit vollem
      // Unterfarbenersatz wird sie reines Schwarz (K) – ohne Spuren von C, M, Y.
      var farbe = doc.setTextColor;
      doc.setTextColor = function (r, g, b) {
        if (arguments.length >= 3 && typeof r === "number" && Math.max(r, g, b) - Math.min(r, g, b) <= 24 && Math.min(r, g, b) < 250) {
          var grau = Math.round((r + g + b) / 3);
          return farbe.call(this, grau, grau, grau);
        }
        return farbe.apply(this, arguments);
      };
    }
    doc.text = function (t, x, y, o, w) { return text.call(this, Array.isArray(t) ? t.map(sicher) : sicher(t), x, y, o, w); };
    doc.getTextWidth = function (t) { return breite.call(this, sicher(t)); };
    return doc;
  }
  // Heller Schein hinter Text auf einem Bild (R886): weiße Konturen in
  // abnehmender Stärke und Deckkraft, darüber die gefüllte Schrift – Vektor,
  // bleibt im Druck scharf.
  function schein(doc, s, x, y, pt, farbe) {
    var mm = pt * 0.3528;
    if (doc._deckend) {
      // R891 – Druckformate: KEINE Transparenz (PDF/X-1a verbietet sie;
      // Ghostscript müsste jede Stelle hochaufgelöst rastern – auf dem Pi
      // dauert das endlos). Zwei deckende Umrisse statt des weichen Scheins.
      doc.setLineJoin("round");
      doc.setDrawColor(228, 228, 228); doc.setLineWidth(mm * 0.6); doc.text(s, x, y, { renderingMode: "stroke" });
      doc.setDrawColor(255, 255, 255); doc.setLineWidth(mm * 0.34); doc.text(s, x, y, { renderingMode: "stroke" });
      doc.setTextColor(farbe[0], farbe[1], farbe[2]);
      doc.text(s, x, y);
      return;
    }
    doc.setDrawColor(255, 255, 255); doc.setLineJoin("round");
    [[0.62, 0.28], [0.42, 0.5], [0.24, 1]].forEach(function (st) {   // R887: kräftiger
      doc.setGState(new doc.GState({ opacity: st[1], "stroke-opacity": st[1] }));
      doc.setLineWidth(mm * st[0]);
      doc.text(s, x, y, { renderingMode: "stroke" });
    });
    doc.setGState(new doc.GState({ opacity: 1, "stroke-opacity": 1 }));
    doc.setTextColor(farbe[0], farbe[1], farbe[2]);
    doc.text(s, x, y);
  }
  // Bearbeitungshinweis (R887): Das Bild füllt immer die Kartenbreite und
  // trägt Text – beides ist eine Bearbeitung. Gemeinfrei, CC0 und KI: kein
  // Hinweis nötig. CC BY: „bearbeitet“. CC BY-SA: „bearbeitet, CC BY-SA“ – die
  // bearbeitete Fassung steht wieder unter derselben Lizenz.
  function bearbeitung(k) {
    var c = (k.foto && k.foto.credit) || "";
    if ((k.foto && k.foto.ki) || /^KI-erzeugt/.test(c)) return "";
    if (/·\s*(CC0[^·]*|Public domain|PD[^·]*)\s*·/i.test(c)) return "";
    var sa = c.match(/·\s*(CC BY-SA[^·]*?)\s*·/i);
    if (sa) return T("bearbeitet, ") + sa[1].trim();
    if (/·\s*CC BY[^·]*·/i.test(c)) return T("bearbeitet");
    return T("bearbeitet");
  }
  // Grundlinie für Text, der in einem Feld (oben, Höhe) senkrecht mittig steht:
  // halbe Feldhöhe plus halbe Großbuchstabenhöhe (≈ 0,7 der Schriftgröße).
  function senkrechtMitte(oben, hoehe, pt) { return oben + hoehe / 2 + pt * 0.3528 * 0.7 / 2; }

  // Rechtsbündig/zentriert selbst rechnen: jsPDF rechnet die Ausrichtung in
  // einer gedrehten Zeichnung (A4-Faltvorlage) falsch herum.
  function rechts(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s), y); }
  function mitte(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s) / 2, y); }

  // ── Vorderseite (Vektor) ─────────────────────────────────────────────────
  function koordinate(v, pos, neg) {
    if (SPR === "en") { pos = pos === "O" ? "E" : pos; neg = neg === "O" ? "E" : neg; }
    return v == null ? "" : komma(Math.abs(v), 4) + "° " + (v >= 0 ? pos : neg);
  }
  // Flughafen kurz (Code) und lesbar (Name, Stadt, Land) – R882.
  function flughafenLang(f) {
    if (!f) return "";
    var name = f.name || f.stadt || f.code || "";
    var zusatz = [f.stadt && f.name && f.name.indexOf(f.stadt) < 0 ? f.stadt : "", f.land || ""].filter(Boolean).join(", ");
    return name + (zusatz ? " (" + zusatz + ")" : "");
  }
  var QUELLE_NAME = { empfang: "Empfang", flugzeug: "Flugzeug", route: "Route" }; // über T() übersetzt

  // Ein Feld der Wertetabelle: Wert rechtsbündig, Zeitstempel klein darunter.
  function zelle(doc, art, w, rechtsX, zy, hervor, f) {
    var text = w ? wertText(art, w) : "–";
    doc.setFont("helvetica", "bold"); doc.setFontSize(6.3);
    if (hervor) doc.setTextColor(f[0], f[1], f[2]); else doc.setTextColor(35, 38, 48);
    rechts(doc, text, rechtsX, zy + 2.7);
    doc.setFont("helvetica", "normal"); doc.setFontSize(3.5); doc.setTextColor(130, 135, 148);
    rechts(doc, w && w.t ? w.t : "", rechtsX, zy + 4.8);
  }

  // rand (mm, R889): Beschnitt – Flächen über die Schnittkante, keine runden
  // Ecken und kein gezeichneter Kartenrand (die Druckerei schneidet).
  function vorderseite(doc, x, y, k, kontext, bild, rand) {
    rand = rand || 0;
    var f = KAT_FARBE[k.kategorie] || [80, 80, 80], m = k.moment || {}, W = k.werte || {};
    if (rand) {
      doc.setFillColor(255, 255, 255); doc.rect(x - rand, y - rand, B + 2 * rand, H + 2 * rand, "F");
      doc.setFillColor(f[0], f[1], f[2]); doc.rect(x - rand, y - rand, B + 2 * rand, 11.6 + rand, "F");
    } else {
      doc.setDrawColor(200); doc.setLineWidth(0.2); doc.setFillColor(255, 255, 255);
      doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, H - 1.2, 3, 3, "FD");
      // Kopf: Nummer, Kategorie, Bauart
      doc.setFillColor(f[0], f[1], f[2]);
      doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, 11, 3, 3, "F");
      doc.rect(x + 0.6, y + 6, B - 1.2, 5.6, "F");
    }
    doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(15);
    doc.text(k.nr, x + 4, y + 9);
    doc.setFontSize(6.2); rechts(doc, katTitel(k.kategorie, k.kategorie_titel).toUpperCase(), x + B - 4, y + 5.2);
    doc.setFont("helvetica", "normal"); doc.setFontSize(5.4);
    rechts(doc, SPR === "en" ? T(k.klasse || "unbekannt") : (KLASSE_NAME[k.klasse] || "Flugzeug"), x + B - 4, y + 9.2);
    // ── Bildfeld (R886): das Bild als Hintergrund der oberen Hälfte, der Text
    // darauf mit hellem Schein. Zuschneiden ist eine Bearbeitung: Gemeinfreie,
    // CC0- und KI-Bilder füllen das Feld; CC BY ebenfalls, mit „zugeschnitten“
    // in der Urheberzeile; CC BY-SA bleibt ungeschnitten (eine Bearbeitung
    // müsste sonst selbst unter CC BY-SA stehen – die ganze Karte).
    // Bildfeld 33 mm bei 88 mm Kartenhöhe; jede zusätzliche Höhe geht ins Bild (R890).
    var fx = rand ? x - rand : x + 0.6, fy = y + 11.6, fw = rand ? B + 2 * rand : B - 1.2, fh = 33 + (H - 88);
    doc.setFillColor(232, 241, 250); doc.rect(fx, fy, fw, fh, "F");
    var credit = k.foto ? nodeText(k.foto.credit) : "";
    if (k._foto) {
      // R903: Das ganze Foto, auf das Feld skaliert (nicht beschnitten), in
      // voller Auflösung – jsPDF bettet die Bildpunkte unverändert ein.
      // Mit Beschnitt nur bis zum Sicherheitsabstand (3 mm innerhalb der
      // Schnittkante, R902); der helle Feldgrund läuft in den Beschnitt.
      var px = rand ? x + 3 : fx, pw = rand ? B - 6 : fw;
      var sc = Math.min(pw / k._foto.w, fh / k._foto.h), bw = k._foto.w * sc, bhh = k._foto.h * sc;
      doc.addImage(k._foto.daten, k._foto.format, px + (pw - bw) / 2, fy + (fh - bhh) / 2, bw, bhh, k._foto.alias, "FAST");
    } else {
      // Silhouette groß, mittig zwischen Typ oben und Route unten.
      var sw = Math.min(54, B - 9), sh = sw * 17.5 / 54;
      doc.addImage(bild, /^data:image\/jpeg/.test(bild) ? "JPEG" : "PNG", x + (B - sw) / 2, fy + 9.5 + (fh - 33) / 2, sw, sh, "silh-" + (k.klasse || "unbekannt"), "FAST");
    }
    // Typ, Kennzeichen, Rufzeichen, Betreiber – oben, mit Schein
    var typ = k.typ || (k.icao ? T("Typ ") + k.icao : T("Unbekannter Typ"));
    if (k.hersteller && typ.toUpperCase().indexOf(k.hersteller.toUpperCase().split(" ")[0]) < 0 && k.hersteller.length < 18) typ = k.hersteller.split(" ")[0] + " " + typ;
    doc.setFont("helvetica", "bold"); doc.setFontSize(8.6);
    schein(doc, kuerzen(doc, typ, B - 8), x + 4, fy + 4.8, 8.6, [20, 23, 30]);
    doc.setFont("helvetica", "normal"); doc.setFontSize(5.6);
    schein(doc, kuerzen(doc, [k.kennzeichen, k.flug, k.betreiber || k.airline].filter(Boolean).join(" · ") || T("ohne Kennzeichen"), B - 8), x + 4, fy + 8.0, 5.6, [45, 50, 62]);
    // Flugverbindung am unteren Bildrand (R887), darunter die Urheberzeile
    if (k.von && k.nach) {
      doc.setFont("helvetica", "bold"); doc.setFontSize(6.4);
      schein(doc, k.von.code + "  ›  " + k.nach.code, x + 4, fy + fh - 6.2, 6.4, [30, 36, 48]);
      doc.setFont("helvetica", "normal"); doc.setFontSize(4.1);
      schein(doc, kuerzen(doc, flughafenLang(k.von) + "  ›  " + flughafenLang(k.nach), B - 8), x + 4, fy + fh - 3.7, 4.1, [55, 60, 72]);
    } else {
      doc.setFont("helvetica", "normal"); doc.setFontSize(4.8);
      schein(doc, k.route || T("Route unbekannt"), x + 4, fy + fh - 3.9, 4.8, [90, 95, 108]);
    }
    if (credit) {
      doc.setFont("helvetica", "normal"); doc.setFontSize(3.3);
      var cz = urheberZeile(doc, credit, B - 8);
      schein(doc, cz, x + B - 4 - doc.getTextWidth(cz), fy + fh - 1.0, 3.3, [60, 65, 78]);
    }
    // Rekord
    var ry = fy + fh + 0.9;
    doc.setFillColor(f[0], f[1], f[2]); doc.roundedRect(x + 3, ry, B - 6, 4.4, 1.2, 1.2, "F");
    doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(5.4);
    mitte(doc, kuerzen(doc, T("Rekord · ") + rekordText(k), B - 8), x + B / 2, senkrechtMitte(ry, 4.4, 5.4));
    // Wertetabelle: Minimum und Maximum mit Zeitstempel (Steigen/Sinken: beide Richtungen)
    var ty = ry + 5.0, spMin = x + B - 23, spMax = x + B - 4.5, zh = 5.6;
    doc.setFont("helvetica", "bold"); doc.setFontSize(3.8); doc.setTextColor(130, 135, 148);
    rechts(doc, "MINIMUM", spMin, ty + 1.6); rechts(doc, "MAXIMUM", spMax, ty + 1.6);
    var zeilen = [["hoehe", "Höhe", "hoehe_min", "hoehe_max"], ["geschw", "Geschwindigkeit", "geschw_min", "geschw_max"],
      ["steig", "Steigen/Sinken", "sink_max", "steig_max"], ["km", "Entfernung", "km_min", "km_max"]];
    zeilen.forEach(function (z, i) {
      var zy = ty + 2.2 + i * zh, w = W[z[0]] || {};
      if (i % 2 === 0) { doc.setFillColor(243, 245, 248); doc.rect(x + 3, zy, B - 6, zh, "F"); }
      doc.setFont("helvetica", "bold"); doc.setFontSize(5.8);
      var aktiv = k.kategorie === z[2] || k.kategorie === z[3];
      if (aktiv) doc.setTextColor(f[0], f[1], f[2]); else doc.setTextColor(45, 48, 58);
      doc.text(T(z[0] === "steig" ? "Sinken/Steigen" : z[1]), x + 4.5, zy + 3.6);
      zelle(doc, z[0], w.min, spMin, zy, k.kategorie === z[2], f);
      zelle(doc, z[0], w.max, spMax, zy, k.kategorie === z[3], f);
    });
    // Metadaten und Quellen
    var my = ty + 2.2 + 4 * zh + 2.0;
    doc.setFont("helvetica", "normal"); doc.setFontSize(3.9); doc.setTextColor(70, 76, 90);
    var zeilenMeta = [
      T("Beim Rekord: ") + ([m.kurs != null ? T("Kurs ") + Math.round(m.kurs) + "°" : "", m.squawk ? "Squawk " + m.squawk : "",
        m.lat != null ? koordinate(m.lat, "N", "S") + " " + koordinate(m.lon, "O", "W") : ""].filter(Boolean).join(" · ") || "–"),
      [k.halter ? T("Halter ") + k.halter + (k.land ? " (" + k.land + ")" : "") : "", k.airline && k.airline !== k.betreiber ? "Airline " + k.airline : ""].filter(Boolean).join(" · "),
      k.erste ? T("Erfasst ") + k.erste + " – " + k.letzte + (k.meldungen ? " · " + tausend(k.meldungen) + T(" Meldungen") : "") : "",
      T("Quellen: ") + (Object.keys(k.quellen || {}).sort().map(function (q) { return T(QUELLE_NAME[q] || q) + " " + nodeText(k.quellen[q]); }).join(" · ") || "–")
    ].filter(Boolean);
    zeilenMeta.forEach(function (zl, i) { doc.text(kuerzen(doc, zl, B - 8), x + 4, my + i * 1.85); });
    // Fuß (R889): aufnehmender Knoten mit seinen Fundus-Koordinaten; 3,4 mm vom
    // Rand – innerhalb des Sicherheitsabstands der Druckerei.
    doc.setFont("helvetica", "normal"); doc.setFontSize(3.9); doc.setTextColor(130, 135, 148);
    doc.text("Hex " + k.hex + (k.icao ? " · " + k.icao : ""), x + eckAbstand(), y + H - 3.4);
    var auf = k.aufnahme || kontext.aufnahme || {};
    var ort = auf.lat != null && (auf.lat !== 0 || auf.lon !== 0) ? koordinate(auf.lat, "N", "S") + " · " + koordinate(auf.lon, "O", "W") : T("Standort unbekannt");
    rechts(doc, kuerzen(doc, (auf.node ? auf.node + " · " : "") + ort, B - 26), x + B - eckAbstand(), y + H - 3.4);
  }

  // ── Rückseite als Vektorzeichnung (R892) ─────────────────────────────
  // Bisher ein Rasterbild (12 px/mm) – neben der Vektor-Vorderseite sichtbar
  // unscharf. Jetzt: Verlauf aus schmalen Farbbändern, Knotennetz und Logo als
  // Pfade, Schrift als Text. Keine Transparenz (PDF/X-1a): die halbdurch-
  // sichtigen Netzlinien werden vorab mit dem Grün an ihrer Stelle gemischt.
  // SVG-Pfad (M, L, A, Z – absolut) → Befehle für jsPDF doc.path(), Kreisbögen als Bézier
  function svgPfad(d, sk, dx, dy) {
    var t = d.match(/[MLAZmlaz]|-?\d*\.?\d+(?:e-?\d+)?/g), i = 0, ops = [], cx = 0, cy = 0, sx = 0, sy = 0;
    function n() { return parseFloat(t[i++]); }
    function P(x, y) { return [dx + x * sk, dy + y * sk]; }
    function bogen(x1, y1, rx, ry, phi, gross, sweep, x2, y2) {
      // Endpunkt- → Mittelpunktdarstellung (SVG 1.1, Anhang F.6), phi = 0 im Logo
      var c = Math.cos(phi), s = Math.sin(phi);
      var xp = c * (x1 - x2) / 2 + s * (y1 - y2) / 2, yp = -s * (x1 - x2) / 2 + c * (y1 - y2) / 2;
      var lam = (xp * xp) / (rx * rx) + (yp * yp) / (ry * ry);
      if (lam > 1) { rx *= Math.sqrt(lam); ry *= Math.sqrt(lam); }
      var f = Math.sqrt(Math.max(0, (rx * rx * ry * ry - rx * rx * yp * yp - ry * ry * xp * xp) / (rx * rx * yp * yp + ry * ry * xp * xp)));
      if (gross === sweep) f = -f;
      var cxp = f * rx * yp / ry, cyp = -f * ry * xp / rx;
      var mx = c * cxp - s * cyp + (x1 + x2) / 2, my = s * cxp + c * cyp + (y1 + y2) / 2;
      function wink(ux, uy, vx, vy) { var a = Math.atan2(ux * vy - uy * vx, ux * vx + uy * vy); return a; }
      var th1 = wink(1, 0, (xp - cxp) / rx, (yp - cyp) / ry);
      var dth = wink((xp - cxp) / rx, (yp - cyp) / ry, (-xp - cxp) / rx, (-yp - cyp) / ry);
      if (!sweep && dth > 0) dth -= 2 * Math.PI; else if (sweep && dth < 0) dth += 2 * Math.PI;
      var teile = Math.ceil(Math.abs(dth) / (Math.PI / 2)), st = dth / teile;
      for (var k = 0; k < teile; k++) {
        var a1 = th1 + k * st, a2 = a1 + st, h = 4 / 3 * Math.tan(st / 4);
        var p1 = [Math.cos(a1) - h * Math.sin(a1), Math.sin(a1) + h * Math.cos(a1)];
        var p2 = [Math.cos(a2) + h * Math.sin(a2), Math.sin(a2) - h * Math.cos(a2)];
        var p3 = [Math.cos(a2), Math.sin(a2)];
        var abb = function (p) { var X = rx * p[0], Y = ry * p[1]; return P(mx + c * X - s * Y, my + s * X + c * Y); };
        ops.push({ op: "c", c: abb(p1).concat(abb(p2), abb(p3)) });
      }
    }
    while (i < t.length) {
      var b = t[i++];
      if (b === "M") { cx = n(); cy = n(); sx = cx; sy = cy; ops.push({ op: "m", c: P(cx, cy) }); }
      else if (b === "L") { cx = n(); cy = n(); ops.push({ op: "l", c: P(cx, cy) }); }
      else if (b === "A") { var rx = n(), ry = n(), ph = n() * Math.PI / 180, ga = n(), sw = n(), x = n(), y = n(); bogen(cx, cy, rx, ry, ph, ga, sw, x, y); cx = x; cy = y; }
      else if (b === "Z" || b === "z") { ops.push({ op: "h", c: [] }); cx = sx; cy = sy; }
    }
    return ops;
  }

  var LOGO_D = null;
  function logoPfadLaden() {
    if (LOGO_D !== null) return Promise.resolve(LOGO_D);
    return fetch("/static/logo.svg", { credentials: "same-origin" }).then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (svg) { var m = svg.match(/ d="([^"]+)"/); LOGO_D = m ? m[1] : ""; return LOGO_D; })
      .catch(function () { LOGO_D = ""; return ""; });
  }
  // R915/R916: unten nicht mehr fast schwarz (6,53,31) – die schwarze Schrift
  // (R914) bleibt bis unten lesbar (Kontrast ≥ 4,5:1); oben heller, damit der
  // Verlauf deutlich sichtbar bleibt.
  var VERLAUF = [[0, [90, 230, 130]], [0.45, [10, 185, 95]], [1, [12, 130, 72]]];
  function verlaufFarbe(t) {
    t = Math.max(0, Math.min(1, t));
    for (var i = 1; i < VERLAUF.length; i++) {
      if (t <= VERLAUF[i][0]) {
        var a = VERLAUF[i - 1], b = VERLAUF[i], f = (t - a[0]) / (b[0] - a[0]);
        return [0, 1, 2].map(function (k) { return Math.round(a[1][k] + (b[1][k] - a[1][k]) * f); });
      }
    }
    return VERLAUF[VERLAUF.length - 1][1];
  }
  function mischen(farbe, deckkraft) { return farbe.map(function (c) { return Math.round(255 * deckkraft + c * (1 - deckkraft)); }); }

  // R920: SR-71 Blackbird nach dem NASA-Foto (Flug über der Sierra Nevada):
  // Umriss per Bildsegmentierung (GrabCut) gewonnen, dazu die tiefen Schatten
  // (Triebwerkseinläufe, Rumpfkante) als zweiter Ton – sonst verschwänden die
  // Gondeln in der Fläche. Bildpunkte, Nase links unten.
  var SR71 = { breite: 961, hoehe: 761, umriss: [[961,483],[959,478],[938,459],[924,449],[915,446],[900,435],[896,435],[879,423],[873,421],[862,411],[854,409],[827,391],[811,389],[804,390],[804,368],[802,369],[800,367],[802,361],[794,350],[794,338],[809,181],[797,188],[795,191],[761,317],[745,323],[730,333],[708,355],[705,362],[700,366],[695,363],[691,357],[687,358],[681,354],[685,348],[695,345],[697,343],[693,343],[696,340],[704,339],[700,334],[703,328],[699,321],[690,312],[680,307],[636,275],[632,274],[629,269],[624,269],[616,263],[613,263],[608,258],[606,251],[608,244],[604,238],[600,226],[599,216],[592,216],[589,213],[579,212],[559,215],[543,220],[537,215],[530,213],[514,204],[502,195],[498,195],[490,188],[484,188],[479,183],[475,183],[455,171],[451,171],[448,167],[450,163],[423,162],[411,158],[405,158],[398,154],[390,157],[386,156],[400,137],[405,119],[401,100],[395,92],[407,82],[437,50],[491,0],[468,9],[384,80],[374,82],[369,80],[346,79],[338,80],[318,88],[235,39],[222,33],[218,29],[183,13],[176,11],[165,11],[159,15],[159,25],[161,29],[165,59],[184,103],[189,109],[192,120],[189,124],[172,133],[159,146],[160,151],[150,159],[145,169],[141,183],[143,208],[140,215],[140,233],[142,241],[145,245],[137,272],[140,270],[150,272],[166,269],[180,275],[196,276],[210,272],[228,262],[243,259],[251,269],[258,288],[257,291],[262,298],[270,326],[274,331],[275,342],[270,348],[252,359],[125,449],[93,479],[61,518],[48,538],[28,575],[19,597],[4,646],[4,655],[1,667],[0,706],[5,751],[1,761],[3,759],[6,761],[18,758],[23,755],[64,754],[67,752],[132,742],[142,737],[158,734],[184,725],[189,725],[198,720],[214,717],[217,714],[238,706],[249,699],[252,699],[267,691],[270,692],[271,689],[292,677],[297,678],[296,676],[327,657],[375,619],[388,606],[388,602],[393,601],[417,575],[422,567],[431,558],[433,558],[434,553],[466,516],[466,514],[473,509],[491,484],[497,478],[502,476],[509,477],[520,475],[538,479],[545,477],[602,480],[615,483],[621,489],[621,494],[617,505],[617,521],[620,532],[626,542],[624,568],[629,566],[635,567],[648,562],[663,566],[680,565],[694,559],[711,546],[731,546],[744,541],[746,544],[750,539],[763,534],[766,536],[765,534],[773,527],[777,526],[798,511],[805,499],[812,492],[834,493],[889,500],[904,500],[956,492],[961,489]],
    schatten: [[[327,418],[325,419],[326,428],[324,432],[331,438],[341,430],[341,427]],[[760,320],[758,319],[736,330],[709,356],[702,367],[695,365],[691,368],[677,386],[667,409],[658,423],[658,427],[653,436],[647,441],[644,439],[644,436],[652,424],[652,418],[675,382],[676,375],[674,374],[650,393],[640,399],[620,429],[616,438],[609,444],[605,452],[592,467],[588,478],[612,480],[622,488],[622,495],[618,506],[618,520],[621,531],[627,541],[625,560],[629,560],[645,532],[654,521],[663,501],[674,490],[687,494],[697,504],[701,514],[701,527],[696,538],[688,546],[655,562],[664,565],[683,563],[708,547],[714,528],[713,512],[701,487],[682,471],[683,447],[689,438],[699,434],[701,422],[707,419],[714,421],[719,418],[723,419],[725,418],[725,415],[730,412],[728,408],[719,403],[724,396],[724,384],[732,379],[730,373],[732,364],[737,360],[748,359],[751,357]],[[571,246],[575,257],[567,265],[567,269],[564,272],[563,286],[565,300],[570,313],[583,328],[652,378],[671,360],[678,356],[684,347],[691,345],[695,339],[702,338],[699,335],[690,336],[673,320],[625,297],[602,304],[598,303],[598,299],[611,296],[614,292],[599,286],[588,275],[584,268],[582,256],[577,245],[574,244]],[[331,227],[328,226],[325,228],[325,232],[318,238],[312,249],[311,255],[306,261],[309,262],[313,259],[314,255],[321,254],[320,248],[324,244],[326,234],[331,230]],[[538,223],[532,222],[519,226],[459,258],[430,276],[428,279],[422,281],[407,294],[372,316],[369,320],[354,328],[350,335],[338,334],[332,340],[330,345],[319,348],[316,354],[309,356],[303,362],[290,369],[290,372],[286,376],[273,382],[264,393],[256,396],[256,398],[248,402],[245,407],[239,409],[235,414],[230,416],[229,421],[215,431],[213,435],[183,454],[168,469],[159,473],[130,479],[78,499],[71,507],[71,511],[77,523],[85,528],[87,532],[63,551],[54,564],[54,574],[56,579],[70,597],[68,598],[68,602],[64,605],[63,610],[53,610],[53,612],[58,614],[59,617],[56,619],[55,623],[47,626],[47,632],[41,638],[40,647],[35,652],[33,663],[27,676],[27,683],[23,691],[14,735],[8,749],[10,750],[16,746],[28,732],[30,726],[44,711],[46,705],[57,696],[70,678],[84,665],[94,651],[106,641],[110,626],[115,620],[120,618],[121,608],[125,602],[125,598],[134,595],[144,583],[148,586],[152,594],[156,594],[200,537],[210,516],[229,500],[240,499],[263,467],[263,452],[270,445],[280,441],[278,438],[269,442],[266,440],[266,437],[272,430],[290,422],[296,415],[304,411],[310,411],[315,415],[321,411],[329,411],[332,404],[341,397],[344,391],[364,375],[373,381],[375,380],[370,374],[370,371],[377,369],[382,372],[385,370],[386,366],[392,363],[395,354],[409,342],[411,336],[408,334],[408,328],[423,310],[436,301],[440,303],[450,297],[456,297],[458,291],[483,268],[484,264],[490,258],[508,246],[512,239],[538,225]],[[331,203],[329,202],[295,221],[293,225],[295,226],[329,207]],[[375,83],[347,80],[330,84],[304,94],[226,67],[177,81],[177,85],[180,89],[186,90],[209,103],[221,114],[221,117],[177,138],[151,160],[142,184],[144,209],[141,216],[141,232],[146,246],[141,262],[145,262],[186,205],[192,200],[200,200],[209,205],[219,218],[221,225],[220,241],[212,252],[191,263],[175,267],[171,270],[181,274],[195,275],[209,271],[220,265],[227,258],[232,248],[234,226],[225,203],[203,182],[214,166],[212,158],[220,153],[235,153],[235,150],[230,147],[235,140],[247,135],[260,126],[263,120],[306,93],[326,99],[340,113],[354,104],[364,94],[372,90],[375,87]]] };
  // rand (mm): Beschnitt; rund: abgerundete Ecken (A4-Faltvorlage)
  function rueckseiteVektor(doc, x, y, kontext, rand, rund) {
    rand = rand || 0;
    // Verlaufsrichtung wie bisher: von oben links nach (0,4 · Breite, Höhe)
    var gx = 0.4 * B, gy = H, gl = gx * gx + gy * gy;
    var tVon = function (px, py) { return ((px - x) * gx + (py - y) * gy) / gl; };
    doc.saveGraphicsState();
    if (rund) { doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, H - 1.2, 3, 3, null); }
    else { doc.rect(x - rand, y - rand, B + 2 * rand, H + 2 * rand, null); }
    doc.clip(); doc.discardPath();
    // Bänder senkrecht zur Verlaufsrichtung; erstes und letztes reichen weit
    // über die Karte hinaus (Beschnitt, Ecken).
    var N = 260, len = Math.sqrt(gl), ex = gx / len, ey = gy / len, nx = -ey, ny = ex, weit = 2 * (B + H);
    for (var i = 0; i < N; i++) {
      var t0 = i === 0 ? -0.5 : i / N, t1 = i === N - 1 ? 1.5 : (i + 1) / N + 0.6 / N;
      var a0x = x + t0 * gx, a0y = y + t0 * gy, a1x = x + t1 * gx, a1y = y + t1 * gy;
      var c = verlaufFarbe((i + 0.5) / N);
      doc.setFillColor(c[0], c[1], c[2]);
      doc.path([{ op: "m", c: [a0x - nx * weit, a0y - ny * weit] }, { op: "l", c: [a0x + nx * weit, a0y + ny * weit] },
        { op: "l", c: [a1x + nx * weit, a1y + ny * weit] }, { op: "l", c: [a1x - nx * weit, a1y - ny * weit] }, { op: "h", c: [] }]);
      doc.fill();
    }
    // R917: Schein um das Logo – zur Logomitte hin aufgehellt. Ringe aus
    // kleinen Feldern, jedes in der Verlaufsfarbe SEINER Stelle, nur mit Weiß
    // gemischt: Der äußere Rand geht so unsichtbar in den Verlauf über.
    // Ohne Transparenz (PDF/X-1a erlaubt keine).
    var gcx = x + B / 2, gcy = y + 34 + (H - 88) / 2, GR = 27;
    var schein = function (px, py) { var r = Math.sqrt((px - gcx) * (px - gcx) + (py - gcy) * (py - gcy)); return 0.62 * Math.pow(Math.max(0, 1 - r / GR), 1.6); };
    var grund = function (px, py) { return mischen(verlaufFarbe(tVon(px, py)), schein(px, py)); };
    (function () {
      var cx = gcx, cy = gcy, R = GR, NR = 28, NS = 56, TAU = 2 * Math.PI;
      for (var k = NR - 1; k >= 0; k--) {
        var r0 = R * k / NR, r1 = R * (k + 1) / NR + 0.05, rm = (r0 + r1) / 2;
        var w = 0.62 * Math.pow(Math.max(0, 1 - rm / R), 1.6);
        for (var j = 0; j < NS; j++) {
          var a0 = TAU * j / NS, a1 = TAU * (j + 1) / NS + 0.01, am = (a0 + a1) / 2;
          var c = mischen(verlaufFarbe(tVon(cx + rm * Math.cos(am), cy + rm * Math.sin(am))), w);
          doc.setFillColor(c[0], c[1], c[2]);
          doc.path([{ op: "m", c: [cx + r0 * Math.cos(a0), cy + r0 * Math.sin(a0)] }, { op: "l", c: [cx + r1 * Math.cos(a0), cy + r1 * Math.sin(a0)] },
            { op: "l", c: [cx + r1 * Math.cos(a1), cy + r1 * Math.sin(a1)] }, { op: "l", c: [cx + r0 * Math.cos(a1), cy + r0 * Math.sin(a1)] }, { op: "h", c: [] }]);
          doc.fill();
        }
      }
    })();
    // Knotennetz: fester Startwert – ALLE Karten haben exakt dieselbe Rückseite.
    var zufall = (function (a) { return function () { a = (a * 1664525 + 1013904223) >>> 0; return a / 4294967296; }; })(64);
    var knoten = [];
    for (var n = 0; n < 46; n++) knoten.push([x + 2 + zufall() * (B - 4), y + 2 + zufall() * (H - 4), (2 + zufall() * 4) / 10]);
    doc.setLineWidth(0.2);
    for (var a1 = 0; a1 < knoten.length; a1++) for (var b1 = a1 + 1; b1 < knoten.length; b1++) {
      var dx = knoten[a1][0] - knoten[b1][0], dy = knoten[a1][1] - knoten[b1][1], d = Math.sqrt(dx * dx + dy * dy);
      if (d < 16.5) {
        var lf = mischen(grund((knoten[a1][0] + knoten[b1][0]) / 2, (knoten[a1][1] + knoten[b1][1]) / 2), 0.28 * (1 - d / 16.5));
        doc.setDrawColor(lf[0], lf[1], lf[2]); doc.line(knoten[a1][0], knoten[a1][1], knoten[b1][0], knoten[b1][1]);
      }
    }
    knoten.forEach(function (p) {
      var kf = mischen(grund(p[0], p[1]), 0.55);
      doc.setFillColor(kf[0], kf[1], kf[2]); doc.circle(p[0], p[1], p[2], "F");
    });
    doc.restoreGraphicsState();
    // Rahmen
    var rf = mischen(verlaufFarbe(0.5), 0.45);
    doc.setDrawColor(rf[0], rf[1], rf[2]); doc.setLineWidth(0.4);
    doc.roundedRect(x + 4, y + 4, B - 8, H - 8, 2, 2, "S");
    // Schrift und Logo (Maße wie bisher: 1 mm = 10 Einheiten der Rasterfassung)
    // R914: schwarz, in Inter – FUNDUS fett (Black wie auf der Seite);
    // darunter der Zeitstempel der Erstellung.
    var dyMm = (H - 88) / 2;
    function zeile(s, yy, mm, abstand, stil) {
      var pt = mm / 0.3528;
      if (INTER) doc.setFont("Inter", stil);
      else doc.setFont("helvetica", stil === "normal" ? "normal" : "bold");
      doc.setFontSize(pt); doc.setTextColor(0, 0, 0);
      var breite = doc.getTextWidth(s) + abstand * (sicher(s).length - 1);
      doc.text(s, x + (B - breite) / 2, yy, abstand ? { charSpace: abstand } : undefined);
    }
    zeile("FUNDUS", y + 13.2 + dyMm, 5.6, 1.4, "black");
    // R921: Adresse darunter, genau so breit wie FUNDUS (Zeichenabstand passt sich an)
    (function () {
      var url = "fnd.resolve.bar", n = url.length;
      if (INTER) doc.setFont("Inter", "black"); else doc.setFont("helvetica", "bold");
      doc.setFontSize(5.6 / 0.3528);
      var ziel = doc.getTextWidth("FUNDUS") + 1.4 * 5;
      if (INTER) doc.setFont("Inter", "bold"); else doc.setFont("helvetica", "bold");
      var mm = 2.2, w;
      do { doc.setFontSize(mm / 0.3528); w = doc.getTextWidth(url); if (w <= ziel) break; mm -= 0.1; } while (mm > 1.2);
      var abst = (ziel - w) / (n - 1);
      doc.setTextColor(0, 0, 0);
      doc.text(url, x + (B - ziel) / 2, y + 16.4 + dyMm, abst > 0 ? { charSpace: abst } : undefined);
    })();
    if (LOGO_D) {
      doc.setFillColor(0, 0, 0);
      doc.path(svgPfad(LOGO_D, 33 / 200, x + (B - 33) / 2, y + 17.5 + dyMm));
      doc.fillEvenOdd();
    }
    zeile(kontext.spiel || "FLIGHT QUAD 64", y + 59.3 - 2.4 + dyMm, 5.4, 0, "bold"); // R920: 2,4 mm höher
    zeile("Best of " + (kontext.code || ""), y + 65.3 - 2.4 + dyMm, 3.8, 0, "bold");
    // R918–R920: SR-71 Blackbird zwischen „Best of …“ und „TKM x HELIX“ –
    // dunkelgrau mit schwarzen Schatten (wie eine Schablone).
    (function () {
      var oben = y + 65.3 - 2.4 + dyMm + 0.9, unten = y + H - 10.4 - 2.8;
      var sk = 0.97 * (unten - oben) / SR71.hoehe, bx = x + (B - SR71.breite * sk) / 2, by = (oben + unten) / 2 - SR71.hoehe * sk / 2;
      var flaeche = function (pkte) {
        var weg = pkte.map(function (p, i) { return { op: i ? "l" : "m", c: [bx + p[0] * sk, by + p[1] * sk] }; });
        weg.push({ op: "h", c: [] }); doc.path(weg); doc.fill();
      };
      doc.setFillColor(40, 43, 49); flaeche(SR71.umriss);
      doc.setFillColor(0, 0, 0); SR71.schatten.forEach(flaeche);
    })();
    zeile("TKM x HELIX", y + H - 10.4, 3.0, 0.6, "bold");
    if (kontext.erstellt) zeile(kontext.erstellt, y + H - 7.2, 2.2, 0.3, "normal");
  }

  // ── Titelkarte (R890): Spielname, Zeitraum, die acht Quartette, Regeln ──
  var KATEGORIEN = [["hoehe_max", "Höchste Höhe"], ["hoehe_min", "Niedrigste Höhe"], ["geschw_max", "Schnellstes"],
    ["geschw_min", "Langsamstes"], ["steig_max", "Stärkstes Steigen"], ["sink_max", "Stärkstes Sinken"],
    ["km_max", "Weiteste Entfernung"], ["km_min", "Nächste Entfernung"]];
  function titelkarte(doc, x, y, kontext, rand) {
    rand = rand || 0;
    var gruen = [0, 150, 72];
    if (rand) {
      doc.setFillColor(255, 255, 255); doc.rect(x - rand, y - rand, B + 2 * rand, H + 2 * rand, "F");
      doc.setFillColor(gruen[0], gruen[1], gruen[2]); doc.rect(x - rand, y - rand, B + 2 * rand, 22 + rand, "F");
    } else {
      doc.setDrawColor(200); doc.setLineWidth(0.2); doc.setFillColor(255, 255, 255);
      doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, H - 1.2, 3, 3, "FD");
      doc.setFillColor(gruen[0], gruen[1], gruen[2]);
      doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, 21.4, 3, 3, "F"); doc.rect(x + 0.6, y + 12, B - 1.2, 10, "F");
    }
    doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(13);
    mitte(doc, kontext.spiel, x + B / 2, y + 9.6);
    doc.setFontSize(6.4); mitte(doc, "Best of " + (kontext.code || "") + " · " + zeitraumTitel(kontext.code, kontext.titel), x + B / 2, y + 14.6);
    doc.setFont("helvetica", "normal"); doc.setFontSize(4.6); mitte(doc, "FUNDUS · TKM x HELIX", x + B / 2, y + 19.0);
    // Die acht Quartette
    var qy = y + 27;
    doc.setFont("helvetica", "bold"); doc.setFontSize(6.2); doc.setTextColor(30, 30, 30);
    doc.text(T("Die Quartette"), x + 4, qy);
    KATEGORIEN.forEach(function (kat, i) {
      var zy = qy + 2.2 + i * 4.0, f = KAT_FARBE[kat[0]] || [80, 80, 80];
      doc.setFillColor(f[0], f[1], f[2]); doc.roundedRect(x + 4, zy + 0.4, 9, 3.0, 0.8, 0.8, "F");
      doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(5);
      mitte(doc, kontext.anzahl === 32 ? String(i + 1) : (2 * i + 1) + "·" + (2 * i + 2), x + 8.5, zy + 2.6);
      doc.setTextColor(40, 40, 40); doc.setFontSize(5.6); doc.setFont("helvetica", "normal");
      doc.text(katTitel(kat[0], kat[1]), x + 15, zy + 2.7);
    });
    // Spielregeln
    var ry = qy + 2.2 + 8 * 4.0 + 3.4;
    doc.setFont("helvetica", "bold"); doc.setFontSize(6.2); doc.setTextColor(30, 30, 30);
    doc.text(T("Spielregeln"), x + 4, ry);
    doc.setFont("helvetica", "normal"); doc.setFontSize(4.5); doc.setTextColor(50, 50, 50);
    var regeln = T("regeln");
    // 59 × 91 (WIRmachenDRUCK, R901): rechts unten setzt die Druckerei einen
    // QR-Code, am rechten Rand ihren Vermerk – dort bleibt die Titelkarte frei.
    var frei = B === 59 && H === 91, textBreite = frei ? B - 11 : B - 8, fussBreite = frei ? 47 - 4 - 1.5 : B - 8;
    doc.splitTextToSize(sicher(regeln), textBreite).forEach(function (zl, i) { doc.text(zl, x + 4, ry + 2.6 + i * 1.95); });
    // Herkunft der Daten
    var auf = kontext.aufnahme || {};
    doc.setFontSize(3.9); doc.setTextColor(120, 120, 120);
    doc.text(kuerzen(doc, (kontext.flugzeuge ? tausend(kontext.flugzeuge) + T(" Flugzeuge aufgezeichnet · ADS-B-Empfang ") : "ADS-B " ) + (auf.node || kontext.node || ""), fussBreite), x + 4, y + H - 5.4);
    doc.text(kuerzen(doc, auf.lat != null ? koordinate(auf.lat, "N", "S") + " · " + koordinate(auf.lon, "O", "W") + " · " + T("Typen adsbdb/hexdb · Routen adsb.lol") : T("Typen adsbdb/hexdb · Routen adsb.lol"), fussBreite), x + eckAbstand(), y + H - 3.4);
  }

  function schnittmarken(doc, x, y, w, h) {
    doc.setDrawColor(150); doc.setLineWidth(0.15); doc.setLineDashPattern([], 0);
    [[x, y, -1, -1], [x + w, y, 1, -1], [x, y + h, -1, 1], [x + w, y + h, 1, 1]].forEach(function (p) {
      doc.line(p[0] + p[2] * 1, p[1], p[0] + p[2] * 5, p[1]);
      doc.line(p[0], p[1] + p[3] * 1, p[0], p[1] + p[3] * 5);
    });
  }

  // deckend (R891): Bild auf eine Grundfarbe legen und als JPEG ausgeben –
  // ohne Alphakanal, also ohne Transparenz im PDF.
  function deckend(daten, grund, druckfarben) {
    return new Promise(function (ok) {
      var img = new Image();
      img.onload = function () {
        var cv = document.createElement("canvas"); cv.width = img.naturalWidth; cv.height = img.naturalHeight;
        var g = cv.getContext("2d"); g.fillStyle = grund || "#ffffff"; g.fillRect(0, 0, cv.width, cv.height); g.drawImage(img, 0, 0);
        if (druckfarben) bildDruckfarben(g, cv.width, cv.height);
        // R903: volle Auflösung, hohe Qualität (das Bild wird nur umgefärbt)
        ok({ daten: cv.toDataURL("image/jpeg", 0.96), w: cv.width, h: cv.height });
      };
      img.onerror = function () { ok(null); };
      img.src = daten;
    });
  }

  // Foto vom Node holen: Daten, Format und Seitenverhältnis.
  var fotoCache = {};
  function fotoLaden(url) {
    if (fotoCache[url]) return fotoCache[url];
    fotoCache[url] = fetch(url, { credentials: "same-origin" }).then(function (r) {
      if (!r.ok) throw new Error("Foto fehlt");
      return r.blob();
    }).then(function (b) {
      return new Promise(function (ok, fehler) {
        var fr = new FileReader();
        fr.onload = function () {
          var img = new Image();
          img.onload = function () { ok({ daten: fr.result, format: b.type === "image/png" ? "PNG" : "JPEG", w: img.naturalWidth, h: img.naturalHeight, alias: "foto-" + url.split("/").pop() }); };
          img.onerror = fehler;
          img.src = fr.result;
        };
        fr.onerror = fehler;
        fr.readAsDataURL(b);
      });
    }).catch(function () { return null; });
    return fotoCache[url];
  }

  // opt.groesse: "63x88" (Standard) oder "59x91" (R890).
  function erzeugen(daten, format, fortschritt, opt) {
    var jsPDF = window.jspdf.jsPDF;
    var masse = { "63x88": [63, 88], "59x91": [59, 91] }[(opt && opt.groesse) || "63x88"] || [63, 88];
    SPR = opt && opt.sprache === "en" ? "en" : "de"; // R894
    B = masse[0]; H = masse[1];
    var karten = daten.karten || [];
    var anzahl = daten.anzahl === 32 || (!daten.anzahl && karten.length <= 32) ? 32 : 64;
    // R914: Erstellungszeitpunkt (Ortszeit) als JJJJMMTThhmmss auf jeder Rückseite
    var jetzt = new Date(), zz = function (n) { return (n < 10 ? "0" : "") + n; };
    var erstellt = jetzt.getFullYear() + zz(jetzt.getMonth() + 1) + zz(jetzt.getDate()) + zz(jetzt.getHours()) + zz(jetzt.getMinutes()) + zz(jetzt.getSeconds());
    var kontext = { titel: daten.titel, node: daten.node, code: daten.code, aufnahme: daten.aufnahme,
      anzahl: anzahl, spiel: "FLIGHT QUAD " + anzahl, flugzeuge: daten.flugzeuge, erstellt: erstellt };
    var klassen = {};
    karten.forEach(function (k) { klassen[k.klasse] = true; });
    var istA4 = format === "a4" || format === "a4_druck";
    var bilder = {}, druck = format !== "a4"; // a4_druck: wie die Druckformate (R892)
    var vorbereiten = Object.keys(klassen).map(function (kl) {
      return silhouetteBild(kl, "#1f2a3a", false).then(function (b) {
        // Druckformate: Silhouette auf die Bildfeld-Farbe, ohne Alphakanal (R891)
        if (!druck) { bilder[kl] = b; return; }
        return deckend(b, "#e8f1fa", true).then(function (d) { bilder[kl] = d ? d.daten : b; });
      });
    });
    // R903: Fotos in voller Auflösung nacheinander vorbereiten (nicht alle
    // gleichzeitig – sonst lägen Dutzende große Bilder zugleich im Speicher),
    // jedes Foto nur einmal, auch wenn mehrere Karten es zeigen.
    var fotoKette = Promise.resolve(), druckFoto = {}, fotoNr = 0;
    var fotoUrls = karten.filter(function (k) { return k.foto && k.foto.url; }).map(function (k) { return k.foto.url; })
      .filter(function (u, i, a) { return a.indexOf(u) === i; });
    karten.forEach(function (k) {
      if (!(k.foto && k.foto.url)) return;
      var u = k.foto.url;
      fotoKette = fotoKette.then(function () {
        if (!(u in druckFoto)) {
          fotoNr++;
          if (opt && opt.meldung) opt.meldung(fotoNr, fotoUrls.length);
          druckFoto[u] = fotoLaden(u).then(function (f) {
            if (!f || !druck) return f;
            return deckend(f.daten, "#ffffff", true).then(function (d) {
              return d ? { daten: d.daten, format: "JPEG", w: d.w, h: d.h, alias: f.alias + "-j" } : f;
            });
          });
        }
        return druckFoto[u].then(function (f) { k._foto = f; });
      });
    });
    vorbereiten.push(fotoKette);
    // Zuerst die Titelkarte, dann die Spielkarten.
    var alle = [{ _titel: true }].concat(karten);
    function vorne(d, x, y, k, rand) {
      if (k._titel) titelkarte(d, x, y, kontext, rand);
      else vorderseite(d, x, y, k, kontext, bilder[k.klasse] || bilder.unbekannt, rand);
    }
    vorbereiten.push(logoPfadLaden());
    return Promise.all(vorbereiten).then(function () {
      var doc;
      if (istA4) {
        // Streifen (2 × Kartenhöhe) × Kartenbreite: Rückseite links (liegend,
        // Kopf zur Falzlinie), Vorderseite rechts, um 90° gegen den Uhrzeiger-
        // sinn – ihre Oberkante liegt an der Falzlinie.
        doc = sicherMachen(new jsPDF({ unit: "mm", format: "a4" }), { neutral: druck, druck: druck });
        if (druck) doc._deckend = true; // PDF/X-1a: keine Transparenz (R892)
        // y0 = 22: unterster Streifen endet ≥ 5 mm vor dem Blattrand (R888).
        var proSeite = 4, abstand = 6, x0 = (210 - 2 * H) / 2, y0 = 22;
        var seiten = Math.ceil(alle.length / proSeite);
        alle.forEach(function (k, i) {
          var reihe = i % proSeite;
          if (i > 0 && reihe === 0) doc.addPage("a4");
          if (reihe === 0) {
            doc.setFont("helvetica", "normal"); doc.setFontSize(7.5); doc.setTextColor(110);
            doc.text(kontext.spiel + " · " + daten.code + " · " + zeitraumTitel(daten.code, daten.titel) + " · " + T("seite", { n: Math.floor(i / proSeite) + 1, m: seiten }), 105, 12, { align: "center" });
            doc.text(T("a4kopf", { B: B, H: H }), 105, 17, { align: "center" });
          }
          var y = y0 + reihe * (B + abstand), falz = x0 + H;
          if (druck && reihe === 0) {
            // PDF/X verlangt einen Endformatrahmen; bei A4 ist es das Blatt.
            var f0 = doc.internal.scaleFactor, pc0 = doc.internal.getCurrentPageInfo().pageContext;
            pc0.trimBox = { bottomLeftX: 0, bottomLeftY: 0, topRightX: 210 * f0, topRightY: 297 * f0 };
          }
          // Rückseite gedreht (90° im Uhrzeigersinn, Kopf zur Falzlinie):
          // Kartenpunkt (u, v) → Seite (falz − v, y + u).
          doc.advancedAPI(function (d) {
            d.saveGraphicsState();
            d.setCurrentTransformationMatrix(new d.Matrix(0, 1, -1, 0, falz, y));
            rueckseiteVektor(d, 0, 0, kontext, 0, true);
            d.restoreGraphicsState();
          });
          // Vorderseite gedreht zeichnen: Kartenpunkt (x, y) → Seite (falz + y, unten − x).
          doc.advancedAPI(function (d) {
            d.saveGraphicsState();
            d.setCurrentTransformationMatrix(new d.Matrix(0, -1, 1, 0, falz, y + B));
            vorne(d, 0, 0, k, 0);
            d.restoreGraphicsState();
          });
          schnittmarken(doc, x0, y, 2 * H, B);
          doc.setDrawColor(150); doc.setLineWidth(0.15); doc.setLineDashPattern([1.2, 1.2], 0);
          doc.line(falz, y + 1, falz, y + B - 1);
          doc.setLineDashPattern([], 0);
          if (fortschritt) fortschritt(i + 1, alle.length);
        });
      } else {
        // "karten" und "druckerei" (R889/R890): Einzelseiten im Endformat plus
        // 3 mm Beschnitt, nach jeder Vorderseite ihre Rückseite; Texte
        // mindestens 3 mm innerhalb der Schnittkante. Bei "druckerei" macht der
        // Node danach CMYK, Farbdeckung, Schriften als Pfade und PDF/X-1a.
        var PB = B + 2 * RAND, PH = H + 2 * RAND;
        doc = sicherMachen(new jsPDF({ unit: "mm", format: [PB, PH] }), { neutral: true, druck: true });
        doc._deckend = true; // keine Transparenz (R891)
        // Endformat- und Beschnittrahmen je Seite: geschnitten wird an der
        // TrimBox (3 mm innen); Ghostscript übernimmt die Rahmen unverändert.
        var rahmen = function () {
          var f = doc.internal.scaleFactor, r = RAND * f, w = PB * f, h = PH * f;
          var pc = doc.internal.getCurrentPageInfo().pageContext;
          pc.trimBox = { bottomLeftX: r, bottomLeftY: r, topRightX: w - r, topRightY: h - r };
          pc.bleedBox = { bottomLeftX: 0, bottomLeftY: 0, topRightX: w, topRightY: h };
        };
        alle.forEach(function (k, i) {
          if (i > 0) doc.addPage([PB, PH]);
          rahmen();
          vorne(doc, RAND, RAND, k, RAND);
          doc.addPage([PB, PH]);
          rahmen();
          rueckseiteVektor(doc, RAND, RAND, kontext, RAND, false);
          if (fortschritt) fortschritt(i + 1, alle.length);
        });
      }
      doc.setProperties({ title: kontext.spiel + " – " + daten.code, creator: "Fundus Aeronautik" });
      return doc;
    });
  }

  window.FundusQuartett = { erzeugen: erzeugen, silhouetteBild: silhouetteBild, KLASSE_NAME: KLASSE_NAME };
})();
