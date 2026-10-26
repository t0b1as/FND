-- pages/energy.lua
local render = require "render"
local cjson  = require "cjson.safe"

return function()
    ngx.header["Content-Type"] = "text/html"
    local t, lang = render.header("energy.title", "energy"), ngx.ctx.lang or "de"
    lang = ngx.ctx.lang or lang

    -- Meter-Status prüfen
    local meterStatus, _ = render.api_get("/v1/meter/status")
    local meterEnabled   = meterStatus and meterStatus.enabled

    -- Live-Widget nur wenn Meter aktiv
    if meterEnabled then
        local jsStr = cjson.encode({
            kwh_label  = t("energy.col_kwh"),
            watt_label = "W",
            meter_id   = meterStatus.meter_id or "",
            lang       = lang,
        })
        ngx.print(string.format([[
<section class="meter-live">
  <h2>%s <span class="live-badge">LIVE</span></h2>
  <!-- R842: alle Messwerte in EINER Zeile, Uhrzeit in derselben Schrift und Größe -->
  <div class="meter-zeile">
    <div><span class="label">%s</span><span class="value" id="live-kwh">–</span></div>
    <div><span class="label">Einspeisung</span><span class="value" id="live-feed">–</span></div>
    <div><span class="label">%s</span><span class="value" id="live-watt">–</span></div>
    <div><span class="label">%s</span><span class="value" id="live-ts">–</span></div>
  </div>
</section>
<script>const METER_STRINGS = %s;</script>
<script src="/static/meter.js?v=]] .. require("render").rev() .. [["></script>
]], t("energy.title"), "Bezug", "Leistung",
    t("energy.col_timestamp"), jsStr))
    end

    -- Token-Tabelle
    local data, err = render.api_get("/v1/energy")
    if err then
        ngx.print("<p class='error'>" .. err .. "</p>")
    else
        local tokens = data.tokens or {}
        ngx.print("<p class='meta'>" .. t("energy.count", #tokens) .. "</p>")

        local columns = {
            { key="id",             label=t("energy.col_id")        },
            { key="data.meter_id",  label=t("energy.col_meter")     },
            { key="data.kwh",       label=t("energy.col_kwh")       },
            { key="data.lat",       label=t("energy.col_lat")       },
            { key="data.lon",       label=t("energy.col_lon")       },
            { key="data.timestamp", label=t("energy.col_timestamp") },
        }
        ngx.print(render.record_table(tokens, columns))
    end

    -- R832: Nachbarschaftshandel (Variante A)
    ngx.print([==[
<section class="set-card" id="markt" style="margin:18px 0">
  <div class="set-head"><h3>Nachbarschaftshandel</h3></div>
  <div class="set-body" id="markt-body"><p class="meta">Lade …</p></div>
</section>
<script>
(function(){
  function f(n, d){ return Number(n||0).toLocaleString('de-DE', {minimumFractionDigits:d, maximumFractionDigits:d}); }
  function zeit(u){ var d=new Date(Number(u)*1000); return d.toLocaleDateString('de-DE',{day:'2-digit',month:'2-digit'})+' '+d.toLocaleTimeString('de-DE',{hour:'2-digit',minute:'2-digit'}); }
  function esc(t){ return String(t==null?'':t).replace(/[&<>"]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c];}); }
  // R840: Live-Handel alle 2 s – dasselbe Signal liefert /api/v1/markt/live
  // an Home Assistant (REST-Sensor) für Automationen.
  var liveT=null;
  function live(){
    clearTimeout(liveT);
    fetch('/api/v1/markt/live',{credentials:'same-origin'}).then(function(r){return r.ok?r.json():null;}).then(function(d){
      var el=document.getElementById('markt-live'); if(!el||!d) return;
      var t;
      if(d.rolle==='erzeuger'){
        t='<b>Du speist ein:</b> '+f(d.einspeisung_w,0)+' W · an '+(d.abnehmer||0)+' Nachbarn: <b>'+f(d.zugeteilt_w,0)+' W</b> · Energiepreis <b>'+f(d.preis,4)+'</b> FND/kWh · Knappheit '+f((d.knappheit||0)*100,0)+' %';
      } else if(d.rolle==='verbraucher'){
        t='<b>Du beziehst:</b> '+f(d.bezug_w,0)+' W · davon vom Nachbarn <b>'+f(d.vom_nachbarn_w,0)+' W</b>'+
          (d.bester_preis!=null?' · bester Preis <b>'+f(d.bester_preis,4)+'</b> FND/kWh inkl. Netzentgelt':' · gerade kein Angebot in Reichweite');
      } else t='<span class="meta">Noch keine Sekundendaten.</span>';
      el.innerHTML='<span class="meta">Live · </span>'+t;
    }).catch(function(){}).then(function(){ liveT=setTimeout(live,2000); });
  }
  fetch('/api/v1/markt/status',{credentials:'same-origin'}).then(function(r){return r.ok?r.json():null;}).then(function(d){
    var b=document.getElementById('markt-body'); if(!d){ b.innerHTML='<p class="meta">Nicht verfügbar.</p>'; return; }
    var p=d.parameter||{};
    if(!p.teilnahme){
      b.innerHTML='<p>Nicht aktiv. Ein Teil deines Stroms würde je Viertelstunde mit Nachbarn verrechnet, der Rest wie gewohnt über deinen Tarif.</p>'+
        '<p class="meta">Einschalten in <code>/etc/fundus/fundus.env</code>: <code>FUNDUS_MARKT_TEILNAHME=1</code>, den Standort unter <a href="/settings">Einstellungen</a> auf der Karte.</p>';
      return;
    }
    var h='<p style="margin:0 0 8px">Mindestpreis <b>'+f(p.mindestpreis,3)+'</b> · Höchstpreis <b>'+f(p.hoechstpreis,3)+'</b> FND/kWh · Reichweite '+f(p.max_km,0)+' km<br>'+
      'Netzentgelt '+f(p.netz_grund,3)+' + '+f(p.netz_je_km,4)+' FND/kWh je km Luftlinie'+(p.netz_wallet?'':' <span class="meta">(nur vorgemerkt – kein Empfänger eingetragen)</span>')+'</p>'+
      '<p class="meta" style="margin:0 0 12px">Budget der Zähler-Wallet <code>'+esc((d.wallet||'').slice(0,10))+'…</code>: <b>'+f(d.guthaben_fnd,4)+' FND</b>'+
      (d.guthaben_fnd>0?'':' – ohne Guthaben läuft dein Bezug vollständig über den Tarif.')+
      (d.offen_fnd>0?' · <b>'+f(d.offen_fnd,4)+' FND</b> angenommen, noch nicht überwiesen – gezahlt wird gebündelt, spätestens nach '+(d.zahlintervall_min||60)+' min':'')+'</p>';
    function tab(titel, zeilen, spalten){
      if(!zeilen||!zeilen.length) return '<h4 style="margin:10px 0 4px">'+titel+'</h4><p class="meta">Noch nichts.</p>';
      return '<h4 style="margin:10px 0 4px">'+titel+'</h4><div style="overflow-x:auto"><table class="data-table"><tr>'+spalten.map(function(s){return '<th>'+s[0]+'</th>';}).join('')+'</tr>'+
        zeilen.map(function(z){return '<tr>'+spalten.map(function(s){return '<td style="white-space:nowrap">'+s[1](z)+'</td>';}).join('')+'</tr>';}).join('')+'</table></div>';
    }
    h+=tab('Gekauft', d.gekauft, [['Viertelstunde',function(z){return zeit(z.von);}],['Bezug',function(z){return f(z.bezug_kwh,3)+' kWh';}],
      ['vom Nachbarn',function(z){return f(z.p2p_kwh,3)+' kWh';}],['über Tarif',function(z){return f(z.tarif_kwh,3)+' kWh';}],
      ['Energie',function(z){return f(z.energie_fnd,4)+' FND';}],['Netzentgelt',function(z){return f(z.netz_fnd,4)+' FND';}]]);
    h+=tab('Verkauft', d.verkauft, [['Viertelstunde',function(z){return zeit(z.von);}],['Einspeisung',function(z){return f(z.einspeisung_kwh,3)+' kWh';}],
      ['zugeteilt',function(z){var s=0;(z.posten||[]).forEach(function(x){s+=Number(x.kwh||0);});return f(s,3)+' kWh an '+(z.posten||[]).length;}],
      ['gehandelt',function(z){return z.sekundlich ? 'sekündlich' : 'je Viertelstunde, Knappheit '+f(z.knappheit*100,0)+' %';}],['Energiepreis',function(z){return f(z.preis,4)+' FND/kWh';}]]);
    // R844: Forderungen als Erzeuger – wer Posten älter als 2 h nicht bezahlt
    // hat, ist säumig und bekommt keine Zuteilungen, bis er nachzahlt.
    if(d.forderungen&&d.forderungen.length){
      h+=tab('Forderungen an Abnehmer', d.forderungen, [['Abnehmer',function(z){return esc(z.verbraucher);}],
        ['angenommen',function(z){return f(z.angenommen_fnd,4)+' FND';}],['bezahlt',function(z){return f(z.bezahlt_fnd,4)+' FND';}],
        ['Stand',function(z){return z.saeumig?'<b style="color:var(--red)">säumig</b> – '+f(z.ueberfaellig_fnd,4)+' FND überfällig':'in Ordnung';}]]);
    }
    b.innerHTML='<div id="markt-live" style="margin:0 0 12px;padding:10px 12px;border:1px solid var(--border);border-radius:10px"><span class="meta">Live: warte auf Daten …</span></div>'+h;
    live();
  }).catch(function(){});
})();
</script>
]==])

    ngx.print(string.format([[
<section class="info-box">
  <h2>%s</h2><p>%s</p>
</section>
]], t("energy.fee_title"), t("energy.fee_desc")))

    render.footer()
end
