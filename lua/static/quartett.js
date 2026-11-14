// Flugquartett als PDF (R877) – im Browser mit jsPDF (static/jspdf.umd.min.js).
// Daten: /api/v1/aeronautik/quartett. Formate:
//   "karten": eine Karte je Seite in Spielkartengröße 63 × 88 mm, am Ende eine
//             Rückseite (so verlangen es die meisten Kartendruckereien)
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
  function tausend(n) { return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, "."); }
  function komma(n, st) { return n.toFixed(st).replace(".", ","); }
  function wertText(art, w) {
    if (!w) return "–";
    switch (art) {
      case "hoehe": return tausend(w.v) + " m";
      case "geschw": return tausend(w.v) + " km/h";
      // Gedankenstrich statt Minuszeichen: U+2212 fehlt in der PDF-Standardschrift.
      case "steig": return (w.v > 0 ? "+" : w.v < 0 ? "–" : "") + komma(Math.abs(w.v), 1) + " m/s";
      case "km": return komma(w.v, 1) + " km";
    }
    return String(w.v);
  }
  function rekordText(k) {
    var r = k.rekord, e = r.einheit;
    var v = e === "m" ? tausend(r.v) : e === "km/h" ? tausend(r.v) : e === "m/s" ? (r.v > 0 ? "+" : "–") + komma(Math.abs(r.v), 1) : komma(r.v, 1);
    return k.kategorie_titel + ": " + v + " " + e;
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

  // Rechtsbündig/zentriert selbst rechnen: jsPDF rechnet die Ausrichtung in
  // einer gedrehten Zeichnung (A4-Faltvorlage) falsch herum.
  function rechts(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s), y); }
  function mitte(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s) / 2, y); }

  // ── Vorderseite (Vektor) ─────────────────────────────────────────────────
  function vorderseite(doc, x, y, k, kontext, bild) {
    var f = KAT_FARBE[k.kategorie] || [80, 80, 80];
    doc.setDrawColor(200); doc.setLineWidth(0.2); doc.setFillColor(255, 255, 255);
    doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, H - 1.2, 3, 3, "FD");
    // Kopf: Nummer und Kategorie
    doc.setFillColor(f[0], f[1], f[2]);
    doc.roundedRect(x + 0.6, y + 0.6, B - 1.2, 11, 3, 3, "F");
    doc.rect(x + 0.6, y + 6, B - 1.2, 5.6, "F");
    doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(15);
    doc.text(k.nr, x + 4, y + 9);
    doc.setFontSize(6.2); rechts(doc, k.kategorie_titel.toUpperCase(), x + B - 4, y + 5.2);
    doc.setFont("helvetica", "normal"); doc.setFontSize(5.4);
    rechts(doc, KLASSE_NAME[k.klasse] || "Flugzeug", x + B - 4, y + 9.2);
    // Typ und Kennungen
    doc.setTextColor(25, 28, 36); doc.setFont("helvetica", "bold"); doc.setFontSize(9);
    var typ = k.typ || (k.icao ? "Typ " + k.icao : "Unbekannter Typ");
    if (k.hersteller && typ.toUpperCase().indexOf(k.hersteller.toUpperCase().split(" ")[0]) < 0 && k.hersteller.length < 18) typ = k.hersteller.split(" ")[0] + " " + typ;
    doc.text(kuerzen(doc, typ, B - 8), x + 4, y + 16.2);
    doc.setFont("helvetica", "normal"); doc.setFontSize(6); doc.setTextColor(95, 100, 112);
    var unter = [k.kennzeichen, (k.flug || []).join(", "), k.route].filter(Boolean).join(" · ") || "ohne Kennzeichen";
    doc.text(kuerzen(doc, unter, B - 8), x + 4, y + 19.6);
    // Bild: Foto (ganz, ohne Beschnitt) mit Urheberzeile – sonst Silhouette.
    doc.setFillColor(232, 241, 250); doc.rect(x + 3, y + 21.6, B - 6, 20.4, "F");
    if (k._foto) {
      var fb = B - 6, fh = 20.4 - 2.6, s = Math.min(fb / k._foto.w, fh / k._foto.h), w = k._foto.w * s, h = k._foto.h * s;
      doc.addImage(k._foto.daten, k._foto.format, x + 3 + (fb - w) / 2, y + 21.6 + (fh - h) / 2, w, h, k._foto.alias, "FAST");
      doc.setFont("helvetica", "normal"); doc.setFontSize(3.6); doc.setTextColor(95, 100, 112);
      mitte(doc, urheberZeile(doc, k.foto.credit, B - 8), x + B / 2, y + 41.2);
    } else {
      // Alias: jede Silhouette nur EINMAL einbetten, sonst wird das PDF riesig.
      doc.addImage(bild, "PNG", x + 6.5, y + 22.6, 50, 18.75, "silh-" + (k.klasse || "unbekannt"), "FAST");
    }
    // Rekord
    doc.setFillColor(f[0], f[1], f[2]); doc.roundedRect(x + 3, y + 43.6, B - 6, 5.4, 1.4, 1.4, "F");
    doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(6.2);
    mitte(doc, kuerzen(doc, "Rekord · " + rekordText(k), B - 10), x + B / 2, y + 47.2);
    // Vier Spielwerte mit Zeitstempel
    var zeilen = [["hoehe", "Höhe"], ["geschw", "Geschwindigkeit"], ["steig", "Steigen/Sinken"], ["km", "Entfernung"]];
    zeilen.forEach(function (z, i) {
      var zy = y + 50.6 + i * 7.1, w = k.werte[z[0]];
      if (i % 2 === 0) { doc.setFillColor(243, 245, 248); doc.rect(x + 3, zy, B - 6, 7.1, "F"); }
      var aktiv = k.kategorie.indexOf(z[0]) === 0 || (z[0] === "steig" && k.kategorie === "sink_max");
      doc.setTextColor(aktiv ? f[0] : 45, aktiv ? f[1] : 48, aktiv ? f[2] : 58);
      doc.setFont("helvetica", "bold"); doc.setFontSize(7); doc.text(z[1], x + 5, y + 50.6 + i * 7.1 + 3.2);
      doc.setFontSize(9); rechts(doc, wertText(z[0], w), x + B - 5, y + 50.6 + i * 7.1 + 4.6);
      doc.setFont("helvetica", "normal"); doc.setFontSize(4.8); doc.setTextColor(130, 135, 148);
      doc.text(w ? w.t : "–", x + 5, y + 50.6 + i * 7.1 + 5.9);
    });
    // Fuß
    doc.setFontSize(4.6); doc.setTextColor(130, 135, 148);
    doc.text("Hex " + k.hex + (k.icao ? " · " + k.icao : ""), x + 4, y + H - 3.4);
    rechts(doc, "FLIGHT QUAD 64 · " + (kontext.code || ""), x + B - 4, y + H - 3.4);
  }

  // ── Rückseite (als Bild, damit sie sich für das Falten drehen lässt) ─────
  // Fundus-Logo zentriert, darunter FLIGHT QUAD 64 und der Zeitraum als Code
  // (2026, 202603, 20261114, 2026W46). Das Logo kommt aus /static/logo.svg –
  // gezeichnet in currentColor, hier weiß eingefärbt.
  function logoBild() {
    return fetch("/static/logo.svg", { credentials: "same-origin" }).then(function (r) {
      if (!r.ok) throw new Error("Logo fehlt");
      return r.text();
    }).then(function (svg) {
      svg = svg.replace(/currentColor/g, "#ffffff").replace("<svg ", '<svg width="800" height="800" ');
      return new Promise(function (ok) {
        var img = new Image();
        img.onload = function () { ok(img); };
        img.onerror = function () { ok(null); };
        img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
      });
    }).catch(function () { return null; });
  }

  // drehung 90: um 90° im Uhrzeigersinn, liegend (Kopf rechts, zur Falzlinie).
  function rueckseite(kontext, drehung) {
    return logoBild().then(function (logo) {
      var s = 10, cv = document.createElement("canvas"), w = B * s, h = H * s;
      if (drehung === 90) { cv.width = h; cv.height = w; } else { cv.width = w; cv.height = h; }
      var g = cv.getContext("2d");
      if (drehung === 90) { g.translate(h, 0); g.rotate(Math.PI / 2); }
      var gr = g.createLinearGradient(0, 0, 0, h); gr.addColorStop(0, "#123a6b"); gr.addColorStop(1, "#0a1c36");
      g.fillStyle = gr; g.beginPath();
      if (g.roundRect) g.roundRect(6, 6, w - 12, h - 12, 30); else g.rect(6, 6, w - 12, h - 12);
      g.fill();
      g.strokeStyle = "rgba(255,255,255,.35)"; g.lineWidth = 4; g.beginPath();
      if (g.roundRect) g.roundRect(40, 40, w - 80, h - 80, 20); else g.rect(40, 40, w - 80, h - 80);
      g.stroke();
      var groesse = 380, ly = 170;
      if (logo) g.drawImage(logo, (w - groesse) / 2, ly, groesse, groesse);
      g.fillStyle = "#ffffff"; g.textAlign = "center";
      g.font = "bold 58px Helvetica, Arial, sans-serif";
      g.fillText("FLIGHT QUAD 64", w / 2, ly + groesse + 95);
      g.font = "bold 40px Helvetica, Arial, sans-serif"; g.fillStyle = "rgba(255,255,255,.85)";
      g.fillText(kontext.code || "", w / 2, ly + groesse + 155);
      return cv.toDataURL("image/png");
    });
  }

  function schnittmarken(doc, x, y, w, h) {
    doc.setDrawColor(150); doc.setLineWidth(0.15); doc.setLineDashPattern([], 0);
    [[x, y, -1, -1], [x + w, y, 1, -1], [x, y + h, -1, 1], [x + w, y + h, 1, 1]].forEach(function (p) {
      doc.line(p[0] + p[2] * 1, p[1], p[0] + p[2] * 5, p[1]);
      doc.line(p[0], p[1] + p[3] * 1, p[0], p[1] + p[3] * 5);
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

  function erzeugen(daten, format, fortschritt) {
    var jsPDF = window.jspdf.jsPDF;
    var karten = daten.karten || [];
    var kontext = { titel: daten.titel, node: daten.node, code: daten.code };
    var klassen = {};
    karten.forEach(function (k) { klassen[k.klasse] = true; });
    var bilder = {};
    var vorbereiten = Object.keys(klassen).map(function (kl) {
      return silhouetteBild(kl, "#1f2a3a", false).then(function (b) { bilder[kl] = b; });
    });
    karten.forEach(function (k) {
      if (k.foto && k.foto.url) vorbereiten.push(fotoLaden(k.foto.url).then(function (f) { k._foto = f; }));
    });
    return Promise.all(vorbereiten).then(function () {
      return rueckseite(kontext, format === "a4" ? 90 : 0);
    }).then(function (rueck) {
      var doc;
      if (format === "a4") {
        // Streifen 176 × 63 mm: Rückseite links (liegend, Kopf zur Falzlinie),
        // Vorderseite rechts, um 90° gegen den Uhrzeigersinn – ihre Oberkante
        // liegt an der Falzlinie.
        doc = new jsPDF({ unit: "mm", format: "a4" });
        var proSeite = 4, abstand = 6, x0 = (210 - 2 * H) / 2, y0 = 24;
        var seiten = Math.ceil(karten.length / proSeite);
        karten.forEach(function (k, i) {
          var reihe = i % proSeite;
          if (i > 0 && reihe === 0) doc.addPage("a4");
          if (reihe === 0) {
            doc.setFont("helvetica", "normal"); doc.setFontSize(7.5); doc.setTextColor(110);
            doc.text("FLIGHT QUAD 64 · " + daten.code + " · " + daten.titel + " · Seite " + (Math.floor(i / proSeite) + 1) + " von " + seiten, 105, 12, { align: "center" });
            doc.text("Karten in Originalgröße 63 × 88 mm. An den Ecken ausschneiden, an der gestrichelten Oberkante nach hinten falten, zusammenkleben.", 105, 17, { align: "center" });
          }
          var y = y0 + reihe * (B + abstand), falz = x0 + H;
          doc.addImage(rueck, "PNG", x0, y, H, B, "rueckseite", "FAST");
          // Vorderseite gedreht zeichnen: Kartenpunkt (x, y) → Seite (falz + y, unten − x).
          doc.advancedAPI(function (d) {
            d.saveGraphicsState();
            d.setCurrentTransformationMatrix(new d.Matrix(0, -1, 1, 0, falz, y + B));
            vorderseite(d, 0, 0, k, kontext, bilder[k.klasse] || bilder.unbekannt);
            d.restoreGraphicsState();
          });
          schnittmarken(doc, x0, y, 2 * H, B);
          doc.setDrawColor(150); doc.setLineWidth(0.15); doc.setLineDashPattern([1.2, 1.2], 0);
          doc.line(falz, y + 1, falz, y + B - 1);
          doc.setLineDashPattern([], 0);
          if (fortschritt) fortschritt(i + 1, karten.length);
        });
      } else {
        doc = new jsPDF({ unit: "mm", format: [B, H] });
        karten.forEach(function (k, i) {
          if (i > 0) doc.addPage([B, H]);
          vorderseite(doc, 0, 0, k, kontext, bilder[k.klasse] || bilder.unbekannt);
          if (fortschritt) fortschritt(i + 1, karten.length);
        });
        doc.addPage([B, H]);
        doc.addImage(rueck, "PNG", 0, 0, B, H, "rueckseite", "FAST");
      }
      doc.setProperties({ title: "FLIGHT QUAD 64 – " + daten.code, creator: "Fundus Aeronautik" });
      return doc;
    });
  }

  window.FundusQuartett = { erzeugen: erzeugen, silhouetteBild: silhouetteBild, KLASSE_NAME: KLASSE_NAME };
})();
