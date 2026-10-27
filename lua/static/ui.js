// ui.js – gemeinsame Oberflächen-Bausteine (R541)
//  1. fundusToast(text, art): kleine Einblendung unten statt Browser-Dialog.
//     alert() wird darauf umgeleitet (confirm() bleibt – Seiten warten auf die Antwort).
//  2. Lade-Platzhalter: Elemente, die nur "Lade …" enthalten, schimmern, bis
//     der echte Inhalt da ist.
//  3. Untere Leiste (mobil): aktiven Bereich markieren; im Messenger ausblenden.
(function () {
  "use strict";

  // ── 1. Einblendungen ──────────────────────────────────────────────────────
  function stack() {
    var s = document.getElementById("toast-stack");
    if (!s) {
      s = document.createElement("div");
      s.id = "toast-stack";
      s.setAttribute("role", "status");
      s.setAttribute("aria-live", "polite");
      document.body.appendChild(s);
    }
    return s;
  }
  function guessKind(t) {
    t = String(t || "").trim();
    if (/^(✗|❌|⚠|Fehler|Error)/i.test(t)) return "error";
    if (/^(✓|✅)/.test(t)) return "success";
    return "info";
  }
  window.fundusToast = function (text, kind, ms) {
    if (!document.body) { return; }
    kind = kind || guessKind(text);
    var el = document.createElement("div");
    el.className = "toast toast-" + kind;
    el.textContent = String(text == null ? "" : text);
    stack().appendChild(el);
    requestAnimationFrame(function () { el.classList.add("in"); });
    var close = function () {
      el.classList.remove("in");
      setTimeout(function () { if (el.parentNode) el.parentNode.removeChild(el); }, 250);
    };
    el.addEventListener("click", close);
    setTimeout(close, ms || (kind === "error" ? 7000 : 4000));
  };
  var nativeAlert = window.alert;
  window.alert = function (msg) {
    try { window.fundusToast(msg); } catch (e) { nativeAlert.call(window, msg); }
  };

  // ── 2. Lade-Platzhalter ───────────────────────────────────────────────────
  var LOADING = /^(⏳\s*)?(Lade|Loading)\s*(…|\.\.\.)?$/i;
  function markLoading(root) {
    var els = (root || document).querySelectorAll("div, p, span, td, section");
    for (var i = 0; i < els.length; i++) {
      var e = els[i];
      if (e.children.length || e.classList.contains("is-loading")) continue;
      if (!LOADING.test((e.textContent || "").trim())) continue;
      e.classList.add("is-loading");
      (function (el) {
        var mo = new MutationObserver(function () {
          if (!LOADING.test((el.textContent || "").trim()) || el.children.length) {
            el.classList.remove("is-loading");
            mo.disconnect();
          }
        });
        mo.observe(el, { childList: true, characterData: true, subtree: true });
      })(e);
    }
  }

  // ── 3. Untere Leiste ──────────────────────────────────────────────────────
  function bottomNav() {
    var nav = document.getElementById("bottom-nav");
    if (!nav) return;
    var path = location.pathname;
    document.body.classList.add("has-bottom-nav");
    var links = nav.querySelectorAll("a[data-bn]");
    for (var i = 0; i < links.length; i++) {
      var p = links[i].getAttribute("data-bn");
      var on = p === "/" ? path === "/" : (path === p || path.indexOf(p + "/") === 0);
      links[i].classList.toggle("on", on);
      if (on) links[i].setAttribute("aria-current", "page");
    }
  }

  // ── 3b. Leiste am SICHTBAREN Bildschirm halten (R852) ────────────────────
  // Manche mobilen Browser verankern position:fixed am Layout-Fenster statt am
  // sichtbaren Ausschnitt – nach Zoom, beim Ein- und Ausfahren der Adressleiste
  // oder bei eigenen Scroll-Eigenheiten wandert die Leiste dann mit dem Inhalt.
  // visualViewport kennt den Ausschnitt, den man wirklich sieht: Weicht dessen
  // Unterkante ab, wird die Leiste um genau diese Differenz verschoben. Stimmt
  // alles, passiert nichts. Bei offener Tastatur wird sie ausgeblendet, statt
  // über der Tastatur zu schweben.
  function leisteVerankern() {
    var nav = document.getElementById("bottom-nav"), vv = window.visualViewport;
    if (!nav || !vv) return;
    var geplant = 0;
    function setzen() {
      geplant = 0;
      var luecke = window.innerHeight - (vv.offsetTop + vv.height);
      var tastatur = luecke > 150 && vv.scale <= 1.01;
      nav.style.visibility = tastatur ? "hidden" : "";
      nav.style.transform = !tastatur && Math.abs(luecke) > 1 ? "translateY(" + (-luecke) + "px)" : "";
    }
    function bald() { if (!geplant) geplant = requestAnimationFrame(setzen); }
    vv.addEventListener("resize", bald);
    vv.addEventListener("scroll", bald);
    window.addEventListener("scroll", bald, { passive: true });
    setzen();
  }

  // Prüfansicht: ?leiste=pruefen zeigt live, was der Browser misst – für ein
  // Bildschirmfoto, wenn die Leiste auf einem Gerät doch wandert.
  function leistePruefen() {
    if (!/[?&]leiste=pruefen\b/.test(location.search)) return;
    var nav = document.getElementById("bottom-nav"), vv = window.visualViewport;
    var feld = document.createElement("pre");
    feld.style.cssText = "position:fixed;top:70px;left:8px;right:8px;z-index:9999;margin:0;padding:8px 10px;" +
      "font:11px/1.45 monospace;color:#fff;background:rgba(0,0,0,.82);border:1px solid #5ec8ff;border-radius:8px;white-space:pre-wrap;pointer-events:none";
    document.body.appendChild(feld);
    function zeigen() {
      var r = nav ? nav.getBoundingClientRect() : null, cs = nav ? getComputedStyle(nav) : null;
      feld.textContent =
        "Browser   " + navigator.userAgent.replace(/^Mozilla\/5\.0 /, "").slice(0, 140) + "\n" +
        "Fenster   innerHeight " + window.innerHeight + " · scrollY " + Math.round(window.scrollY) + "\n" +
        (vv ? "Sichtbar  höhe " + Math.round(vv.height) + " · oben " + Math.round(vv.offsetTop) + " · zoom " + vv.scale.toFixed(2) + "\n" : "Sichtbar  (kein visualViewport)\n") +
        (r ? "Leiste    unterkante " + Math.round(r.bottom) + " · position " + cs.position + " · verschoben " + (nav.style.transform || "nein") : "Leiste    nicht vorhanden") +
        "\nStandalone " + (window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true ? "ja (Home-Bildschirm)" : "nein");
    }
    window.addEventListener("scroll", zeigen, { passive: true });
    if (vv) { vv.addEventListener("resize", zeigen); vv.addEventListener("scroll", zeigen); }
    setInterval(zeigen, 500); zeigen();
  }

  // ── 4. Seitenübergänge (R581) ─────────────────────────────────────────────
  // Beim Klick auf einen internen Link den Inhalt kurz ausblenden, dann laden.
  function pageTransitions() {
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    document.addEventListener("click", function (e) {
      var a = e.target.closest && e.target.closest("a[href]");
      if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey) return;
      if (a.target === "_blank" || a.hasAttribute("download")) return;
      var href = a.getAttribute("href") || "";
      if (!href || href[0] === "#" || /^(mailto|tel|javascript):/i.test(href)) return;
      try { if (new URL(a.href).origin !== location.origin) return; } catch (err) { return; }
      e.preventDefault();
      document.body.classList.add("leaving");
      setTimeout(function () { location.href = a.href; }, 150);
    });
    // Zurück-Navigation: Seite wieder sichtbar machen (bfcache)
    window.addEventListener("pageshow", function () { document.body.classList.remove("leaving"); });
  }

  // ── 5. Vibration auf dem Handy ────────────────────────────────────────────
  // Kurzer Impuls bei Aktionen – nur wo das Gerät es unterstützt.
  window.fundusBuzz = function (pattern) {
    try { if (navigator.vibrate) navigator.vibrate(pattern || 12); } catch (e) {}
  };
  function haptics() {
    if (!navigator.vibrate) return;
    document.addEventListener("click", function (e) {
      var el = e.target.closest && e.target.closest("button, .btn, .btn-sm, .chip, .bottom-nav a, .hc-fav");
      if (!el || el.disabled) return;
      fundusBuzz(el.classList.contains("btn-danger") ? [10, 40, 10] : 12);
    }, { passive: true });
  }

  // ── 6. Leere Listen mit Symbol und Vorschlag ──────────────────────────────
  var EMPTY_ICONS = { listings: "listings", messenger: "messenger", shared: "shared", files: "files", wallet: "wallet" };
  function decorateEmpty(root) {
    var box = (root || document).querySelectorAll(".empty-hint");
    for (var i = 0; i < box.length; i++) {
      var el = box[i];
      if (el.querySelector(".ico") || el.dataset.decorated) continue;
      el.dataset.decorated = "1";
      var key = el.getAttribute("data-empty") || "";
      var svg = (window.FUNDUS_ICONS || {})[EMPTY_ICONS[key] || key] || (window.FUNDUS_ICONS || {}).listings || "";
      var text = (el.textContent || "").trim();
      el.innerHTML = svg + '<div class="eh-title"></div>';
      el.querySelector(".eh-title").textContent = text;
      var cta = el.getAttribute("data-cta"), href = el.getAttribute("data-href");
      if (cta && href) {
        var a = document.createElement("a");
        a.className = "btn"; a.href = href; a.textContent = cta;
        el.appendChild(a);
      }
    }
  }
  // Nachträglich eingefügte leere Listen ebenfalls erfassen.
  if (window.MutationObserver) {
    new MutationObserver(function () { decorateEmpty(); }).observe(document.documentElement, { childList: true, subtree: true });
  }

  function init() { markLoading(); bottomNav(); leisteVerankern(); leistePruefen(); pageTransitions(); haptics(); decorateEmpty(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
