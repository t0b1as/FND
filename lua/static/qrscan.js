// qrscan.js – QR-Code-Leser in reinem JavaScript, ohne fremde Server (R806).
//
// Wozu: Der Wallet-Scanner nutzte nur die Browser-Schnittstelle
// BarcodeDetector. Die gibt es NICHT in Safari, NICHT in Brave (dort aus
// Datenschutzgründen abgeschaltet) und auch nicht im gewöhnlichen Chrome unter
// Windows und Linux – nur unter Android, macOS und ChromeOS. Dieser Leser
// springt überall dort ein.
//
// Aufbau nach ZXing, dem Standard-Leser für QR-Codes:
//   1. Schwellwert je Bildblock (robust gegen Schatten und Verläufe)
//   2. Suchmuster finden (die drei großen Quadrate, Verhältnis 1:1:3:1:1)
//   3. Ausrichtungsmuster suchen und das Raster perspektivisch abtasten
//   4. Formatbits lesen (Fehlerkorrekturstufe und Maske)
//   5. Codewörter auslesen, Reed-Solomon-Korrektur, Daten entschlüsseln
//
// Unterstützt: Versionen 1–10 (bis 271 Byte), alle vier Fehlerkorrekturstufen,
// Zahlen-, Alphanumerik- und Byte-Modus (UTF-8). Für Wallet-Adressen und
// Zahlungsadressen reicht das mit großem Abstand.
//
//   window.fundusQRDecode(imageData) → Text oder null
(function () {
  "use strict";

  // ── GF(256), Polynom 0x11d ────────────────────────────────────────────────
  var EXP = new Array(512), LOG = new Array(256);
  (function () {
    var x = 1;
    for (var i = 0; i < 255; i++) {
      EXP[i] = x; LOG[x] = i;
      x <<= 1; if (x & 0x100) x ^= 0x11d;
    }
    for (var j = 255; j < 512; j++) EXP[j] = EXP[j - 255];
  })();
  function gmul(a, b) { return (a === 0 || b === 0) ? 0 : EXP[LOG[a] + LOG[b]]; }
  function gdiv(a, b) { if (a === 0) return 0; return EXP[(LOG[a] + 255 - LOG[b]) % 255]; }
  function ginv(a) { return EXP[255 - LOG[a]]; }

  // ── Reed-Solomon-Dekodierung (Berlekamp-Massey, Chien, Forney) ───────────
  // cw: Codewörter, höchster Grad zuerst. ec: Anzahl Prüfwörter. Korrigiert
  // an Ort und Stelle; gibt false zurück, wenn nicht korrigierbar.
  function rsDecode(cw, ec) {
    var n = cw.length, synd = new Array(ec), fehlerfrei = true;
    for (var i = 0; i < ec; i++) {
      var s = 0, a = EXP[i];
      for (var k = 0; k < n; k++) s = gmul(s, a) ^ cw[k];
      synd[i] = s; if (s !== 0) fehlerfrei = false;
    }
    if (fehlerfrei) return true;
    // Berlekamp-Massey → Fehlerortungspolynom C (niedrigster Grad zuerst)
    var C = [1], B = [1], L = 0, m = 1, b = 1;
    for (var r = 0; r < ec; r++) {
      var d = synd[r];
      for (var q = 1; q <= L; q++) d ^= gmul(C[q] || 0, synd[r - q]);
      if (d === 0) { m++; continue; }
      var T = C.slice(), coef = gdiv(d, b);
      for (var t = 0; t < B.length; t++) {
        var idx = t + m;
        while (C.length <= idx) C.push(0);
        C[idx] ^= gmul(coef, B[t]);
      }
      if (2 * L <= r) { L = r + 1 - L; B = T; b = d; m = 1; } else m++;
    }
    if (L === 0 || L * 2 > ec) return false;
    // Chien-Suche: Nullstellen von C bei X^-1, Stelle j entspricht Grad j
    var orte = [];
    for (var j = 0; j < n; j++) {
      var xinv = EXP[(255 - j) % 255], v = 0, p = 1;
      for (var c = 0; c < C.length; c++) { v ^= gmul(C[c], p); p = gmul(p, xinv); }
      if (v === 0) orte.push(j);
    }
    if (orte.length !== L) return false;
    // Fehlerwertpolynom Omega = (S * C) mod x^ec
    var omega = new Array(ec).fill(0);
    for (var u = 0; u < ec; u++) {
      for (var w = 0; w <= u && w < C.length; w++) omega[u] ^= gmul(C[w], synd[u - w]);
    }
    // Forney (Startwurzel alpha^0): e = X * Omega(X^-1) / C'(X^-1)
    for (var e = 0; e < orte.length; e++) {
      var pos = orte[e], X = EXP[pos], Xinv = ginv(X);
      var num = 0, pw = 1;
      for (var o = 0; o < omega.length; o++) { num ^= gmul(omega[o], pw); pw = gmul(pw, Xinv); }
      var den = 0;
      for (var z = 1; z < C.length; z += 2) {           // Ableitung: nur ungerade Glieder
        den ^= gmul(C[z], EXP[(LOG[Xinv] * (z - 1)) % 255]);
      }
      if (den === 0) return false;
      var wert = gmul(X, gdiv(num, den));
      var stelle = n - 1 - pos;
      if (stelle < 0) return false;
      cw[stelle] ^= wert;
    }
    // Nachprüfen: alle Syndrome müssen jetzt null sein
    for (var i2 = 0; i2 < ec; i2++) {
      var s2 = 0, a2 = EXP[i2];
      for (var k2 = 0; k2 < n; k2++) s2 = gmul(s2, a2) ^ cw[k2];
      if (s2 !== 0) return false;
    }
    return true;
  }

  // ── Tabellen ──────────────────────────────────────────────────────────────
  // [Prüfwörter je Block, [[Blöcke, Datenwörter], ...]] für L, M, Q, H.
  // Gegen die aus der Geometrie berechnete Codewortzahl geprüft (R806).
  var EC = {
    1: { L: [7, [[1, 19]]], M: [10, [[1, 16]]], Q: [13, [[1, 13]]], H: [17, [[1, 9]]] },
    2: { L: [10, [[1, 34]]], M: [16, [[1, 28]]], Q: [22, [[1, 22]]], H: [28, [[1, 16]]] },
    3: { L: [15, [[1, 55]]], M: [26, [[1, 44]]], Q: [18, [[2, 17]]], H: [22, [[2, 13]]] },
    4: { L: [20, [[1, 80]]], M: [18, [[2, 32]]], Q: [26, [[2, 24]]], H: [16, [[4, 9]]] },
    5: { L: [26, [[1, 108]]], M: [24, [[2, 43]]], Q: [18, [[2, 15], [2, 16]]], H: [22, [[2, 11], [2, 12]]] },
    6: { L: [18, [[2, 68]]], M: [16, [[4, 27]]], Q: [24, [[4, 19]]], H: [28, [[4, 15]]] },
    7: { L: [20, [[2, 78]]], M: [18, [[4, 31]]], Q: [18, [[2, 14], [4, 15]]], H: [26, [[4, 13], [1, 14]]] },
    8: { L: [24, [[2, 97]]], M: [22, [[2, 38], [2, 39]]], Q: [22, [[4, 18], [2, 19]]], H: [26, [[4, 14], [2, 15]]] },
    9: { L: [30, [[2, 116]]], M: [22, [[3, 36], [2, 37]]], Q: [20, [[4, 16], [4, 17]]], H: [24, [[4, 12], [4, 13]]] },
    10: { L: [18, [[2, 68], [2, 69]]], M: [26, [[4, 43], [1, 44]]], Q: [24, [[6, 19], [2, 20]]], H: [28, [[6, 15], [2, 16]]] }
  };
  var ALIGN = { 1: [], 2: [6, 18], 3: [6, 22], 4: [6, 26], 5: [6, 30], 6: [6, 34],
                7: [6, 22, 38], 8: [6, 24, 42], 9: [6, 26, 46], 10: [6, 28, 50] };
  var VERSION_BITS = { 7: 0x07C94, 8: 0x085BC, 9: 0x09A99, 10: 0x0A4D3 };

  // Formatcodes: 5 Datenbits (Stufe, Maske) + BCH(15,5), maskiert mit 0x5412.
  var FORMAT = [];
  (function () {
    for (var d = 0; d < 32; d++) {
      var v = d << 10;
      for (var i = 14; i >= 10; i--) if (v & (1 << i)) v ^= (0x537 << (i - 10));
      FORMAT.push({ code: ((d << 10) | v) ^ 0x5412, daten: d });
    }
  })();
  var STUFE = { 0: "M", 1: "L", 2: "H", 3: "Q" };   // ZXing: ECLevel.forBits

  function bitsAnders(a, b) { var x = a ^ b, n = 0; while (x) { n += x & 1; x >>>= 1; } return n; }

  // ── 1. Schwellwert je 8×8-Block (wie jsQR) ───────────────────────────────
  function binarisieren(img) {
    var W = img.width, H = img.height, px = img.data;
    var lum = new Uint8ClampedArray(W * H);
    for (var i = 0, p = 0; i < W * H; i++, p += 4) {
      lum[i] = (px[p] * 0.299 + px[p + 1] * 0.587 + px[p + 2] * 0.114) | 0;
    }
    var BS = 8, bw = Math.ceil(W / BS), bh = Math.ceil(H / BS);
    var bp = new Float32Array(bw * bh);
    for (var by = 0; by < bh; by++) {
      for (var bx = 0; bx < bw; bx++) {
        var sum = 0, mn = 255, mx = 0, cnt = 0;
        for (var y = by * BS; y < Math.min(H, by * BS + BS); y++) {
          for (var x = bx * BS; x < Math.min(W, bx * BS + BS); x++) {
            var l = lum[y * W + x]; sum += l; cnt++;
            if (l < mn) mn = l; if (l > mx) mx = l;
          }
        }
        var avg = sum / cnt;
        if (mx - mn <= 24) {
          // Kontrastarmer Block: nicht aus Rauschen Muster erfinden.
          avg = mn / 2;
          if (by > 0 && bx > 0) {
            var nb = (bp[(by - 1) * bw + bx] + 2 * bp[by * bw + bx - 1] + bp[(by - 1) * bw + bx - 1]) / 4;
            if (mn < nb) avg = nb;
          }
        }
        bp[by * bw + bx] = avg;
      }
    }
    var bin = new Uint8Array(W * H);
    for (var vy = 0; vy < bh; vy++) {
      for (var vx = 0; vx < bw; vx++) {
        var s = 0;
        for (var dy = -2; dy <= 2; dy++) {
          for (var dx = -2; dx <= 2; dx++) {
            var yy = Math.min(bh - 1, Math.max(0, vy + dy)), xx = Math.min(bw - 1, Math.max(0, vx + dx));
            s += bp[yy * bw + xx];
          }
        }
        var thr = s / 25;
        for (var y2 = vy * BS; y2 < Math.min(H, vy * BS + BS); y2++) {
          for (var x2 = vx * BS; x2 < Math.min(W, vx * BS + BS); x2++) {
            bin[y2 * W + x2] = lum[y2 * W + x2] <= thr ? 1 : 0;
          }
        }
      }
    }
    return { w: W, h: H, d: bin };
  }
  function dunkel(B, x, y) {
    x = Math.round(x); y = Math.round(y);
    if (x < 0 || y < 0 || x >= B.w || y >= B.h) return 0;
    return B.d[y * B.w + x];
  }

  // ── 2. Suchmuster ─────────────────────────────────────────────────────────
  function verhaeltnisOk(c) {
    var total = c[0] + c[1] + c[2] + c[3] + c[4];
    if (total < 7) return false;
    var m = total / 7, v = m / 1.8;
    return Math.abs(m - c[0]) < v && Math.abs(m - c[1]) < v && Math.abs(3 * m - c[2]) < 3 * v &&
           Math.abs(m - c[3]) < v && Math.abs(m - c[4]) < v;
  }
  // Senkrecht durch (cx, cy) prüfen; gibt neue Mitte y und Länge zurück.
  function kreuzSenkrecht(B, cx, cy, maxC, ref) {
    var c = [0, 0, 0, 0, 0], y = cy;
    cx = Math.round(cx);
    while (y >= 0 && dunkel(B, cx, y)) { c[2]++; y--; }
    if (y < 0) return null;
    while (y >= 0 && !dunkel(B, cx, y) && c[1] <= maxC) { c[1]++; y--; }
    if (y < 0 || c[1] > maxC) return null;
    while (y >= 0 && dunkel(B, cx, y) && c[0] <= maxC) { c[0]++; y--; }
    if (c[0] > maxC) return null;
    y = cy + 1;
    while (y < B.h && dunkel(B, cx, y)) { c[2]++; y++; }
    if (y >= B.h) return null;
    while (y < B.h && !dunkel(B, cx, y) && c[3] < maxC) { c[3]++; y++; }
    if (y >= B.h || c[3] >= maxC) return null;
    while (y < B.h && dunkel(B, cx, y) && c[4] < maxC) { c[4]++; y++; }
    if (c[4] >= maxC) return null;
    var total = c[0] + c[1] + c[2] + c[3] + c[4];
    if (5 * Math.abs(total - ref) >= 2 * ref) return null;
    if (!verhaeltnisOk(c)) return null;
    return { mitte: y - c[4] - c[3] - c[2] / 2, laenge: total };
  }
  function kreuzWaagrecht(B, cx, cy, maxC, ref) {
    var c = [0, 0, 0, 0, 0], x = Math.round(cx);
    cy = Math.round(cy);
    while (x >= 0 && dunkel(B, x, cy)) { c[2]++; x--; }
    if (x < 0) return null;
    while (x >= 0 && !dunkel(B, x, cy) && c[1] <= maxC) { c[1]++; x--; }
    if (x < 0 || c[1] > maxC) return null;
    while (x >= 0 && dunkel(B, x, cy) && c[0] <= maxC) { c[0]++; x--; }
    if (c[0] > maxC) return null;
    x = Math.round(cx) + 1;
    while (x < B.w && dunkel(B, x, cy)) { c[2]++; x++; }
    if (x >= B.w) return null;
    while (x < B.w && !dunkel(B, x, cy) && c[3] < maxC) { c[3]++; x++; }
    if (x >= B.w || c[3] >= maxC) return null;
    while (x < B.w && dunkel(B, x, cy) && c[4] < maxC) { c[4]++; x++; }
    if (c[4] >= maxC) return null;
    var total = c[0] + c[1] + c[2] + c[3] + c[4];
    if (5 * Math.abs(total - ref) >= ref) return null;
    if (!verhaeltnisOk(c)) return null;
    return { mitte: x - c[4] - c[3] - c[2] / 2, laenge: total };
  }
  function sucheSuchmuster(B, schrittVorgabe) {
    var kand = [];
    function merke(x, y, modul) {
      for (var i = 0; i < kand.length; i++) {
        var k = kand[i];
        if (Math.abs(y - k.y) <= modul && Math.abs(x - k.x) <= modul &&
            (Math.abs(modul - k.modul) <= 1 || Math.abs(modul - k.modul) <= k.modul)) {
          var n = k.anzahl;
          k.x = (k.x * n + x) / (n + 1); k.y = (k.y * n + y) / (n + 1);
          k.modul = (k.modul * n + modul) / (n + 1); k.anzahl++;
          return;
        }
      }
      kand.push({ x: x, y: y, modul: modul, anzahl: 1 });
    }
    var schritt = schrittVorgabe || Math.max(1, Math.floor((3 * B.h) / (4 * 97)));
    if (schritt > 3) schritt = 3;
    for (var y = schritt - 1; y < B.h; y += schritt) {
      var c = [0, 0, 0, 0, 0], st = 0;
      for (var x = 0; x < B.w; x++) {
        var d = B.d[y * B.w + x];
        if (d) {
          if ((st & 1) === 1) st++;
          c[st]++;
        } else {
          if ((st & 1) === 0) {
            if (st === 4) {
              if (verhaeltnisOk(c)) {
                var total = c[0] + c[1] + c[2] + c[3] + c[4];
                var cx = x - c[4] - c[3] - c[2] / 2;
                var ks = kreuzSenkrecht(B, cx, y, c[2], total);
                if (ks) {
                  var kw = kreuzWaagrecht(B, cx, ks.mitte, c[2], total);
                  if (kw) merke(kw.mitte, ks.mitte, (kw.laenge + ks.laenge) / 14);
                }
                c = [c[2], c[3], c[4], 1, 0]; st = 3;
              } else {
                c = [c[2], c[3], c[4], 1, 0]; st = 3;
              }
            } else { st++; c[st]++; }
          } else c[st]++;
        }
      }
    }
    return kand;
  }
  function abstand(a, b) { var dx = a.x - b.x, dy = a.y - b.y; return Math.sqrt(dx * dx + dy * dy); }
  // Breite eines Suchmusters ENTLANG einer Richtung messen (R806).
  //
  // Waagrecht gemessen ist ein gedrehter Code zu breit: Der Schnitt läuft
  // schräg durch das Quadrat. Bei 45° liegt man um den Faktor 1,4 daneben,
  // die Kantenlänge wird falsch geschätzt, und nichts passt mehr. Entlang
  // der Linie zum nächsten Suchmuster liegt die Messung dagegen immer
  // parallel zur Codekante. Ein Suchmuster ist 7 Module breit.
  function breiteEntlang(B, p, dx, dy) {
    var gesamt = 0;
    for (var sgn = -1; sgn <= 1; sgn += 2) {
      var x = p.x, y = p.y, wechsel = 0, schritte = 0, vorher = 1;
      while (schritte < 400) {
        x += sgn * dx; y += sgn * dy; schritte++;
        var d = dunkel(B, x, y);
        if (d !== vorher) { wechsel++; vorher = d; if (wechsel === 3) break; }
        if (x < 0 || y < 0 || x >= B.w || y >= B.h) return 0;
      }
      if (wechsel < 3) return 0;
      gesamt += schritte - 0.5;
    }
    return gesamt / 7;
  }
  function modulGroesse(B, o) {
    function richt(a, b) { var l = abstand(a, b); return [(b.x - a.x) / l, (b.y - a.y) / l]; }
    var r1 = richt(o.ol, o.or), r2 = richt(o.ol, o.ul), werte = [];
    [[o.ol, r1], [o.ol, r2], [o.or, r1], [o.ul, r2]].forEach(function (q) {
      var m = breiteEntlang(B, q[0], q[1][0], q[1][1]);
      if (m > 0) werte.push(m);
    });
    if (!werte.length) return o.modul;
    werte.sort(function (a, b) { return a - b; });
    return werte[Math.floor(werte.length / 2)];   // Median: robust gegen Ausreißer
  }
  // Ordnet drei Punkte: unten links, oben links, oben rechts (wie ZXing).
  function ordnen(p) {
    var d01 = abstand(p[0], p[1]), d12 = abstand(p[1], p[2]), d02 = abstand(p[0], p[2]);
    var A, Bp, C;
    if (d12 >= d01 && d12 >= d02) { Bp = p[0]; A = p[1]; C = p[2]; }
    else if (d02 >= d12 && d02 >= d01) { Bp = p[1]; A = p[0]; C = p[2]; }
    else { Bp = p[2]; A = p[0]; C = p[1]; }
    if ((C.x - Bp.x) * (A.y - Bp.y) - (C.y - Bp.y) * (A.x - Bp.x) < 0) { var t = A; A = C; C = t; }
    return { ul: A, ol: Bp, or: C };
  }
  // Wählt aus allen Kandidaten die drei, die am besten ein QR-Dreieck bilden.
  function waehleDrei(kand) {
    kand = kand.filter(function (k) { return k.anzahl >= 2; }).concat(
      kand.filter(function (k) { return k.anzahl < 2; }));
    var n = Math.min(kand.length, 12), best = null, bestWert = Infinity;
    for (var i = 0; i < n; i++) for (var j = i + 1; j < n; j++) for (var k = j + 1; k < n; k++) {
      var a = kand[i], b = kand[j], c = kand[k];
      var mm = (a.modul + b.modul + c.modul) / 3;
      var mAbw = (Math.abs(a.modul - mm) + Math.abs(b.modul - mm) + Math.abs(c.modul - mm)) / mm;
      if (mAbw > 0.6) continue;
      var o = ordnen([a, b, c]);
      var s1 = abstand(o.ol, o.or), s2 = abstand(o.ol, o.ul), h = abstand(o.or, o.ul);
      if (s1 < 10 * mm || s2 < 10 * mm) continue;
      var schenkel = Math.abs(s1 - s2) / Math.max(s1, s2);
      var rechtw = Math.abs(h - Math.sqrt(s1 * s1 + s2 * s2)) / h;
      var wert = schenkel + rechtw * 2 + mAbw * 0.5 - Math.min(a.anzahl + b.anzahl + c.anzahl, 15) * 0.01;
      if (wert < bestWert) { bestWert = wert; best = o; best.modul = mm; }
    }
    return bestWert < 0.6 ? best : null;
  }

  // ── 3. Ausrichtungsmuster und Abtastung ──────────────────────────────────
  function sucheAusrichtung(B, ex, ey, modul) {
    var r = Math.max(4, Math.round(modul * 6)), best = null, bestD = Infinity;
    var x0 = Math.max(0, Math.round(ex - r)), x1 = Math.min(B.w - 1, Math.round(ex + r));
    var y0 = Math.max(0, Math.round(ey - r)), y1 = Math.min(B.h - 1, Math.round(ey + r));
    var tol = modul * 0.7;
    for (var y = y0; y <= y1; y++) {
      // Läufe in der Zeile: hell–dunkel–hell, Mitte ≈ 1 Modul
      var x = x0;
      while (x <= x1) {
        var dk = dunkel(B, x, y), s = x;
        while (x <= x1 && dunkel(B, x, y) === dk) x++;
        if (dk) {
          var lauf = x - s;
          if (Math.abs(lauf - modul) <= tol) {
            // links und rechts je ein helles Modul
            var lw = 0, xl = s - 1; while (xl >= 0 && !dunkel(B, xl, y) && lw <= 2 * modul) { lw++; xl--; }
            var rw = 0, xr = x; while (xr < B.w && !dunkel(B, xr, y) && rw <= 2 * modul) { rw++; xr++; }
            if (Math.abs(lw - modul) <= tol && Math.abs(rw - modul) <= tol) {
              var cx = s + lauf / 2;
              // senkrecht prüfen
              var cxr = Math.round(cx), oben = 0, yo = y;
              while (yo >= 0 && dunkel(B, cxr, yo)) { oben++; yo--; }
              var ow = 0; while (yo >= 0 && !dunkel(B, cxr, yo) && ow <= 2 * modul) { ow++; yo--; }
              var unten = 0, yu = y + 1;
              while (yu < B.h && dunkel(B, cxr, yu)) { unten++; yu++; }
              var uw = 0; while (yu < B.h && !dunkel(B, cxr, yu) && uw <= 2 * modul) { uw++; yu++; }
              var hoehe = oben + unten;
              if (Math.abs(hoehe - modul) <= tol && Math.abs(ow - modul) <= tol && Math.abs(uw - modul) <= tol) {
                var cy = y - oben + 1 + hoehe / 2 - 0.5;
                var d = Math.abs(cx - ex) + Math.abs(cy - ey);
                if (d < bestD) { bestD = d; best = { x: cx, y: cy }; }
              }
            }
          }
        }
      }
    }
    return best;
  }
  // Homographie aus vier Punktpaaren (Modulraum → Bild).
  function homographie(src, dst) {
    var A = [], b = [];
    for (var i = 0; i < 4; i++) {
      var x = src[i][0], y = src[i][1], u = dst[i][0], v = dst[i][1];
      A.push([x, y, 1, 0, 0, 0, -u * x, -u * y]); b.push(u);
      A.push([0, 0, 0, x, y, 1, -v * x, -v * y]); b.push(v);
    }
    for (var c = 0; c < 8; c++) {
      var piv = c;
      for (var r = c + 1; r < 8; r++) if (Math.abs(A[r][c]) > Math.abs(A[piv][c])) piv = r;
      if (Math.abs(A[piv][c]) < 1e-12) return null;
      var t = A[c]; A[c] = A[piv]; A[piv] = t; var tb = b[c]; b[c] = b[piv]; b[piv] = tb;
      for (var r2 = 0; r2 < 8; r2++) {
        if (r2 === c) continue;
        var f = A[r2][c] / A[c][c];
        for (var k = c; k < 8; k++) A[r2][k] -= f * A[c][k];
        b[r2] -= f * b[c];
      }
    }
    var h = []; for (var q = 0; q < 8; q++) h.push(b[q] / A[q][q]);
    return function (x, y) {
      var w = h[6] * x + h[7] * y + 1;
      return [(h[0] * x + h[1] * y + h[2]) / w, (h[3] * x + h[4] * y + h[5]) / w];
    };
  }
  function abtasten(B, H, dim) {
    var m = [];
    for (var y = 0; y < dim; y++) {
      var zeile = new Uint8Array(dim);
      for (var x = 0; x < dim; x++) {
        var p = H(x + 0.5, y + 0.5);
        zeile[x] = dunkel(B, p[0], p[1]);
      }
      m.push(zeile);
    }
    return m;
  }

  // ── 4./5. Formatbits, Codewörter, Daten ──────────────────────────────────
  function leseFormat(m, dim) {
    function bit(x, y) { return m[y][x]; }
    var f1 = 0, f2 = 0, i;
    for (i = 0; i < 6; i++) f1 = (f1 << 1) | bit(i, 8);
    f1 = (f1 << 1) | bit(7, 8); f1 = (f1 << 1) | bit(8, 8); f1 = (f1 << 1) | bit(8, 7);
    for (i = 5; i >= 0; i--) f1 = (f1 << 1) | bit(8, i);
    for (i = dim - 1; i >= dim - 7; i--) f2 = (f2 << 1) | bit(8, i);
    for (i = dim - 8; i < dim; i++) f2 = (f2 << 1) | bit(i, 8);
    var best = null, bestD = 99;
    for (var k = 0; k < FORMAT.length; k++) {
      var d = Math.min(bitsAnders(f1, FORMAT[k].code), bitsAnders(f2, FORMAT[k].code));
      if (d < bestD) { bestD = d; best = FORMAT[k].daten; }
    }
    if (bestD > 3) return null;
    return { stufe: STUFE[(best >> 3) & 3], maske: best & 7 };
  }
  function maskiert(maske, i, j) {   // i = Zeile, j = Spalte
    switch (maske) {
      case 0: return (i + j) % 2 === 0;
      case 1: return i % 2 === 0;
      case 2: return j % 3 === 0;
      case 3: return (i + j) % 3 === 0;
      case 4: return (Math.floor(i / 2) + Math.floor(j / 3)) % 2 === 0;
      case 5: return ((i * j) % 2) + ((i * j) % 3) === 0;
      case 6: return (((i * j) % 2) + ((i * j) % 3)) % 2 === 0;
      default: return (((i + j) % 2) + ((i * j) % 3)) % 2 === 0;
    }
  }
  function funktionsMuster(ver) {
    var dim = 17 + 4 * ver, f = [];
    for (var y = 0; y < dim; y++) f.push(new Uint8Array(dim));
    function reg(x, y, w, h) { for (var yy = y; yy < y + h; yy++) for (var xx = x; xx < x + w; xx++) f[yy][xx] = 1; }
    reg(0, 0, 9, 9); reg(dim - 8, 0, 8, 9); reg(0, dim - 8, 9, 8);
    var a = ALIGN[ver], n = a.length;
    for (var i = 0; i < n; i++) for (var j = 0; j < n; j++) {
      if ((i === 0 && (j === 0 || j === n - 1)) || (i === n - 1 && j === 0)) continue;
      reg(a[j] - 2, a[i] - 2, 5, 5);
    }
    reg(6, 9, 1, dim - 17); reg(9, 6, dim - 17, 1);
    if (ver > 6) { reg(dim - 11, 0, 3, 6); reg(0, dim - 11, 6, 3); }
    return f;
  }
  function leseCodewoerter(m, ver, maske) {
    var dim = 17 + 4 * ver, f = funktionsMuster(ver), out = [], cur = 0, nbits = 0, hoch = true;
    for (var j = dim - 1; j > 0; j -= 2) {
      if (j === 6) j--;
      for (var c = 0; c < dim; c++) {
        var i = hoch ? dim - 1 - c : c;
        for (var col = 0; col < 2; col++) {
          var x = j - col;
          if (f[i][x]) continue;
          var b = m[i][x] ^ (maskiert(maske, i, x) ? 1 : 0);
          cur = (cur << 1) | b; nbits++;
          if (nbits === 8) { out.push(cur); cur = 0; nbits = 0; }
        }
      }
      hoch = !hoch;
    }
    return out;
  }
  function entflechten(raw, ver, stufe) {
    var t = EC[ver][stufe], ec = t[0], bloecke = [];
    t[1].forEach(function (g) { for (var k = 0; k < g[0]; k++) bloecke.push({ n: g[1], cw: [] }); });
    var maxN = 0; bloecke.forEach(function (b) { if (b.n > maxN) maxN = b.n; });
    var off = 0;
    for (var i = 0; i < maxN; i++) bloecke.forEach(function (b) { if (i < b.n) b.cw.push(raw[off++]); });
    for (var e = 0; e < ec; e++) bloecke.forEach(function (b) { b.cw.push(raw[off++]); });
    var daten = [];
    for (var q = 0; q < bloecke.length; q++) {
      var bl = bloecke[q];
      if (bl.cw.some(function (v) { return v === undefined; })) return null;
      if (!rsDecode(bl.cw, ec)) return null;
      for (var d = 0; d < bl.n; d++) daten.push(bl.cw[d]);
    }
    return daten;
  }
  var ALNUM = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:";
  function utf8(bytes) {
    try { return new TextDecoder("utf-8", { fatal: true }).decode(new Uint8Array(bytes)); }
    catch (e) { var s = ""; for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]); return s; }
  }
  function entschluesseln(daten, ver) {
    var pos = 0;
    function lies(n) {
      var v = 0;
      for (var i = 0; i < n; i++) {
        var byte = daten[pos >> 3];
        if (byte === undefined) return -1;
        v = (v << 1) | ((byte >> (7 - (pos & 7))) & 1); pos++;
      }
      return v;
    }
    var text = "", gross = ver >= 10;
    while (pos + 4 <= daten.length * 8) {
      var modus = lies(4);
      if (modus <= 0) break;
      if (modus === 7) {                       // ECI: Kennung überlesen, UTF-8 annehmen
        var e1 = lies(8);
        if ((e1 & 0x80) === 0) {} else if ((e1 & 0xc0) === 0x80) lies(8); else lies(16);
        continue;
      }
      if (modus === 1) {                       // Zahlen
        var n = lies(gross ? 12 : 10);
        while (n >= 3) { var v3 = lies(10); if (v3 < 0) return null; text += ("00" + v3).slice(-3); n -= 3; }
        if (n === 2) { var v2 = lies(7); text += ("0" + v2).slice(-2); }
        else if (n === 1) { text += String(lies(4)); }
      } else if (modus === 2) {                // Alphanumerisch
        var na = lies(gross ? 11 : 9);
        while (na >= 2) { var w = lies(11); if (w < 0) return null; text += ALNUM[Math.floor(w / 45)] + ALNUM[w % 45]; na -= 2; }
        if (na === 1) text += ALNUM[lies(6)];
      } else if (modus === 4) {                // Byte
        var nb = lies(gross ? 16 : 8), bytes = [];
        for (var k = 0; k < nb; k++) { var bv = lies(8); if (bv < 0) return null; bytes.push(bv); }
        text += utf8(bytes);
      } else {
        return text.length ? text : null;      // Kanji o. Ä.: nicht unterstützt
      }
    }
    return text;
  }
  function dekodiereMatrix(m, dim) {
    var ver = (dim - 17) / 4;
    if (ver < 1 || ver > 10 || ver !== Math.floor(ver)) return null;
    var fmt = leseFormat(m, dim);
    if (!fmt) return null;
    var raw = leseCodewoerter(m, ver, fmt.maske);
    var daten = entflechten(raw, ver, fmt.stufe);
    if (!daten) return null;
    return entschluesseln(daten, ver);
  }

  // ── Zusammensetzen ────────────────────────────────────────────────────────
  function dekodiere(img) {
    var B = binarisieren(img);
    var drei = waehleDrei(sucheSuchmuster(B));
    // Zweiter, gründlicherer Durchlauf: jede Zeile statt jeder zweiten oder
    // dritten. Hilft bei Unschärfe und Rauschen, wo ein Suchmuster sonst
    // zwischen den abgetasteten Zeilen zerfällt.
    if (!drei) drei = waehleDrei(sucheSuchmuster(B, 1));
    if (!drei) return null;
    var modul = modulGroesse(B, drei);
    var dOR = Math.round(abstand(drei.ol, drei.or) / modul);
    var dUL = Math.round(abstand(drei.ol, drei.ul) / modul);
    var roh = Math.floor((dOR + dUL) / 2) + 7;
    switch (roh & 3) { case 0: roh++; break; case 2: roh--; break; case 3: roh -= 2; break; }
    // Die Schätzung der Kantenlänge kann um eine Version danebenliegen.
    var dims = [roh, roh + 4, roh - 4, roh + 8, roh - 8];
    for (var di = 0; di < dims.length; di++) {
      var dim = dims[di], ver = (dim - 17) / 4;
      if (ver < 1 || ver > 10) continue;
      var vier, ziel;
      var unten = { x: drei.or.x - drei.ol.x + drei.ul.x, y: drei.or.y - drei.ol.y + drei.ul.y };
      if (ver >= 2) {
        var k = 1 - 3 / (dim - 7);
        var ex = drei.ol.x + k * (unten.x - drei.ol.x), ey = drei.ol.y + k * (unten.y - drei.ol.y);
        var aus = sucheAusrichtung(B, ex, ey, modul);
        if (aus) { vier = aus; ziel = [dim - 6.5, dim - 6.5]; }
      }
      var versuche = [];
      if (vier) versuche.push([vier, ziel]);
      versuche.push([unten, [dim - 3.5, dim - 3.5]]);   // ohne Ausrichtung: affine Schätzung
      for (var vi = 0; vi < versuche.length; vi++) {
        var H = homographie(
          [[3.5, 3.5], [dim - 3.5, 3.5], [3.5, dim - 3.5], versuche[vi][1]],
          [[drei.ol.x, drei.ol.y], [drei.or.x, drei.or.y], [drei.ul.x, drei.ul.y],
           [versuche[vi][0].x, versuche[vi][0].y]]);
        if (!H) continue;
        var text = dekodiereMatrix(abtasten(B, H, dim), dim);
        if (text !== null && text !== undefined && text !== "") return text;
      }
      // Nachkorrektur der vierten Ecke: Bei Version 1 gibt es kein
      // Ausrichtungsmuster, und bei starker Perspektive liegt die affine
      // Schätzung daneben. Ein kleines Raster um die Schätzung ausprobieren –
      // nur wenn alles andere scheiterte, und jeder Versuch kostet unter 1 ms.
      // Falsche Treffer schließt die Fehlerkorrektur aus.
      var basis = versuche[versuche.length - 1];
      for (var oy = -2; oy <= 2; oy++) for (var ox = -2; ox <= 2; ox++) {
        if (!ox && !oy) continue;
        var px = basis[0].x + ox * modul * 0.6, py = basis[0].y + oy * modul * 0.6;
        var H2 = homographie(
          [[3.5, 3.5], [dim - 3.5, 3.5], [3.5, dim - 3.5], basis[1]],
          [[drei.ol.x, drei.ol.y], [drei.or.x, drei.or.y], [drei.ul.x, drei.ul.y], [px, py]]);
        if (!H2) continue;
        var t2 = dekodiereMatrix(abtasten(B, H2, dim), dim);
        if (t2) return t2;
      }
    }
    return null;
  }

  window.fundusQRDecode = function (imageData) {
    try { return dekodiere(imageData); } catch (e) { return null; }
  };
  // Für die Prüfung in Node ohne Browser.
  if (typeof module !== "undefined") module.exports = { dekodiere: dekodiere, rsDecode: rsDecode };
})();
