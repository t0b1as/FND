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
    if (path.indexOf("/messenger") === 0) { document.body.classList.add("no-bottom-nav"); return; }
    document.body.classList.add("has-bottom-nav");
    var links = nav.querySelectorAll("a[data-bn]");
    for (var i = 0; i < links.length; i++) {
      var p = links[i].getAttribute("data-bn");
      var on = p === "/" ? path === "/" : (path === p || path.indexOf(p + "/") === 0);
      links[i].classList.toggle("on", on);
      if (on) links[i].setAttribute("aria-current", "page");
    }
  }

  function init() { markLoading(); bottomNav(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
  else init();
})();
