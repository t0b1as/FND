-- Sammelbestellungen (R696): gemeinsam bestellen, um den Mengenpreis zu
-- bekommen. Der Händler braucht keine Wallet – die Hinterlegung läuft zwischen
-- Teilnehmern und Organisator.
local render = require "render"

return function()
    ngx.header["Content-Type"] = "text/html"
    render.header("Sammelbestellung", "groupbuy")

    ngx.print([[
<div class="top-bar"><h2 style="margin:0;font-size:20px;color:var(--text)">Sammelbestellung</h2></div>

<div class="tab-nav" id="gb-tabs">
  <button class="tab-btn active" onclick="gbTab('offen')">Offene Bestellungen</button>
  <button class="tab-btn" onclick="gbTab('neu')">Neue anlegen</button>
</div>

<section id="gb-offen" class="gb-tab">
  <div class="set-card">
    <div class="set-head"><h3>Offene Bestellungen</h3>
      <button class="btn-sm" aria-label="Neu laden" onclick="gbLaden()">&#8635;</button></div>
    <div class="set-body"><div id="gb-liste"><div class="empty-hint">Lade …</div></div></div>
  </div>
</section>

<section id="gb-neu" class="gb-tab" style="display:none">
  <div class="set-card">
    <div class="set-head"><h3>Neue Sammelbestellung</h3></div>
    <div class="set-body">
      <p class="meta" style="color:#ffc107">Bei <strong>Hinterlegung</strong> wird ein
        Preis je Einheit gebraucht – daraus ergibt sich der Betrag, den jeder
        Teilnehmer hinterlegt. Freigeben kann ihn nur der Teilnehmer selbst,
        nach der Lieferung. Du als Organisator kommst vorher nicht daran.</p>
      <p class="meta">Der Händler braucht kein Fundus-Konto. Du bestellst bei ihm
        wie immer; Fundus organisiert nur, wer wie viel abnimmt – und hält auf
        Wunsch das Geld, bis geliefert ist.</p>
      <div class="attr-raster">
        <label><span class="attr-name">Titel</span>
          <input type="text" id="gb-titel" placeholder="z.B. Heizöl Sammelbestellung Dorf"></label>
        <label><span class="attr-name">Ware</span>
          <input type="text" id="gb-ware" list="gb-waren" placeholder="z.B. Heizöl"></label>
        <datalist id="gb-waren"></datalist>
        <label><span class="attr-name">Einheit</span>
          <input type="text" id="gb-einheit" list="gb-einheiten" placeholder="z.B. Liter"></label>
        <datalist id="gb-einheiten"></datalist>
        <label><span class="attr-name">Mindestmenge</span>
          <input type="number" step="any" id="gb-min" placeholder="ab wann es sich lohnt"></label>
        <label><span class="attr-name">Höchstmenge <span class="attr-einheit">(freiwillig)</span></span>
          <input type="number" step="any" id="gb-max"></label>
        <label><span class="attr-name">Preis je Einheit <span class="attr-einheit">(FND, geschätzt)</span></span>
          <input type="number" step="any" id="gb-preis"></label>
        <label><span class="attr-name">Händler <span class="attr-einheit">(freiwillig)</span></span>
          <input type="text" id="gb-haendler" placeholder="Name oder Telefon"></label>
        <label><span class="attr-name">PLZ</span><input type="text" id="gb-plz"></label>
        <label><span class="attr-name">Ort</span><input type="text" id="gb-ort"></label>
        <label><span class="attr-name">Annahmeschluss</span>
          <input type="date" id="gb-frist"></label>
        <label><span class="attr-name">Liefertermin <span class="attr-einheit">(frei)</span></span>
          <input type="text" id="gb-liefer" placeholder="z.B. KW 48"></label>
        <label><span class="attr-name">Zahlungsart</span>
          <select id="gb-zahlung">
            <option value="escrow">Hinterlegt bis zur Lieferung (empfohlen)</option>
            <!-- R697: Bei Hinterlegung braucht es einen Preis je Einheit,
                 sonst steht der zu hinterlegende Betrag nicht fest. -->
            <option value="ueberweisung">Überweisung an den Organisator</option>
            <option value="bar">Bar bei Lieferung</option>
          </select></label>
      </div>
      <label style="margin-top:10px"><span class="attr-name">Beschreibung</span>
        <textarea id="gb-text" rows="3" placeholder="Qualität, Abholung, Aufteilung der Lieferkosten …"></textarea></label>
      <div style="margin-top:12px">
        <button class="btn btn-primary" onclick="gbAnlegen(this)">Bestellung anlegen</button>
      </div>
      <div id="gb-neu-status" class="meta" style="margin-top:8px"></div>
    </div>
  </div>
</section>
]])

    ngx.print([[<script src="/static/groupbuy.js?v=]] .. require("render").rev() .. [["></script>]])

    render.footer()
end
