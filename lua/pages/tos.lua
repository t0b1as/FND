-- =============================================================================
--  pages/tos.lua – Nutzungsbedingungen / Terms of Service
--
--  Wird beim ersten Aufruf angezeigt.
--  Akzeptanz wird als Cookie gespeichert (fundus_tos=v1).
--  Kein JavaScript erforderlich – funktioniert mit reinem POST-Formular.
-- =============================================================================

local i18n = require "i18n"

local TOS_VERSION = "v1"
local TOS_COOKIE  = "fundus_tos"

-- Exportierbare Hilfsfunktion: prüft ob TOS akzeptiert wurde
local M = {}

M.VERSION = TOS_VERSION
M.COOKIE  = TOS_COOKIE

-- Gibt true zurück wenn der aktuelle Request einen gültigen TOS-Cookie hat.
function M.accepted()
    local cookie_str = ngx.var.http_cookie or ""
    for pair in cookie_str:gmatch("[^;]+") do
        local k, v = pair:match("^%s*([^=]+)=(.+)$")
        if k and k:gsub("%s","") == TOS_COOKIE then
            return v:gsub("%s","") == TOS_VERSION
        end
    end
    return false
end

-- Rendert die TOS-Seite.
function M.render()
    local t, lang = i18n.init()

    ngx.header["Content-Type"] = "text/html"
    ngx.print(string.format([[
<!DOCTYPE html>
<html lang="%s">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s – Fundus</title>
  <link rel="stylesheet" href="/static/style.css?v=1780465208">
  <style>
    .tos-wrap { max-width: 760px; margin: 3rem auto; padding: 0 1.5rem; }
    .tos-card { background: var(--surface); border: 1px solid var(--border);
                border-radius: var(--radius); padding: 2rem 2.5rem; }
    .tos-logo { font-size: 1.5rem; font-weight: 600; margin-bottom: 0.25rem; }
    .tos-version { font-size: 12px; color: var(--muted); margin-bottom: 2rem; }
    .tos-section { margin: 1.5rem 0; }
    .tos-section h2 { font-size: 15px; font-weight: 500; margin: 0 0 0.5rem; }
    .tos-section p, .tos-section li { font-size: 14px; line-height: 1.7;
                                       color: var(--muted); }
    .tos-section ul { padding-left: 1.25rem; margin: 0.5rem 0; }
    .tos-section li { margin: 0.25rem 0; }
    .tos-highlight { border-left: 3px solid var(--warning);
                     background: rgba(240,180,0,0.08);
                     border-radius: 0 var(--r) var(--r) 0;
                     padding: 0.75rem 1rem; margin: 1.5rem 0; }
    .tos-highlight p { color: var(--warning); font-size: 14px; margin: 0; }
    .tos-actions { margin-top: 2rem; padding-top: 1.5rem;
                   border-top: 1px solid var(--brd2);
                   display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
    .tos-decline { font-size: 13px; color: var(--muted);
                   text-decoration: none; }
    .tos-decline:hover { color: var(--txt); }
    .lang-note { font-size: 12px; color: var(--dim); margin-top: 1rem; }
  </style>
</head>
<body>
<div class="tos-wrap">
  <div class="tos-card">
    <div class="tos-logo">Fundus Marketplace</div>
    <div class="tos-version">%s · %s</div>
]], lang, t("tos.title"), t("tos.version_label") .. " " .. TOS_VERSION,
    t("tos.effective_date")))

    -- Haupttext je nach Sprache
    if lang == "en" then
        M._render_en(t)
    else
        M._render_de(t)
    end

    -- Akzeptieren-Formular
    local back = ngx.var.arg_back or "/"
    ngx.print(string.format([[

    <div class="tos-highlight">
      <p>%s</p>
    </div>

    <div class="tos-actions">
      <form method="POST" action="/tos/accept" style="display:inline">
        <input type="hidden" name="back" value="%s">
        <button type="submit" class="btn">%s</button>
      </form>
      <a href="about:blank" class="tos-decline">%s</a>
    </div>

    <p class="lang-note">%s</p>
  </div>
</div>
</body>
</html>
]], t("tos.accept_notice"), ngx.escape_uri(back),
    t("tos.btn_accept"), t("tos.btn_decline"),
    t("tos.language_note")))
end

-- =============================================================================
--  Deutscher Volltext
-- =============================================================================
function M._render_de(t)
    ngx.print([[
    <div class="tos-section">
      <h2>1. Geltungsbereich und Betreiber</h2>
      <p>Diese Nutzungsbedingungen gelten für die Nutzung des Fundus Marketplace Node
      (nachfolgend „Plattform"). Die Plattform ist dezentral organisiert: jeder Nutzer
      betreibt seinen eigenen Node und ist damit gleichzeitig Nutzer und Betreiber seines
      Knotens.</p>
    </div>

    <div class="tos-section">
      <h2>2. Verantwortlichkeit des Nutzers</h2>
      <p>Du trägst die <strong>alleinige und vollständige Verantwortung</strong> für alle
      Inhalte, Angebote, Dienstleistungen, Zertifikate, Token und sonstigen Informationen,
      die du über diesen Node veröffentlichst, anbietest oder verbreitest. Dies umfasst
      insbesondere:</p>
      <ul>
        <li>Die <strong>Richtigkeit und Vollständigkeit</strong> aller Angaben</li>
        <li>Die <strong>Rechtmäßigkeit</strong> aller angebotenen Waren, Dienstleistungen
            und Inhalte</li>
        <li>Die Einhaltung aller anwendbaren <strong>gesetzlichen Vorschriften</strong>,
            einschließlich Steuer-, Handels-, Verbraucherschutz-, Datenschutz- und
            Strafrecht</li>
        <li>Die Einhaltung von <strong>Rechten Dritter</strong> (Urheber-, Marken-,
            Persönlichkeitsrechte)</li>
        <li>Die Einhaltung der für dich geltenden <strong>länderspezifischen
            Rechtsvorschriften</strong></li>
      </ul>
    </div>

    <div class="tos-section">
      <h2>3. Verbotene Inhalte und Handlungen</h2>
      <p>Es ist ausdrücklich untersagt, über diesen Node Folgendes anzubieten,
      zu verbreiten oder zu ermöglichen:</p>
      <ul>
        <li>Illegale Waren oder Dienstleistungen jeder Art</li>
        <li>Inhalte die Minderjährige sexuell darstellen oder gefährden
            (<em>absolutes Verbot</em>)</li>
        <li>Inhalte die zu Hass, Gewalt oder Diskriminierung aufrufen</li>
        <li>Betrug, Täuschung oder irreführende Angaben</li>
        <li>Urheberrechtlich geschützte Inhalte ohne entsprechende Berechtigung</li>
        <li>Daten oder persönliche Informationen Dritter ohne deren ausdrückliche
            Einwilligung</li>
        <li>Inhalte oder Handlungen die gegen das Recht des Landes verstoßen,
            in dem du lebst oder tätig bist</li>
      </ul>
    </div>

    <div class="tos-section">
      <h2>4. Partner-Modul und persönliche Inhalte</h2>
      <p>Die Nutzung des Partner-Moduls setzt voraus, dass alle beteiligten Personen
      <strong>volljährig</strong> (mindestens 18 Jahre alt) sind. Du bestätigst, dass du
      selbst volljährig bist und keine Inhalte veröffentlichst, die Minderjährige
      ansprechen oder für sie zugänglich machen. Alle veröffentlichten Inhalte im
      Partner-Modul müssen auf einvernehmlichen Interaktionen zwischen Erwachsenen
      basieren.</p>
    </div>

    <div class="tos-section">
      <h2>5. Systemgebühren</h2>
      <p>Für die Nutzung der Plattform werden folgende Gebühren
      <strong>automatisch</strong> vom abgewickelten Umsatz einbehalten:</p>
      <ul>
        <li><strong>1,8 % des Transaktionsumsatzes</strong> bei jedem abgeschlossenen
            Kauf, Verkauf oder Dienstleistungsauftrag über den Marktplatz</li>
        <li><strong>1,00 € pro hergestelltem Partnerkontakt</strong> im Partner-Modul,
            sobald ein beidseitig bestätigter Kontakt zustande kommt</li>
      </ul>
      <p>Die Gebühren werden direkt im Zahlungsfluss abgezogen und an den
      Systembetreiber weitergeleitet. Durch die Nutzung der Plattform stimmst du
      dieser automatischen Gebührenerhebung ausdrücklich zu. Alle angegebenen Preise
      in deinen Angeboten sollten die Systemgebühr bereits berücksichtigen.</p>
    </div>

    <div class="tos-section">
      <h2>6. Energie-Token und Finanzdienstleistungen</h2>
      <p>Der Handel mit Energie-Token und Zertifikaten kann in deinem Land
      regulatorischen Anforderungen unterliegen (z.&nbsp;B. Energierecht, Finanzmarktrecht,
      Geldwäschevorschriften). Es liegt in deiner <strong>alleinigen Verantwortung</strong>,
      die für dich geltenden Vorschriften zu kennen und einzuhalten.</p>
    </div>

    <div class="tos-section">
      <h2>7. Dezentrale Architektur und Haftungsausschluss</h2>
      <p>Fundus ist eine dezentrale Plattform ohne zentrale Kontrollinstanz. Es gibt
      keinen zentralen Betreiber der Inhalte prüft, moderiert oder für sie haftet.
      <strong>Du bist allein verantwortlich</strong> für alle Handlungen die über
      deinen Node erfolgen. Die Plattform stellt lediglich technische Infrastruktur
      bereit.</p>
    </div>

    <div class="tos-section">
      <h2>8. Datenschutz</h2>
      <p>Alle persönlichen Profilangaben (insbesondere im Partner-Modul) werden
      ausschließlich lokal auf deinem Node gespeichert und nur in anonymisierter,
      gehashter Form an das Netzwerk übermittelt. Du bist selbst dafür verantwortlich,
      die Datenschutzrechte Dritter zu wahren, deren Daten du verarbeitest.</p>
    </div>

    <div class="tos-section">
      <h2>9. Änderungen der Nutzungsbedingungen</h2>
      <p>Diese Bedingungen können aktualisiert werden. Bei einer wesentlichen Änderung
      wird beim nächsten Aufruf erneut um Zustimmung gebeten. Die fortgesetzte Nutzung
      nach Akzeptanz der neuen Version gilt als Zustimmung.</p>
    </div>

    <div class="tos-section">
      <h2>10. Anwendbares Recht</h2>
      <p>Da Fundus dezentral betrieben wird, richtet sich die Rechtslage nach dem
      Recht des Landes, in dem du deinen Node betreibst und Inhalte veröffentlichst.
      Du bist verpflichtet, die dort geltenden Gesetze einzuhalten.</p>
    </div>
]])
end

-- =============================================================================
--  English full text
-- =============================================================================
function M._render_en(t)
    ngx.print([[
    <div class="tos-section">
      <h2>1. Scope and Operator</h2>
      <p>These Terms of Service govern the use of the Fundus Marketplace Node
      (hereinafter "Platform"). The Platform is decentralised: each user operates their
      own node and is therefore simultaneously a user and the operator of their node.</p>
    </div>

    <div class="tos-section">
      <h2>2. User Responsibility</h2>
      <p>You bear <strong>sole and full responsibility</strong> for all content, listings,
      services, certificates, tokens and other information you publish, offer or distribute
      through this node. This includes in particular:</p>
      <ul>
        <li>The <strong>accuracy and completeness</strong> of all information provided</li>
        <li>The <strong>lawfulness</strong> of all goods, services and content offered</li>
        <li>Compliance with all applicable <strong>laws and regulations</strong>, including
            tax, commercial, consumer protection, data protection and criminal law</li>
        <li>Respect for <strong>third-party rights</strong> (copyright, trademarks,
            personality rights)</li>
        <li>Compliance with the <strong>country-specific legal requirements</strong>
            applicable to you</li>
      </ul>
    </div>

    <div class="tos-section">
      <h2>3. Prohibited Content and Actions</h2>
      <p>It is strictly prohibited to offer, distribute or facilitate the following
      through this node:</p>
      <ul>
        <li>Illegal goods or services of any kind</li>
        <li>Content that sexually depicts or endangers minors
            (<em>absolute prohibition</em>)</li>
        <li>Content inciting hatred, violence or discrimination</li>
        <li>Fraud, deception or misleading statements</li>
        <li>Copyrighted content without appropriate authorisation</li>
        <li>Data or personal information of third parties without their explicit consent</li>
        <li>Any content or action violating the law of the country where you live
            or operate</li>
      </ul>
    </div>

    <div class="tos-section">
      <h2>4. Partner Module and Personal Content</h2>
      <p>Use of the Partner Module requires that all persons involved are
      <strong>adults</strong> (at least 18 years of age). You confirm that you yourself
      are of legal age and do not publish content that targets or is accessible to
      minors. All content published in the Partner Module must be based on consensual
      interactions between adults.</p>
    </div>

    <div class="tos-section">
      <h2>5. System Fees</h2>
      <p>The following fees are <strong>automatically</strong> deducted from
      transactions processed through the Platform:</p>
      <ul>
        <li><strong>1.8% of the transaction value</strong> for each completed
            purchase, sale or service order through the marketplace</li>
        <li><strong>€1.00 per partner contact established</strong> in the Partner
            Module, once a mutually confirmed contact is made</li>
      </ul>
      <p>Fees are deducted directly from the payment flow and forwarded to the
      system operator. By using the Platform you expressly consent to this automatic
      fee collection. All prices in your listings should already account for the
      system fee.</p>
    </div>

    <div class="tos-section">
      <h2>6. Energy Tokens and Financial Services</h2>
      <p>Trading in energy tokens and certificates may be subject to regulatory
      requirements in your country (e.g. energy law, financial market law, anti-money
      laundering regulations). It is your <strong>sole responsibility</strong> to be
      aware of and comply with the regulations applicable to you.</p>
    </div>

    <div class="tos-section">
      <h2>7. Decentralised Architecture and Disclaimer</h2>
      <p>Fundus is a decentralised platform without a central controlling authority.
      There is no central operator who checks, moderates or is liable for content.
      <strong>You alone are responsible</strong> for all actions carried out through
      your node. The Platform provides technical infrastructure only.</p>
    </div>

    <div class="tos-section">
      <h2>8. Data Protection</h2>
      <p>All personal profile data (particularly in the Partner Module) is stored
      exclusively locally on your node and only transmitted to the network in anonymised,
      hashed form. You are responsible for respecting the data protection rights of
      third parties whose data you process.</p>
    </div>

    <div class="tos-section">
      <h2>9. Changes to the Terms of Service</h2>
      <p>These terms may be updated. In the event of a material change, your consent
      will be requested again on the next visit. Continued use after accepting the new
      version constitutes agreement.</p>
    </div>

    <div class="tos-section">
      <h2>10. Applicable Law</h2>
      <p>As Fundus is operated in a decentralised manner, the applicable law is
      determined by the law of the country in which you operate your node and publish
      content. You are obliged to comply with the laws in force there.</p>
    </div>
]])
end

return M
