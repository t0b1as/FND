-- pages/job_show.lua
-- Job-Detail mit Arbeitsvertrag-Formular und digitaler Signatur
local render = require "render"
local cjson  = require "cjson.safe"

return function(captures)
    local id = captures and captures[1] or ""
    ngx.header["Content-Type"] = "text/html"
    local t = render.header("job.title", "jobs")

    if id == "" then
        ngx.print("<p class='error'>" .. t("general.not_found") .. "</p>")
        render.footer(); return
    end

    local record, err = render.api_get("/v1/jobs/" .. id)
    if err or not record then
        ngx.print("<p class='error'>" .. t("general.not_found") .. ": " .. (err or id) .. "</p>")
        render.footer(); return
    end

    local data  = record.data or {}
    local title = tostring(data.title or "–")
    local desc  = tostring(data.description or data.text or "–")
    local jtype = tostring(data.type or "offer")
    local rate  = tostring(data.rate or "")
    local start = tostring(data.start_date or "")

    local typeBadge = jtype == "offer" and "badge-green" or "badge-blue"
    local typeLabel = jtype == "offer" and t("job.type_offer") or t("job.type_request")

    ngx.print(string.format([[
<div class="detail-card">
  <div class="detail-meta">
    <span class="badge %s">%s</span>
    <span class="meta">ID: %s</span>
  </div>
  <h2 style="font-size:1.25rem;font-weight:700;color:var(--text);text-transform:none;letter-spacing:0;margin:0 0 0.75rem">%s</h2>
  <p class="listing-desc">%s</p>
  <div class="detail-actions">
    <a href="/jobs" class="btn-secondary">&larr; %s</a>
  </div>
</div>

<!-- ============================================================
     Arbeitsvertrag
     ============================================================ -->
<div class="contract-section">
  <div class="contract-header">
    <h3>📋 %s</h3>
    <div style="display:flex;gap:8px">
      <button class="btn btn-outline" onclick="previewPDF()">📄 %s</button>
      <button class="btn btn-green"   onclick="signContract()">✍ %s</button>
    </div>
  </div>

  <div class="contract-body">
    <div id="contract-status-bar" style="display:none;margin-bottom:12px"></div>

    <!-- Klauseln -->
    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="display:flex;gap:8px;flex-wrap:wrap">
        <input id="c-employer-name" type="text" placeholder="]] .. t("job.partner_name") .. [[" value="" style="flex:1;min-width:120px">
        <input id="c-employer-email" type="email" placeholder="]] .. t("job.partner_email") .. [[" value="" style="flex:1;min-width:140px">
        <input id="c-employer" type="text" placeholder="0x… Wallet" value="" style="flex:1;min-width:120px">
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="display:flex;gap:8px;flex-wrap:wrap">
        <input id="c-contractor-name" type="text" placeholder="]] .. t("job.partner_name") .. [[" value="" style="flex:1;min-width:120px">
        <input id="c-contractor-email" type="email" placeholder="]] .. t("job.partner_email") .. [[" value="" style="flex:1;min-width:140px">
        <input id="c-contractor" type="text" placeholder="0x… Wallet" value="" style="flex:1;min-width:120px">
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="font-weight:600">%s</div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val">
        <textarea id="c-scope" rows="3" placeholder="]] .. t("job.scope_placeholder") .. [[">%s</textarea>
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="display:flex;gap:8px;align-items:center">
        <input id="c-rate" type="text"
               placeholder="z.B. 85" value="%s" style="width:100px">
        <span class="muted">FND /</span>
        <select id="c-rateunit" style="width:120px;padding:6px 10px;background:var(--surface-2);border:1px solid var(--border-2);border-radius:var(--radius);color:var(--text);outline:none">
          <option value="h">]] .. t("job.unit_hour") .. [[</option>
          <option value="d">]] .. t("job.unit_day") .. [[</option>
          <option value="w">]] .. t("job.unit_week") .. [[</option>
          <option value="m">]] .. t("job.unit_month") .. [[</option>
          <option value="fix">]] .. t("job.unit_fixed") .. [[</option>
        </select>
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="display:flex;gap:8px;flex-wrap:wrap;align-items:center">
        <input id="c-start" type="date" value="%s" style="flex:1;min-width:130px">
        <select id="c-term" onchange="onTermChange()" style="width:150px;padding:6px 10px;background:var(--surface-2);border:1px solid var(--border-2);border-radius:var(--radius);color:var(--text)">
          <option value="unlimited">]] .. t("job.term_unlimited") .. [[</option>
          <option value="fixed">]] .. t("job.term_fixed") .. [[</option>
        </select>
        <input id="c-end" type="date" style="flex:1;min-width:130px;display:none">
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val">
        <select id="c-payment" style="width:100%%;padding:6px 10px;background:var(--surface-2);border:1px solid var(--border-2);border-radius:var(--radius);color:var(--text);outline:none">
          <option value="14">14 Tage netto</option>
          <option value="30">30 Tage netto</option>
          <option value="7">7 Tage netto</option>
          <option value="0">]] .. t("job.start_immediate") .. [[</option>
        </select>
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val">
        <select id="c-notice" style="width:100%%;padding:6px 10px;background:var(--surface-2);border:1px solid var(--border-2);border-radius:var(--radius);color:var(--text);outline:none">
          <option value="jederzeit">]] .. t("job.start_anytime") .. [[</option>
          <option value="14d">14 Tage</option>
          <option value="4w">4 Wochen zum Monatsende</option>
          <option value="3m">3 Monate zum Quartalsende</option>
        </select>
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val">
        <input id="c-jurisdiction" type="text"
               placeholder="z.B. München" value="">
      </div>
    </div>

    <div class="clause">
      <span class="clause-key">%s</span>
      <div class="clause-val" style="color:var(--text-dim);font-size:13px">
        Deutsches Recht (BGB / HGB)
      </div>
    </div>

    <!-- AGB-Standardklauseln -->
    <details style="margin-top:12px">
      <summary style="font-size:12px;color:var(--muted);cursor:pointer;user-select:none">
        Standardklauseln (§§ BGB, Datenschutz, Haftung) anzeigen
      </summary>
      <div style="margin-top:10px;font-size:12px;color:var(--muted);line-height:1.7;display:flex;flex-direction:column;gap:8px">
        <p><strong style="color:var(--text-dim)">§ 1 Vertragsgegenstand.</strong>
        Der Auftragnehmer erbringt die im Leistungsumfang beschriebenen Leistungen selbstständig und weisungsfrei. Es besteht kein Arbeitsverhältnis im Sinne des § 611a BGB.</p>
        <p><strong style="color:var(--text-dim)">§ 2 Vergütung und Zahlung.</strong>
        Die Vergütung erfolgt in FND (Fundus Network Dollar) auf der Fundus-Chain. Die Zahlung wird nach Abnahme der Leistung über den Fundus-Escrow-Mechanismus (14-Tage-Einfrierung) abgewickelt.</p>
        <p><strong style="color:var(--text-dim)">§ 3 Geheimhaltung.</strong>
        Beide Parteien verpflichten sich zur Verschwiegenheit über vertrauliche Informationen der jeweils anderen Partei für die Dauer von 3 Jahren nach Vertragsende.</p>
        <p><strong style="color:var(--text-dim)">§ 4 Haftung.</strong>
        Die Haftung des Auftragnehmers ist auf Vorsatz und grobe Fahrlässigkeit beschränkt. Die Gesamthaftung ist auf die Vertragssumme begrenzt.</p>
        <p><strong style="color:var(--text-dim)">§ 5 Urheberrecht.</strong>
        Alle im Rahmen dieses Vertrags erstellten Werke werden nach vollständiger Zahlung als Eigentum des Auftraggebers übertragen, soweit nicht anders vereinbart.</p>
        <p><strong style="color:var(--text-dim)">§ 6 Blockchain-Nachweis.</strong>
        Die digitale Signatur beider Parteien wird als Hash auf der Fundus-Chain verankert und gilt als rechtsverbindliche Unterzeichnung gemäß eIDAS-Verordnung (Art. 26).</p>
      </div>
    </details>
  </div>
</div>

<!-- ============================================================
     Signaturen
     ============================================================ -->
<div class="contract-section" style="margin-top:14px">
  <div class="contract-header">
    <h3>✍ Digitale Signaturen</h3>
    <span id="sig-status-badge" class="badge badge-amber">]] .. t("job.sig_waiting") .. [[</span>
  </div>
  <div class="contract-body">
    <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px">

      <div class="sig-card" id="sig-employer-card">
        <div class="sig-status-row">
          <span style="font-size:13px;font-weight:600;color:var(--text-dim)">%s</span>
          <span id="sig-employer-badge" class="badge badge-amber" style="margin-left:auto">]] .. t("job.sig_pending") .. [[</span>
        </div>
        <div class="sig-addr" id="sig-employer-addr">–</div>
        <div class="sig-hash" id="sig-employer-hash"></div>
        <button class="btn btn-green" style="margin-top:10px;font-size:12px" onclick="signAs('employer')">
          Jetzt signieren
        </button>
      </div>

      <div class="sig-card" id="sig-contractor-card">
        <div class="sig-status-row">
          <span style="font-size:13px;font-weight:600;color:var(--text-dim)">%s</span>
          <span id="sig-contractor-badge" class="badge badge-amber" style="margin-left:auto">]] .. t("job.sig_pending") .. [[</span>
        </div>
        <div class="sig-addr" id="sig-contractor-addr">–</div>
        <div class="sig-hash" id="sig-contractor-hash"></div>
        <button class="btn btn-green" style="margin-top:10px;font-size:12px" onclick="signAs('contractor')">
          Jetzt signieren
        </button>
      </div>

    </div>
    <div id="sig-final" style="display:none;margin-top:12px" class="success-box">
      ✓ Arbeitsvertrag beidseitig signiert und auf der Fundus-Chain verankert.
      <a href="#" onclick="previewPDF()" style="margin-left:8px;color:var(--green)">📄 Vertrag als PDF</a>
    </div>
  </div>
</div>

<script>
const JT = ]] .. (require("cjson.safe").encode({
  signed = t("job.signed"), fully_signed = t("job.fully_signed"), awaiting_counter = t("job.awaiting_counter"),
  contact_seller = t("listing.contact_seller"),
  sign_login_email = t("job.sign_login_email"), sign_login_pass = t("job.sign_login_pass"),
  sign_login_failed = t("job.sign_login_failed"), sign_failed = t("job.sign_failed"),
}) or "{}") .. [[
// Kontakt-Schlüssel des Inserenten (falls eingebettet) → "Anschreiben"-Button.
const JOB_CONTACT = ]] .. (require("cjson.safe").encode({
  pub_key = (type(data.contact_pub_key) == "string") and data.contact_pub_key or "",
  fundus_id = (type(data.contact_fundus_id) == "string") and data.contact_fundus_id or "",
}) or "{}") .. [[;
(function addContactButton() {
  if (!JOB_CONTACT.pub_key) return;
  const actions = document.querySelector('.detail-actions');
  if (!actions) return;
  const b = document.createElement('button');
  b.className = 'btn btn-outline';
  b.textContent = '💬 ' + (JT.contact_seller || 'Anschreiben');
  b.onclick = function () {
    const u = new URLSearchParams();
    if (JOB_CONTACT.fundus_id) u.set('to', JOB_CONTACT.fundus_id);
    u.set('key', JOB_CONTACT.pub_key);
    window.location.href = '/messenger?' + u.toString();
  };
  actions.appendChild(b);
})();
// ──────────────────────────────────────────────────────────
//  Vertrags-Hash berechnen
// ──────────────────────────────────────────────────────────
function getContractPayload() {
  const term = document.getElementById('c-term').value;
  return JSON.stringify({
    job_id:      '%s',
    title:       '%s',
    employer_name:   document.getElementById('c-employer-name').value.trim(),
    employer:        document.getElementById('c-employer').value.trim(),
    contractor_name: document.getElementById('c-contractor-name').value.trim(),
    contractor:      document.getElementById('c-contractor').value.trim(),
    scope:       document.getElementById('c-scope').value.trim(),
    rate:        document.getElementById('c-rate').value.trim(),
    rate_unit:   document.getElementById('c-rateunit').value,
    start:       document.getElementById('c-start').value,
    term:        term,
    end:         (term === 'fixed') ? document.getElementById('c-end').value : '',
    payment:     document.getElementById('c-payment').value,
    notice:      document.getElementById('c-notice').value,
    jurisdiction:document.getElementById('c-jurisdiction').value.trim(),
    law:         'DE-BGB',
    timestamp:   new Date().toISOString(),
  });
}

// Enddatum nur zeigen, wenn "befristet" gewählt ist.
function onTermChange() {
  const term = document.getElementById('c-term').value;
  document.getElementById('c-end').style.display = (term === 'fixed') ? '' : 'none';
}

async function sha256hex(str) {
  const enc = new TextEncoder().encode(str);
  const buf = await crypto.subtle.digest('SHA-256', enc);
  return Array.from(new Uint8Array(buf)).map(b => b.toString(16).padStart(2,'0')).join('');
}

// ──────────────────────────────────────────────────────────
//  Signieren
// ──────────────────────────────────────────────────────────
const signatures = {};

async function signAs(role) {
  const payload = getContractPayload();
  const hash    = await sha256hex(payload);

  // WICHTIG: Für jede Signatur die Identität FRISCH ableiten — NICHT eine
  // bestehende Session wiederverwenden. Bei einem Zwei-Parteien-Vertrag signieren
  // beide am selben Browser; würde man die Session wiederverwenden, würde die
  // zweite Partei fälschlich mit der Wallet der ersten signieren. Jede Signatur
  // verlangt daher die eigenen Zugangsdaten.
  const roleLabel = (role === 'employer') ? (JT.party_employer || 'Arbeitgeber') : (JT.party_contractor || 'Auftragnehmer');
  // E-Mail aus dem Formularfeld der jeweiligen Partei (oben bei den Namen).
  const email = document.getElementById('c-' + role + '-email').value.trim();
  if (!email) { alert(JT.sign_need_email || 'Bitte zuerst die E-Mail des ' + roleLabel + ' oben eintragen.'); return; }
  // Passwort weiterhin per Prompt — es soll NICHT im Klartext im Formular stehen.
  const pass = prompt((JT.sign_login_pass || 'Passwort') + ' (' + roleLabel + '):');
  if (!pass) return;

  let wallet = '0x…';
  let sig = '';
  try {
    // 1. Identität frisch ableiten (legt Session mit DIESEM Schlüssel an).
    const dr = await fetch('/api/v1/identity/derive', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, password: pass })
    });
    if (!dr.ok) { alert(JT.sign_login_failed || 'Anmeldung fehlgeschlagen.'); return; }
    const dd = await dr.json();
    wallet = dd.wallet_address || dd.fundus_id || wallet;
    const fundusId = dd.fundus_id;

    // 2. Mit der angemeldeten Identität signieren (Session-Cookie).
    const r = await fetch('/api/v1/identity/sign', {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: hash }),
    });
    if (r.ok) { const d = await r.json(); sig = d.signature || ''; if (d.wallet_address) wallet = d.wallet_address; }
  } catch(e) { alert('Fehler: ' + e.message); return; }

  if (!sig) {
    alert(JT.sign_failed || 'Signatur fehlgeschlagen.');
    return;
  }

  // Schutz: nicht zweimal dieselbe Wallet für beide Rollen zulassen.
  const otherRole = (role === 'employer') ? 'contractor' : 'employer';
  if (signatures[otherRole] && signatures[otherRole].wallet === wallet) {
    alert(JT.sign_same_wallet || 'Beide Parteien dürfen nicht dieselbe Wallet nutzen.');
    return;
  }

  signatures[role] = { wallet, hash, sig, ts: new Date().toISOString() };

  // UI aktualisieren
  const addrEl  = document.getElementById('sig-' + role + '-addr');
  const hashEl  = document.getElementById('sig-' + role + '-hash');
  const badgeEl = document.getElementById('sig-' + role + '-badge');

  addrEl.textContent  = wallet;
  hashEl.textContent  = 'SHA-256: ' + hash.slice(0,32) + '…';
  badgeEl.textContent = JT.signed;
  badgeEl.className   = 'badge badge-green';

  // On-Chain anchoring
  await fetch('/api/v1/jobs/' + '%s' + '/sign', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ role, wallet, hash, signature: sig }),
  }).catch(() => {});

  checkAllSigned();
}

