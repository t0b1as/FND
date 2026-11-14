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
    var v = (k.moment || {}).v, e = k.einheit || "";
    if (v == null) return k.kategorie_titel;
    var s = e === "m" || e === "km/h" ? tausend(v) : e === "m/s" ? (v > 0 ? "+" : "–") + komma(Math.abs(v), 1) : komma(v, 1);
    return k.kategorie_titel + " " + s + " " + e;
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
  function sicherMachen(doc, opt) {
    var text = doc.text, breite = doc.getTextWidth;
    if (opt && opt.neutral) {
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
    if (sa) return "bearbeitet, " + sa[1].trim();
    if (/·\s*CC BY[^·]*·/i.test(c)) return "bearbeitet";
    return "bearbeitet";
  }
  // Grundlinie für Text, der in einem Feld (oben, Höhe) senkrecht mittig steht:
  // halbe Feldhöhe plus halbe Großbuchstabenhöhe (≈ 0,7 der Schriftgröße).
  function senkrechtMitte(oben, hoehe, pt) { return oben + hoehe / 2 + pt * 0.3528 * 0.7 / 2; }

  // Rechtsbündig/zentriert selbst rechnen: jsPDF rechnet die Ausrichtung in
  // einer gedrehten Zeichnung (A4-Faltvorlage) falsch herum.
  function rechts(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s), y); }
  function mitte(doc, s, x, y) { doc.text(s, x - doc.getTextWidth(s) / 2, y); }

  // ── Vorderseite (Vektor) ─────────────────────────────────────────────────
  function koordinate(v, pos, neg) { return v == null ? "" : komma(Math.abs(v), 4) + "° " + (v >= 0 ? pos : neg); }
  // Flughafen kurz (Code) und lesbar (Name, Stadt, Land) – R882.
  function flughafenLang(f) {
    if (!f) return "";
    var name = f.name || f.stadt || f.code || "";
    var zusatz = [f.stadt && f.name && f.name.indexOf(f.stadt) < 0 ? f.stadt : "", f.land || ""].filter(Boolean).join(", ");
    return name + (zusatz ? " (" + zusatz + ")" : "");
  }
  var QUELLE_NAME = { empfang: "Empfang", flugzeug: "Flugzeug", route: "Route" };

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
    doc.setFontSize(6.2); rechts(doc, k.kategorie_titel.toUpperCase(), x + B - 4, y + 5.2);
    doc.setFont("helvetica", "normal"); doc.setFontSize(5.4);
    rechts(doc, KLASSE_NAME[k.klasse] || "Flugzeug", x + B - 4, y + 9.2);
    // ── Bildfeld (R886): das Bild als Hintergrund der oberen Hälfte, der Text
    // darauf mit hellem Schein. Zuschneiden ist eine Bearbeitung: Gemeinfreie,
    // CC0- und KI-Bilder füllen das Feld; CC BY ebenfalls, mit „zugeschnitten“
    // in der Urheberzeile; CC BY-SA bleibt ungeschnitten (eine Bearbeitung
    // müsste sonst selbst unter CC BY-SA stehen – die ganze Karte).
    // Bildfeld 33 mm bei 88 mm Kartenhöhe; jede zusätzliche Höhe geht ins Bild (R890).
    var fx = rand ? x - rand : x + 0.6, fy = y + 11.6, fw = rand ? B + 2 * rand : B - 1.2, fh = 33 + (H - 88);
    doc.setFillColor(232, 241, 250); doc.rect(fx, fy, fw, fh, "F");
    var credit = k.foto ? k.foto.credit : "";
    if (k._foto) {
      // Immer auf volle Kartenbreite (R887); Überstand oben/unten mittig abgeschnitten.
      var sc = Math.max(fw / k._foto.w, fh / k._foto.h), bw = k._foto.w * sc, bhh = k._foto.h * sc;
      doc.saveGraphicsState(); doc.rect(fx, fy, fw, fh, null); doc.clip(); doc.discardPath();
      doc.addImage(k._foto.daten, k._foto.format, fx + (fw - bw) / 2, fy + (fh - bhh) / 2, bw, bhh, k._foto.alias, "FAST");
      doc.restoreGraphicsState();
      var hinweis = bearbeitung(k);
      if (hinweis) credit += " · " + hinweis;
    } else {
      // Silhouette groß, mittig zwischen Typ oben und Route unten.
      var sw = Math.min(54, B - 9), sh = sw * 17.5 / 54;
      doc.addImage(bild, /^data:image\/jpeg/.test(bild) ? "JPEG" : "PNG", x + (B - sw) / 2, fy + 9.5 + (fh - 33) / 2, sw, sh, "silh-" + (k.klasse || "unbekannt"), "FAST");
    }
    // Typ, Kennzeichen, Rufzeichen, Betreiber – oben, mit Schein
    var typ = k.typ || (k.icao ? "Typ " + k.icao : "Unbekannter Typ");
    if (k.hersteller && typ.toUpperCase().indexOf(k.hersteller.toUpperCase().split(" ")[0]) < 0 && k.hersteller.length < 18) typ = k.hersteller.split(" ")[0] + " " + typ;
    doc.setFont("helvetica", "bold"); doc.setFontSize(8.6);
    schein(doc, kuerzen(doc, typ, B - 8), x + 4, fy + 4.8, 8.6, [20, 23, 30]);
    doc.setFont("helvetica", "normal"); doc.setFontSize(5.6);
    schein(doc, kuerzen(doc, [k.kennzeichen, k.flug, k.betreiber || k.airline].filter(Boolean).join(" · ") || "ohne Kennzeichen", B - 8), x + 4, fy + 8.0, 5.6, [45, 50, 62]);
    // Flugverbindung am unteren Bildrand (R887), darunter die Urheberzeile
    if (k.von && k.nach) {
      doc.setFont("helvetica", "bold"); doc.setFontSize(6.4);
      schein(doc, k.von.code + "  ›  " + k.nach.code, x + 4, fy + fh - 6.2, 6.4, [30, 36, 48]);
      doc.setFont("helvetica", "normal"); doc.setFontSize(4.1);
      schein(doc, kuerzen(doc, flughafenLang(k.von) + "  ›  " + flughafenLang(k.nach), B - 8), x + 4, fy + fh - 3.7, 4.1, [55, 60, 72]);
    } else {
      doc.setFont("helvetica", "normal"); doc.setFontSize(4.8);
      schein(doc, k.route || "Route unbekannt", x + 4, fy + fh - 3.9, 4.8, [90, 95, 108]);
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
    mitte(doc, kuerzen(doc, "Rekord · " + rekordText(k), B - 8), x + B / 2, senkrechtMitte(ry, 4.4, 5.4));
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
      doc.text(z[0] === "steig" ? "Sinken/Steigen" : z[1], x + 4.5, zy + 3.6);
      zelle(doc, z[0], w.min, spMin, zy, k.kategorie === z[2], f);
      zelle(doc, z[0], w.max, spMax, zy, k.kategorie === z[3], f);
    });
    // Metadaten und Quellen
    var my = ty + 2.2 + 4 * zh + 2.0;
    doc.setFont("helvetica", "normal"); doc.setFontSize(3.9); doc.setTextColor(70, 76, 90);
    var zeilenMeta = [
      "Beim Rekord: " + ([m.kurs != null ? "Kurs " + Math.round(m.kurs) + "°" : "", m.squawk ? "Squawk " + m.squawk : "",
        m.lat != null ? koordinate(m.lat, "N", "S") + " " + koordinate(m.lon, "O", "W") : ""].filter(Boolean).join(" · ") || "–"),
      [k.halter ? "Halter " + k.halter + (k.land ? " (" + k.land + ")" : "") : "", k.airline && k.airline !== k.betreiber ? "Airline " + k.airline : ""].filter(Boolean).join(" · "),
      k.erste ? "Erfasst " + k.erste + " – " + k.letzte + (k.meldungen ? " · " + tausend(k.meldungen) + " Meldungen" : "") : "",
      "Quellen: " + (Object.keys(k.quellen || {}).sort().map(function (q) { return (QUELLE_NAME[q] || q) + " " + k.quellen[q]; }).join(" · ") || "–")
    ].filter(Boolean);
    zeilenMeta.forEach(function (zl, i) { doc.text(kuerzen(doc, zl, B - 8), x + 4, my + i * 1.85); });
    // Fuß (R889): aufnehmender Knoten mit seinen Fundus-Koordinaten; 3,4 mm vom
    // Rand – innerhalb des Sicherheitsabstands der Druckerei.
    doc.setFont("helvetica", "normal"); doc.setFontSize(3.9); doc.setTextColor(130, 135, 148);
    doc.text("Hex " + k.hex + (k.icao ? " · " + k.icao : ""), x + 4, y + H - 3.4);
    var auf = k.aufnahme || kontext.aufnahme || {};
    var ort = auf.lat != null && (auf.lat !== 0 || auf.lon !== 0) ? koordinate(auf.lat, "N", "S") + " · " + koordinate(auf.lon, "O", "W") : "Standort unbekannt";
    rechts(doc, kuerzen(doc, (auf.node ? auf.node + " · " : "") + ort, B - 26), x + B - 4, y + H - 3.4);
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
  var VERLAUF = [[0, [0, 200, 83]], [0.45, [23, 168, 98]], [1, [6, 53, 31]]];
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
    // Knotennetz: fester Startwert – ALLE Karten haben exakt dieselbe Rückseite.
    var zufall = (function (a) { return function () { a = (a * 1664525 + 1013904223) >>> 0; return a / 4294967296; }; })(64);
    var knoten = [];
    for (var n = 0; n < 46; n++) knoten.push([x + 2 + zufall() * (B - 4), y + 2 + zufall() * (H - 4), (2 + zufall() * 4) / 10]);
    doc.setLineWidth(0.2);
    for (var a1 = 0; a1 < knoten.length; a1++) for (var b1 = a1 + 1; b1 < knoten.length; b1++) {
      var dx = knoten[a1][0] - knoten[b1][0], dy = knoten[a1][1] - knoten[b1][1], d = Math.sqrt(dx * dx + dy * dy);
      if (d < 16.5) {
        var lf = mischen(verlaufFarbe(tVon((knoten[a1][0] + knoten[b1][0]) / 2, (knoten[a1][1] + knoten[b1][1]) / 2)), 0.28 * (1 - d / 16.5));
        doc.setDrawColor(lf[0], lf[1], lf[2]); doc.line(knoten[a1][0], knoten[a1][1], knoten[b1][0], knoten[b1][1]);
      }
    }
    knoten.forEach(function (p) {
      var kf = mischen(verlaufFarbe(tVon(p[0], p[1])), 0.55);
      doc.setFillColor(kf[0], kf[1], kf[2]); doc.circle(p[0], p[1], p[2], "F");
    });
    doc.restoreGraphicsState();
    // Rahmen
    var rf = mischen(verlaufFarbe(0.5), 0.45);
    doc.setDrawColor(rf[0], rf[1], rf[2]); doc.setLineWidth(0.4);
    doc.roundedRect(x + 4, y + 4, B - 8, H - 8, 2, 2, "S");
    // Schrift und Logo (Maße wie bisher: 1 mm = 10 Einheiten der Rasterfassung)
    var dyMm = (H - 88) / 2;
    function zeile(s, yy, mm, abstand, deckkraft, fett) {
      var pt = mm / 0.3528, f = mischen(verlaufFarbe(tVon(x + B / 2, yy)), deckkraft);
      doc.setFont("helvetica", fett ? "bold" : "normal"); doc.setFontSize(pt); doc.setTextColor(f[0], f[1], f[2]);
      var breite = doc.getTextWidth(s) + abstand * (sicher(s).length - 1);
      doc.text(s, x + (B - breite) / 2, yy, abstand ? { charSpace: abstand } : undefined);
    }
    zeile("FUNDUS", y + 13.2 + dyMm, 5.6, 1.4, 1, true);
    if (LOGO_D) {
      doc.setFillColor(255, 255, 255);
      doc.path(svgPfad(LOGO_D, 33 / 200, x + (B - 33) / 2, y + 17.5 + dyMm));
      doc.fillEvenOdd();
    }
    zeile(kontext.spiel || "FLIGHT QUAD 64", y + 59.3 + dyMm, 5.4, 0, 1, true);
    zeile("Best of " + (kontext.code || ""), y + 65.3 + dyMm, 3.8, 0, 0.88, true);
    zeile("TKM x HELIX", y + H - 9.8, 3.0, 0.6, 0.75, true);
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
    doc.setFontSize(6.4); mitte(doc, "Best of " + (kontext.code || "") + " · " + (kontext.titel || ""), x + B / 2, y + 14.6);
    doc.setFont("helvetica", "normal"); doc.setFontSize(4.6); mitte(doc, "FUNDUS · TKM x HELIX", x + B / 2, y + 19.0);
    // Die acht Quartette
    var qy = y + 27;
    doc.setFont("helvetica", "bold"); doc.setFontSize(6.2); doc.setTextColor(30, 30, 30);
    doc.text("Die Quartette", x + 4, qy);
    KATEGORIEN.forEach(function (kat, i) {
      var zy = qy + 2.2 + i * 4.0, f = KAT_FARBE[kat[0]] || [80, 80, 80];
      doc.setFillColor(f[0], f[1], f[2]); doc.roundedRect(x + 4, zy + 0.4, 9, 3.0, 0.8, 0.8, "F");
      doc.setTextColor(255, 255, 255); doc.setFont("helvetica", "bold"); doc.setFontSize(5);
      mitte(doc, kontext.anzahl === 32 ? String(i + 1) : (2 * i + 1) + "·" + (2 * i + 2), x + 8.5, zy + 2.6);
      doc.setTextColor(40, 40, 40); doc.setFontSize(5.6); doc.setFont("helvetica", "normal");
      doc.text(kat[1], x + 15, zy + 2.7);
    });
    // Spielregeln
    var ry = qy + 2.2 + 8 * 4.0 + 3.4;
    doc.setFont("helvetica", "bold"); doc.setFontSize(6.2); doc.setTextColor(30, 30, 30);
    doc.text("Spielregeln", x + 4, ry);
    doc.setFont("helvetica", "normal"); doc.setFontSize(4.5); doc.setTextColor(50, 50, 50);
    var regeln = "Karten mischen und gleichmäßig verteilen; jeder hält seinen Stapel verdeckt. Wer am Zug ist, nennt einen Wert " +
      "seiner obersten Karte und die Spalte, etwa „Höhe, Maximum“. Bei Maximum gewinnt der größte Wert, bei Minimum der kleinste, " +
      "bei Sinken/Steigen jeweils der stärkere. Der Gewinner bekommt alle aufgedeckten Karten und ist wieder am Zug. " +
      "Wer alle Karten hat, gewinnt. Quartett-Variante: vier Karten einer Nummer (A–D) sammeln.";
    doc.splitTextToSize(sicher(regeln), B - 8).forEach(function (zl, i) { doc.text(zl, x + 4, ry + 2.6 + i * 1.95); });
    // Herkunft der Daten
    var auf = kontext.aufnahme || {};
    doc.setFontSize(3.9); doc.setTextColor(120, 120, 120);
    doc.text(kuerzen(doc, (kontext.flugzeuge ? tausend(kontext.flugzeuge) + " Flugzeuge aufgezeichnet · " : "") + "ADS-B-Empfang " + (auf.node || kontext.node || ""), B - 8), x + 4, y + H - 5.4);
    doc.text(kuerzen(doc, auf.lat != null ? koordinate(auf.lat, "N", "S") + " · " + koordinate(auf.lon, "O", "W") + " · Typen adsbdb/hexdb · Routen adsb.lol" : "Typen adsbdb/hexdb · Routen adsb.lol", B - 8), x + 4, y + H - 3.4);
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
  function deckend(daten, grund) {
    return new Promise(function (ok) {
      var img = new Image();
      img.onload = function () {
        var cv = document.createElement("canvas"); cv.width = img.naturalWidth; cv.height = img.naturalHeight;
        var g = cv.getContext("2d"); g.fillStyle = grund || "#ffffff"; g.fillRect(0, 0, cv.width, cv.height); g.drawImage(img, 0, 0);
        ok({ daten: cv.toDataURL("image/jpeg", 0.92), w: cv.width, h: cv.height });
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
    B = masse[0]; H = masse[1];
    var karten = daten.karten || [];
    var anzahl = daten.anzahl === 32 || (!daten.anzahl && karten.length <= 32) ? 32 : 64;
    var kontext = { titel: daten.titel, node: daten.node, code: daten.code, aufnahme: daten.aufnahme,
      anzahl: anzahl, spiel: "FLIGHT QUAD " + anzahl, flugzeuge: daten.flugzeuge };
    var klassen = {};
    karten.forEach(function (k) { klassen[k.klasse] = true; });
    var istA4 = format === "a4" || format === "a4_druck";
    var bilder = {}, druck = format !== "a4"; // a4_druck: wie die Druckformate (R892)
    var vorbereiten = Object.keys(klassen).map(function (kl) {
      return silhouetteBild(kl, "#1f2a3a", false).then(function (b) {
        // Druckformate: Silhouette auf die Bildfeld-Farbe, ohne Alphakanal (R891)
        if (!druck) { bilder[kl] = b; return; }
        return deckend(b, "#e8f1fa").then(function (d) { bilder[kl] = d ? d.daten : b; });
      });
    });
    karten.forEach(function (k) {
      if (k.foto && k.foto.url) vorbereiten.push(fotoLaden(k.foto.url).then(function (f) {
        if (!f || !druck || f.format === "JPEG") { k._foto = f; return; }
        return deckend(f.daten).then(function (d) {
          k._foto = d ? { daten: d.daten, format: "JPEG", w: d.w, h: d.h, alias: f.alias + "-j" } : f;
        });
      }));
    });
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
        doc = sicherMachen(new jsPDF({ unit: "mm", format: "a4" }), { neutral: druck });
        if (druck) doc._deckend = true; // PDF/X-1a: keine Transparenz (R892)
        // y0 = 22: unterster Streifen endet ≥ 5 mm vor dem Blattrand (R888).
        var proSeite = 4, abstand = 6, x0 = (210 - 2 * H) / 2, y0 = 22;
        var seiten = Math.ceil(alle.length / proSeite);
        alle.forEach(function (k, i) {
          var reihe = i % proSeite;
          if (i > 0 && reihe === 0) doc.addPage("a4");
          if (reihe === 0) {
            doc.setFont("helvetica", "normal"); doc.setFontSize(7.5); doc.setTextColor(110);
            doc.text(kontext.spiel + " · " + daten.code + " · " + daten.titel + " · Seite " + (Math.floor(i / proSeite) + 1) + " von " + seiten, 105, 12, { align: "center" });
            doc.text("Karten in Originalgröße " + B + " × " + H + " mm. An den Ecken ausschneiden, an der gestrichelten Oberkante nach hinten falten, zusammenkleben.", 105, 17, { align: "center" });
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
        doc = sicherMachen(new jsPDF({ unit: "mm", format: [PB, PH] }), { neutral: true });
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
