// navvis.js — zentrale Steuerung der Navigations-Sichtbarkeit.
// Drei unabhängige Zielorte je Navigationspunkt: Landing-Page, Burger-Menü,
// und die Schnellzugriff-Leiste in der Titelzeile.
// R928: Die Einstellung gehört zum KONTO, nicht zum Browser – sie liegt
// verschlüsselt beim Node (/api/v1/konto/navigation) und folgt dem Konto auf
// jeden Node. Ohne Anmeldung gilt die Standard-Navigation. Im Browser liegt nur
// eine Kopie der letzten Antwort, damit die Leiste ohne Flackern erscheint.

(function(){
  // Die konfigurierbaren Navigationspunkte (bis inkl. Fundus Love).
  // key: stabiler Bezeichner, path: Ziel, label: Anzeige, icon: Emoji/HTML.
  window.NAV_ITEMS = [
    { key:"listings",     path:"/listings",     label:"Marktplatz",   icon:"🛒", color:"green",  defQuick:true },
    { key:"energy",       path:"/energy",       label:"Energie",      icon:"⚡", beta:true, color:"gold" },
    { key:"aeronautik",   path:"/aeronautik",   label:"Aeronautik",   icon:"✈", color:"blue",   defQuick:true },
    { key:"certificates", path:"/certificates", label:"Zertifikate",  icon:"📜", beta:true, color:"blue" },
    { key:"shop",         path:"/shop",         label:"FND/SOL",      icon:"💰", color:"green",  defQuick:true },
    { key:"drive",        path:"/drive",        label:"Drive",        icon:"🚗", color:"cyan",   defQuick:true },
    { key:"jobs",         path:"/jobs",         label:"Jobs",         icon:"💼", beta:true, color:"purple", defLanding:false },
    { key:"files",        path:"/files",        label:"Dateien",      icon:"📁", color:"pink",   defQuick:true, quickPath:"/shared", quickLabel:"Freigaben" },
    { key:"messenger",    path:"/messenger",    label:"Messenger",    icon:"💬", color:"blue", defQuick:true },
    { key:"feed",         path:"/feed",         label:"Feed",         icon:"🌊", color:"purple", defQuick:true },
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

  const CACHE_KEY = "fundus_nav_konto"; // Kopie der Kontoeinstellung (R928)
  try { localStorage.removeItem("fundus_nav_vis"); } catch(e){} // alte Geräte-Einstellung (bis R927)

  let _konto = null;        // Einstellung des angemeldeten Kontos (oder null = Standard)
  let _angemeldet = false;
  try { const c = JSON.parse(localStorage.getItem(CACHE_KEY) || "null"); if (c && c.vis) { _konto = c.vis; _angemeldet = true; } } catch(e){}

  window.navAngemeldet = function(){ return _angemeldet; };

  window.navGetVis = function(){
    let v = {};
    try { v = JSON.parse(JSON.stringify(_konto || {})); } catch(e){ v = {}; }
    // Fehlende Punkte mit Defaults füllen (quick pro Item über defQuick).
    window.NAV_ITEMS.forEach(function(it){
      const defQuick = !!it.defQuick;
      // defLanding:false → Kachel standardmäßig AUS (R601: Jobs ist Beta,
      // der Messenger steht dort jetzt an seiner Stelle). Zuschaltbar bleibt
      // alles über Einstellungen → Navigation anpassen.
      const defLanding = (it.defLanding !== false);
      if (!v[it.key]) v[it.key] = { landing:defLanding, burger:true, quick:defQuick };
      else {
        if (typeof v[it.key].landing !== "boolean") v[it.key].landing = defLanding;
        if (typeof v[it.key].burger  !== "boolean") v[it.key].burger  = true;
        if (typeof v[it.key].quick   !== "boolean") v[it.key].quick   = defQuick;
      }
    });
    return v;
  };

  // Speichert fürs angemeldete Konto. Liefert ein Promise (true = gespeichert).
  window.navSetVis = function(v){
    _konto = v;
    try { localStorage.setItem(CACHE_KEY, JSON.stringify({ vis: v })); } catch(e){}
    window.navApply();
    return fetch("/api/v1/konto/navigation", { method: "PUT", credentials: "same-origin",
        headers: { "Content-Type": "application/json" }, body: JSON.stringify({ vis: v }) })
      .then(function(r){ return r.json().catch(function(){ return {}; }).then(function(d){
        if (!r.ok || d.error) throw new Error(d.error || ("Fehler " + r.status)); return true; }); });
  };

  // Kontoeinstellung vom Node holen; danach neu anwenden und Bescheid geben.
  function kontoLaden(){
    fetch("/api/v1/konto/navigation", { credentials: "same-origin" })
      .then(function(r){ return r.ok ? r.json() : null; })
      .then(function(d){
        if (!d) return;
        _angemeldet = !!d.angemeldet;
        _konto = (d.angemeldet && d.vis && typeof d.vis === "object") ? d.vis : null;
        try {
          if (_konto) localStorage.setItem(CACHE_KEY, JSON.stringify({ vis: _konto }));
          else localStorage.removeItem(CACHE_KEY);
        } catch(e){}
        try { window.navApply(); } catch(e){}
        try { document.dispatchEvent(new CustomEvent("fundus-nav-geladen")); } catch(e){}
      }).catch(function(){});
  }
  window.navNeuLaden = kontoLaden; // z. B. nach An- oder Abmelden
  kontoLaden();

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
          // Leiste kann ein eigenes Ziel haben (Dateien → öffentliche Freigaben;
          // die Verwaltung /files bleibt im Menü, nur im Heimnetz erreichbar).
          a.href = it.quickPath || it.path;
          a.className = "nav-quick-btn nqb-"+(it.color||"green");
          a.title = it.quickLabel || it.label;
          a.innerHTML = '<span class="nqi">'+((window.FUNDUS_ICONS||{})[it.key] || it.icon)+'</span>';
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