// Knopf "Unterzeichnen" oben: springt zu den Signaturen (die Rolle – Auftrag-
// geber oder Auftragnehmer – wählt man dort). Die Funktion fehlte bisher, der
// Knopf tat nichts.
function signContract() {
  const card = document.getElementById('sig-employer-card');
  const sec = card ? card.closest('.contract-section') || card : null;
  if (!sec) return;
  sec.scrollIntoView({ behavior: 'smooth', block: 'start' });
  sec.style.transition = 'box-shadow .3s';
  sec.style.boxShadow = '0 0 0 3px var(--green, #22c55e)';
  setTimeout(function(){ sec.style.boxShadow = ''; }, 1600);
}

function checkAllSigned() {
  if (signatures.employer && signatures.contractor) {
    document.getElementById('sig-status-badge').textContent = JT.fully_signed;
    document.getElementById('sig-status-badge').className   = 'badge badge-green';
    document.getElementById('sig-final').style.display = 'block';
  } else if (signatures.employer || signatures.contractor) {
    document.getElementById('sig-status-badge').textContent = JT.awaiting_counter;
    document.getElementById('sig-status-badge').className   = 'badge badge-amber';
  }
}

// ──────────────────────────────────────────────────────────
//  PDF öffnen
// ──────────────────────────────────────────────────────────
function previewPDF() {
  // Vertrag clientseitig aus den aktuellen Formulardaten rendern und als
  // druckbare Seite öffnen (kein Backend-Endpunkt nötig → kein 404). Der Nutzer
  // kann im Druckdialog "Als PDF speichern" wählen.
  const p = JSON.parse(getContractPayload());
  const unit = { h:'Stunde', d:'Tag', w:'Woche', m:'Monat', fix:'Pauschal' }[p.rate_unit] || p.rate_unit;
  const termText = (p.term === 'fixed' && p.end)
      ? ('befristet bis ' + p.end)
      : 'unbefristet';
  const esc = s => (s||'').replace(/[<>&]/g, c => ({'<':'&lt;','>':'&gt;','&':'&amp;'}[c]));
  const sigRow = (role, label) => {
    const sg = signatures[role];
    if (!sg) return '<div style="margin-top:8px"><b>' + label + ':</b> <i>noch nicht signiert</i></div>';
    const name = role === 'employer' ? p.employer_name : p.contractor_name;
    return '<div style="margin-top:8px"><b>' + label + ':</b> ' + esc(name || '') +
           '<br><span style="font-size:11px;color:#555">Wallet: ' + esc(sg.wallet) +
           '<br>Signatur: ' + esc(sg.sig.slice(0,40)) + '…' +
           '<br>Zeitpunkt: ' + esc(sg.ts) + '</span></div>';
  };
  const html = '<!DOCTYPE html><html><head><meta charset="utf-8"><title>Arbeitsvertrag</title>' +
    '<style>body{font-family:Georgia,serif;max-width:720px;margin:40px auto;padding:0 20px;line-height:1.6;color:#111}' +
    'h1{font-size:22px;border-bottom:2px solid #111;padding-bottom:8px}h2{font-size:15px;margin-top:22px}' +
    '.row{margin:6px 0}.k{display:inline-block;width:160px;color:#555}@media print{body{margin:0}}</style></head><body>' +
    '<h1>Arbeitsvertrag</h1>' +
    '<div class="row"><span class="k">Tätigkeit:</span>' + esc(p.title) + '</div>' +
    '<h2>§1 Vertragsparteien</h2>' +
    '<div class="row"><span class="k">Arbeitgeber:</span>' + esc(p.employer_name || '—') + ' (' + esc(p.employer || '—') + ')</div>' +
    '<div class="row"><span class="k">Auftragnehmer:</span>' + esc(p.contractor_name || '—') + ' (' + esc(p.contractor || '—') + ')</div>' +
    '<h2>§2 Tätigkeit &amp; Umfang</h2><div class="row">' + esc(p.scope || '—') + '</div>' +
    '<h2>§3 Vergütung</h2><div class="row">' + esc(p.rate) + ' FND / ' + esc(unit) + '</div>' +
    '<div class="row"><span class="k">Zahlung:</span>' + esc(p.payment || '—') + '</div>' +
    '<h2>§4 Laufzeit</h2>' +
    '<div class="row"><span class="k">Beginn:</span>' + esc(p.start || '—') + '</div>' +
    '<div class="row"><span class="k">Laufzeit:</span>' + esc(termText) + '</div>' +
    '<div class="row"><span class="k">Kündigungsfrist:</span>' + esc(p.notice || '—') + '</div>' +
    '<h2>§5 Rechtliches</h2>' +
    '<div class="row"><span class="k">Gerichtsstand:</span>' + esc(p.jurisdiction || '—') + '</div>' +
    '<div class="row"><span class="k">Anwendbares Recht:</span>Deutsches Recht (BGB)</div>' +
    '<h2>§6 Digitale Signaturen</h2>' + sigRow('employer','Arbeitgeber') + sigRow('contractor','Auftragnehmer') +
    '<p style="margin-top:30px;font-size:11px;color:#777">Digital signiert über die Fundus-Chain (Ed25519). ' +
    'Die Signatur-Hashes verankern den Vertragstext unveränderlich.</p>' +
    '<script>window.onload=function(){window.print()}</scr' + 'ipt></body></html>';
  const w = window.open('', '_blank');
  w.document.write(html);
  w.document.close();
}

