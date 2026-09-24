// navvis.js — zentrale Steuerung der Navigations-Sichtbarkeit.
// Drei unabhängige Zielorte je Navigationspunkt: Landing-Page, Burger-Menü,
// und die Schnellzugriff-Leiste in der Titelzeile. Einstellung liegt lokal
// (localStorage), ist also pro Gerät/Browser — passend zum Ausblenden von
// Beta-Features.

(function(){
  // Die konfigurierbaren Navigationspunkte (bis inkl. Fundus Love).
  // key: stabiler Bezeichner, path: Ziel, label: Anzeige, icon: Emoji/HTML.
  window.NAV_ITEMS = [
    { key:"listings",     path:"/listings",     label:"Marktplatz",   icon:"🛒", color:"green",  defQuick:true },
    { key:"energy",       path:"/energy",       label:"Energie",      icon:"⚡", beta:true, color:"gold" },
    { key:"certificates", path:"/certificates", label:"Zertifikate",  icon:"📜", beta:true, color:"blue" },
    { key:"shop",         path:"/shop",         label:"FND/SOL",      icon:"💰", color:"green",  defQuick:true },
    { key:"jobs",         path:"/jobs",         label:"Jobs",         icon:"💼", beta:true, color:"purple" },
    { key:"files",        path:"/files",        label:"Dateien",      icon:"📁", color:"pink",   defQuick:true },
    { key:"messenger",    path:"/messenger",    label:"Messenger",    icon:"💬", beta:true, color:"blue", defQuick:true },
    { key:"partner",      path:"/partner",      label:"Fundus Love",  icon:"💗", color:"rose", defQuick:true },
    { key:"wallet",       path:"/wallet",       label:"Wallet",       icon:"👛", color:"gold",   defQuick:true }
  ];

  // Standard: überall sichtbar außer Schnellzugriff (der ist opt-in, damit die
  // Titelzeile nicht überläuft — nur die wichtigsten Punkte).
  const DEFAULTS = {
    landing: true,   // Kachel auf der Landing-Page
    burger:  true,   // Eintrag im Burger-Menü
    quick:   false   // Button in der Titelzeilen-Schnellzugriffsleiste
  };

  const LS_KEY = "fundus_nav_vis";

  window.navGetVis = function(){
    let v = {};
    try { v = JSON.parse(localStorage.getItem(LS_KEY) || "{}"); } catch(e){ v = {}; }
    // Fehlende Punkte mit Defaults füllen (quick pro Item über defQuick).
    window.NAV_ITEMS.forEach(function(it){
      const defQuick = !!it.defQuick;
      if (!v[it.key]) v[it.key] = { landing:true, burger:true, quick:defQuick };
      else {
        if (typeof v[it.key].landing !== "boolean") v[it.key].landing = true;
        if (typeof v[it.key].burger  !== "boolean") v[it.key].burger  = true;
        if (typeof v[it.key].quick   !== "boolean") v[it.key].quick   = defQuick;
      }
    });
    return v;
  };

  window.navSetVis = function(v){
    try { localStorage.setItem(LS_KEY, JSON.stringify(v)); } catch(e){}
    window.navApply();
  };

  // Wendet die Sichtbarkeit auf die aktuelle Seite an: Burger-Einträge,
  // Landing-Kacheln und die Schnellzugriffsleiste.
  window.navApply = function(){
    const v = window.navGetVis();

    // 1. Burger-Menü: Einträge per data-nav-key ein-/ausblenden.
    document.querySelectorAll("[data-nav-key]").forEach(function(el){
      const key = el.getAttribute("data-nav-key");
      if (!v[key]) return;
      const zone = el.getAttribute("data-nav-zone") || "burger";
      el.style.display = v[key][zone] ? "" : "none";
    });

    // 2. Schnellzugriffsleiste in der Titelzeile aufbauen.
    const quick = document.getElementById("nav-quick");
    if (quick){
      quick.innerHTML = "";
      window.NAV_ITEMS.forEach(function(it){
        if (v[it.key] && v[it.key].quick){
          const a = document.createElement("a");
          a.href = it.path;
          a.className = "nav-quick-btn nqb-"+(it.color||"green");
          a.title = it.label;
          a.innerHTML = '<span class="nqi">'+it.icon+'</span>';
          // Ungelesen-Badge für Messenger direkt mit einbauen (aus persistentem
          // Zähler), sonst löscht dieser innerHTML-Rebuild das von msgnotify
          // gesetzte Badge wieder weg.
          if (it.key === "messenger") {
            try {
              // Nur zeigen, wenn ungelesene UND eingeloggt. Das Session-Cookie
              // ist HttpOnly (per JS nicht lesbar) → über window.WALLET prüfen.
              const st = JSON.parse(localStorage.getItem("fnd_msg_unread") || "{}");
              const loggedIn = !!(window.WALLET && window.WALLET.address);
              if (st.count > 0 && loggedIn) {
                a.style.position = "relative";
                const b = document.createElement("span");
                b.className = "msg-nav-badge";
                b.textContent = st.count > 99 ? "99+" : st.count;
                a.appendChild(b);
              }
            } catch(e){}
          }
          quick.appendChild(a);
        }
      });
    }
  };

  // Beim Laden anwenden — mehrfach, robust gegen spät ergänztes DOM und
  // gegen JS-Fehler auf einzelnen Seiten (try/catch, damit ein Fehler die
  // Buttons nicht verschwinden lässt).
  function safeApply(){ try { window.navApply(); } catch(e){} }
  if (document.readyState === "loading"){
    document.addEventListener("DOMContentLoaded", safeApply);
  } else {
    safeApply();
  }
  // Mehrere Nachzügler-Versuche: fängt Seiten ab, die ihr DOM/Header erst per
  // JS aufbauen oder deren load-Event durch WebSockets/lange Requests verzögert
  // ist (z.B. Messenger). navApply ist idempotent, mehrfaches Aufrufen schadet nicht.
  window.addEventListener("load", safeApply);
  var _naTries = 0;
  var _naTimer = setInterval(function(){
    safeApply();
    if (++_naTries >= 5) clearInterval(_naTimer); // bis ~2.5s nach Start
  }, 500);
})();
