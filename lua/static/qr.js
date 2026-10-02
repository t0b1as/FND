// qr.js – kompakter QR-Code-Erzeuger (ISO/IEC 18004), ohne fremde Server.
// Byte-Modus, Fehlerkorrektur M, Versionen 1–10 (bis 213 Zeichen).
//   window.fundusQR.svg(text, sizePx)  → SVG-String
//   window.fundusQR.matrix(text)       → Array von Zeilen (true = dunkel)
(function () {
  "use strict";
  // ── GF(256) ────────────────────────────────────────────────────────────
  var EXP = new Array(512), LOG = new Array(256);
  (function () {
    var x = 1;
    for (var i = 0; i < 255; i++) { EXP[i] = x; LOG[x] = i; x <<= 1; if (x & 0x100) x ^= 0x11d; }
    for (var j = 255; j < 512; j++) EXP[j] = EXP[j - 255];
  })();
  function gmul(a, b) { return (a && b) ? EXP[LOG[a] + LOG[b]] : 0; }
  function rsGenerator(n) {
    var g = [1];
    for (var i = 0; i < n; i++) {
      var ng = new Array(g.length + 1).fill(0);
      for (var j = 0; j < g.length; j++) { ng[j] ^= g[j]; ng[j + 1] ^= gmul(g[j], EXP[i]); }
      g = ng;
    }
    return g;
  }
  function rsEncode(data, ecLen) {
    var gen = rsGenerator(ecLen), res = new Array(ecLen).fill(0);
    for (var i = 0; i < data.length; i++) {
      var f = data[i] ^ res[0];
      res.shift(); res.push(0);
      if (f) for (var j = 0; j < ecLen; j++) res[j] ^= gmul(gen[j + 1], f);
    }
    return res;
  }
  // ── Tabellen (Fehlerkorrektur M): Version → [EC je Block, [Anzahl, Datenwörter]...]
  var EC_M = {
    1: [10, [1, 16]], 2: [16, [1, 28]], 3: [26, [1, 44]], 4: [18, [2, 32]], 5: [24, [2, 43]],
    6: [16, [4, 27]], 7: [18, [4, 31]], 8: [22, [2, 38], [2, 39]], 9: [22, [3, 36], [2, 37]], 10: [26, [4, 43], [1, 44]]
  };
  var ALIGN = { 1: [], 2: [6, 18], 3: [6, 22], 4: [6, 26], 5: [6, 30], 6: [6, 34], 7: [6, 22, 38], 8: [6, 24, 42], 9: [6, 26, 46], 10: [6, 28, 50] };
  function dataCapacity(v) { var t = EC_M[v], n = 0; for (var i = 1; i < t.length; i++) n += t[i][0] * t[i][1]; return n; }

  function utf8(s) { return Array.from(new TextEncoder().encode(s)); }

  function makeData(bytes, v) {
    var cap = dataCapacity(v), bits = [];
    function put(val, n) { for (var i = n - 1; i >= 0; i--) bits.push((val >> i) & 1); }
    put(4, 4);                              // Byte-Modus
    put(bytes.length, v >= 10 ? 16 : 8);    // Zeichenzahl
    bytes.forEach(function (b) { put(b, 8); });
    var maxBits = cap * 8;
    for (var t = 0; t < 4 && bits.length < maxBits; t++) bits.push(0); // Terminator
    while (bits.length % 8) bits.push(0);
    var out = [];
    for (var i = 0; i < bits.length; i += 8) { var b = 0; for (var j = 0; j < 8; j++) b = (b << 1) | bits[i + j]; out.push(b); }
    for (var k = 0; out.length < cap; k++) out.push(k % 2 ? 0x11 : 0xEC);
    return out;
  }
  function interleave(data, v) {
    var t = EC_M[v], ecLen = t[0], blocks = [], pos = 0;
    for (var g = 1; g < t.length; g++) for (var b = 0; b < t[g][0]; b++) { blocks.push(data.slice(pos, pos + t[g][1])); pos += t[g][1]; }
    var ecs = blocks.map(function (bl) { return rsEncode(bl, ecLen); });
    var out = [], maxLen = Math.max.apply(null, blocks.map(function (b) { return b.length; }));
    for (var i = 0; i < maxLen; i++) blocks.forEach(function (bl) { if (i < bl.length) out.push(bl[i]); });
    for (var e = 0; e < ecLen; e++) ecs.forEach(function (ec) { out.push(ec[e]); });
    return out;
  }
  // ── Matrix ─────────────────────────────────────────────────────────────
  function build(v, codewords, mask) {
    var n = v * 4 + 17, m = [], fn = [];
    for (var y = 0; y < n; y++) { m.push(new Array(n).fill(false)); fn.push(new Array(n).fill(false)); }
    function set(x, y, val) { m[y][x] = val; fn[y][x] = true; }
    function finder(cx, cy) {
      for (var dy = -1; dy <= 7; dy++) for (var dx = -1; dx <= 7; dx++) {
        var x = cx + dx, y = cy + dy; if (x < 0 || y < 0 || x >= n || y >= n) continue;
        var on = (dx >= 0 && dx <= 6 && (dy === 0 || dy === 6)) || (dy >= 0 && dy <= 6 && (dx === 0 || dx === 6)) || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4);
        set(x, y, on);
      }
    }
    finder(0, 0); finder(n - 7, 0); finder(0, n - 7);
    for (var i = 8; i < n - 8; i++) { set(i, 6, i % 2 === 0); set(6, i, i % 2 === 0); } // Timing
    var al = ALIGN[v];
    for (var a = 0; a < al.length; a++) for (var b = 0; b < al.length; b++) {
      var cx = al[a], cy = al[b];
      // Nur die drei Muster, die in die Suchmuster ragen, entfallen – die auf
      // den Taktlinien bleiben (sonst sind Versionen ab 7 unlesbar).
      if ((cx <= 8 && cy <= 8) || (cx >= n - 9 && cy <= 8) || (cx <= 8 && cy >= n - 9)) continue;
      for (var dy = -2; dy <= 2; dy++) for (var dx = -2; dx <= 2; dx++) set(cx + dx, cy + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
    }
    set(8, n - 8, true); // dunkles Modul
    // Format-/Versionsbereiche reservieren
    for (var f = 0; f < 9; f++) { if (f !== 6) { fn[8][f] = true; fn[f][8] = true; } }
    for (var f2 = 0; f2 < 8; f2++) { fn[8][n - 1 - f2] = true; fn[n - 1 - f2][8] = true; }
    if (v >= 7) for (var vy = 0; vy < 6; vy++) for (var vx = 0; vx < 3; vx++) { fn[vy][n - 11 + vx] = true; fn[n - 11 + vx][vy] = true; }
    // Daten im Zickzack
    var bitIdx = 0, total = codewords.length * 8;
    function bit(i) { return (codewords[i >> 3] >> (7 - (i & 7))) & 1; }
    for (var right = n - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5;
      for (var vert = 0; vert < n; vert++) {
        var y = ((right + 1) & 2) ? vert : n - 1 - vert; // Richtung wechselt je Spaltenpaar
        for (var k = 0; k < 2; k++) {
          var x = right - k;
          if (fn[y][x]) continue;
          var d = bitIdx < total ? bit(bitIdx) : 0; bitIdx++;
          m[y][x] = !!(d ^ maskBit(mask, x, y));
        }
      }
    }
    // Format-Info (EC M = 00) mit BCH
    var fmt = (0 << 3) | mask, r = fmt << 10;
    for (var s = 14; s >= 10; s--) if ((r >> s) & 1) r ^= 0x537 << (s - 10);
    var bits = ((fmt << 10) | r) ^ 0x5412;
    // Platzierung wie in der Norm (x, y): erste Kopie um den Finder oben links,
    // zweite Kopie unter dem Finder oben rechts / neben dem Finder unten links.
    for (var i2 = 0; i2 < 15; i2++) {
      var bv = !!((bits >> i2) & 1);
      if (i2 <= 5) m[i2][8] = bv;            // (8, i)
      else if (i2 === 6) m[7][8] = bv;       // (8, 7)
      else if (i2 === 7) m[8][8] = bv;       // (8, 8)
      else if (i2 === 8) m[8][7] = bv;       // (7, 8)
      else m[8][14 - i2] = bv;               // (14-i, 8)
      if (i2 < 8) m[8][n - 1 - i2] = bv;     // (n-1-i, 8)
      else m[n - 15 + i2][8] = bv;           // (8, n-15+i)
    }
    if (v >= 7) {
      var vr = v << 12;
      for (var s2 = 17; s2 >= 12; s2--) if ((vr >> s2) & 1) vr ^= 0x1F25 << (s2 - 12);
      var vbits = (v << 12) | vr;
      for (var i3 = 0; i3 < 18; i3++) { var vb = !!((vbits >> i3) & 1); m[Math.floor(i3 / 3)][n - 11 + (i3 % 3)] = vb; m[n - 11 + (i3 % 3)][Math.floor(i3 / 3)] = vb; }
    }
    return m;
  }
  function maskBit(mask, x, y) {
    switch (mask) {
      case 0: return ((x + y) % 2 === 0) ? 1 : 0;
      case 1: return (y % 2 === 0) ? 1 : 0;
      case 2: return (x % 3 === 0) ? 1 : 0;
      case 3: return ((x + y) % 3 === 0) ? 1 : 0;
      case 4: return ((Math.floor(y / 2) + Math.floor(x / 3)) % 2 === 0) ? 1 : 0;
      case 5: return ((x * y) % 2 + (x * y) % 3 === 0) ? 1 : 0;
      case 6: return (((x * y) % 2 + (x * y) % 3) % 2 === 0) ? 1 : 0;
      default: return (((x + y) % 2 + (x * y) % 3) % 2 === 0) ? 1 : 0;
    }
  }
  function penalty(m) {
    var n = m.length, p = 0, x, y, run, c;
    for (y = 0; y < n; y++) { run = 1; for (x = 1; x < n; x++) { if (m[y][x] === m[y][x - 1]) { run++; if (run === 5) p += 3; else if (run > 5) p++; } else run = 1; } }
    for (x = 0; x < n; x++) { run = 1; for (y = 1; y < n; y++) { if (m[y][x] === m[y - 1][x]) { run++; if (run === 5) p += 3; else if (run > 5) p++; } else run = 1; } }
    for (y = 0; y < n - 1; y++) for (x = 0; x < n - 1; x++) { c = m[y][x]; if (c === m[y][x + 1] && c === m[y + 1][x] && c === m[y + 1][x + 1]) p += 3; }
    var pat = [true, false, true, true, true, false, true, false, false, false, false], pat2 = pat.slice().reverse();
    function hasAt(get, i, len) { for (var k = 0; k < 11; k++) { if (i + k >= len) return false; var v = get(i + k); if (v !== pat[k] && v !== pat2[k]) return false; } return true; }
    for (y = 0; y < n; y++) for (x = 0; x < n; x++) {
      var rowGet = (function (yy) { return function (i) { return m[yy][i]; }; })(y);
      var colGet = (function (xx) { return function (i) { return m[i][xx]; }; })(x);
      if (hasAt(rowGet, x, n)) p += 40; if (hasAt(colGet, y, n)) p += 40;
    }
    var dark = 0; for (y = 0; y < n; y++) for (x = 0; x < n; x++) if (m[y][x]) dark++;
    var pct = dark * 100 / (n * n); p += Math.floor(Math.abs(pct - 50) / 5) * 10;
    return p;
  }
  function matrix(text) {
    var bytes = utf8(String(text));
    var v = 1;
    while (v <= 10 && bytes.length + (v >= 10 ? 3 : 2) > dataCapacity(v)) v++;
    if (v > 10) throw new Error("QR: Text zu lang (max. 213 Zeichen)");
    var cw = interleave(makeData(bytes, v), v);
    var best = null, bestP = Infinity;
    for (var mask = 0; mask < 8; mask++) { var m = build(v, cw, mask); var p = penalty(m); if (p < bestP) { bestP = p; best = m; } }
    return best;
  }
  function svg(text, size) {
    var m = matrix(text), n = m.length, q = 4, s = size || 240, cell = s / (n + 2 * q), d = "";
    for (var y = 0; y < n; y++) for (var x = 0; x < n; x++) if (m[y][x]) d += "M" + ((x + q) * cell) + " " + ((y + q) * cell) + "h" + cell + "v" + cell + "h-" + cell + "z";
    return '<svg xmlns="http://www.w3.org/2000/svg" width="' + s + '" height="' + s + '" viewBox="0 0 ' + s + ' ' + s + '" shape-rendering="crispEdges">' +
      '<rect width="100%" height="100%" fill="#fff"/><path d="' + d + '" fill="#000"/></svg>';
  }
  window.fundusQR = { matrix: matrix, svg: svg };
})();