// ──────────────────────────────────────────────────────────
//  Eigene Adresse vorab eintragen
// ──────────────────────────────────────────────────────────
window.addEventListener('DOMContentLoaded', async () => {
  try {
    const d = await window.fundusMe();
    if (d) {
      if (d.wallet_address) {
        // Anzeigen welche Felder vorab ausgefüllt werden
        document.getElementById('sig-employer-addr').textContent = d.wallet_address + ' (du)';
      }
    }
  } catch(e) {}
});
</script>
]],
    typeBadge, typeLabel, record.id,
    title,
    desc:gsub("[<>&\"']", {["<"]="&lt;",[">"]="&gt;",["&"]="&amp;",['"']="&quot;",["'"]="&#39;"}),
    t("general.back"),

    -- Vertragsfelder
    t("job.contract"),
    t("job.contract_pdf"),
    t("job.contract_sign"),

    t("job.party_employer"),
    t("job.party_contractor"),
    t("job.col_title"), title,
    t("job.scope"),
    desc:gsub("[<>&\"']", {["<"]="&lt;",[">"]="&gt;",["&"]="&amp;",['"']="&quot;",["'"]="&#39;"}),
    t("job.rate"), rate,
    t("job.start_date"), start,
    t("job.payment_terms"),
    t("job.notice_period"),
    t("job.jurisdiction"),
    t("job.governing_law"),

    -- Signatur-Karten
    t("job.party_employer"),
    t("job.party_contractor"),

    -- JS-Werte
    record.id, title,
    record.id,
    record.id
    ))

    render.footer()
end
