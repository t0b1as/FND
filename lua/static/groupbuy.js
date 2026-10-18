// Sammelbestellungen (R696)
'use strict';

function gbTab(welcher) {
    document.querySelectorAll('.gb-tab').forEach(function (s) { s.style.display = 'none'; });
    document.querySelectorAll('#gb-tabs .tab-btn').forEach(function (b) { b.classList.remove('active'); });
    const s = document.getElementById('gb-' + welcher);
    if (s) s.style.display = '';
    const i = (welcher === 'offen') ? 0 : 1;
    const b = document.querySelectorAll('#gb-tabs .tab-btn')[i];
    if (b) b.classList.add('active');
    if (welcher === 'offen') gbLaden();
}

function esc(t) {
    return String(t == null ? '' : t).replace(/[&<>"]/g, function (c) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
}

function zahl(n) {
    const v = parseFloat(n);
    if (!isFinite(v)) return '0';
    return v.toLocaleString('de-DE', { maximumFractionDigits: 2 });
}

function datum(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    return isNaN(d) ? '' : d.toLocaleDateString('de-DE');
}

// Vorschläge laden – die Ware ist frei, die Liste erleichtert nur den Einstieg.
(async function gbMeta() {
    try {
        const r = await fetch('/api/v1/groupbuy/meta', { credentials: 'same-origin' });
        if (!r.ok) return;
        const d = await r.json();
        const fuelle = function (id, werte) {
            const dl = document.getElementById(id);
            if (!dl || !Array.isArray(werte)) return;
            dl.innerHTML = werte.map(function (w) {
                return '<option value="' + esc(w) + '">';
            }).join('');
        };
        fuelle('gb-waren', d.waren);
        fuelle('gb-einheiten', d.einheiten);
    } catch (e) {}
})();

async function gbLaden() {
    const box = document.getElementById('gb-liste');
    if (!box) return;
    try {
        const r = await fetch('/api/v1/groupbuy', { credentials: 'same-origin' });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        const liste = d.bestellungen || [];
        if (!liste.length) {
            box.innerHTML = '<div class="empty-hint">Noch keine Sammelbestellungen. ' +
                            'Leg die erste an – im Zweifel findet sich jemand.</div>';
            return;
        }
        box.innerHTML = liste.map(gbKarte).join('');
    } catch (e) {
        box.innerHTML = '<div class="empty-hint">✗ ' + esc(e.message) + '</div>';
    }
}

function gbKarte(g) {
    const summe = parseFloat(g.summe || 0);
    const min = parseFloat(g.mindest_menge || 0);
    const anteil = min > 0 ? Math.min(100, summe / min * 100) : 0;
    const zahlart = { escrow: 'hinterlegt bis zur Lieferung',
                      ueberweisung: 'Überweisung an den Organisator',
                      bar: 'bar bei Lieferung' }[g.zahlungsart] || g.zahlungsart;
    const stand = { offen: 'offen', bestellt: 'beim Händler bestellt',
                    geliefert: 'geliefert', abgesagt: 'abgesagt' }[g.status] || g.status;

    let knoepfe = '';
    const meiner = (g.teilnehmer || []).find(function (t) { return t.ich; });
    if (g.status === 'offen' && !meiner) {
        knoepfe += '<button class="btn" onclick="gbMitmachen(\'' + esc(g.id) + '\',\'' +
                   esc(g.einheit) + '\',\'' + esc(g.zahlungsart) + '\',' +
                   (g.preis_pro_einheit || 0) + ',\'' + esc(g.org_wallet || '') +
                   '\')">Mitbestellen</button>';
    }
    if (g.status === 'offen' && meiner) {
        // R697: Austritt UND Rückforderung in einem Schritt. Getrennt wäre es
        // eine Falle: Wer zuerst austritt, verschwindet aus der Liste und käme
        // über diese Seite nicht mehr an seinen hinterlegten Betrag.
        knoepfe += '<button class="btn-sm" onclick="gbAustreten(\'' + esc(g.id) +
                   '\',\'' + esc(meiner.escrow_id || '') + '\')">Doch nicht</button>';
    }
    if (g.mein && g.status === 'offen' && g.erreicht) {
        knoepfe += '<button class="btn btn-primary" onclick="gbAktion(\'' + esc(g.id) +
                   '\',\'ordered\')">Beim Händler bestellt</button>';
    }
    if (g.mein && g.status === 'bestellt') {
        knoepfe += '<button class="btn btn-primary" onclick="gbAktion(\'' + esc(g.id) +
                   '\',\'delivered\')">Geliefert</button>';
    }
    if (g.mein && g.status !== 'geliefert' && g.status !== 'abgesagt') {
        knoepfe += '<button class="btn-sm" onclick="gbAktion(\'' + esc(g.id) +
                   '\',\'cancel\')">Absagen</button>';
    }
    // R697: Freigabe und Storno der EIGENEN Hinterlegung.
    if (meiner && meiner.escrow_id) {
        if (g.status === 'geliefert') {
            knoepfe += '<button class="btn btn-primary" onclick="gbFreigeben(\'' +
                       esc(meiner.escrow_id) + '\',' + (meiner.bezahlt_fnd || 0) +
                       ')">Erhalten &amp; freigeben</button>';
        } else if (g.status === 'abgesagt') {
            // NUR bei Absage. Ist beim Händler bestellt, bleibt der Betrag
            // gebunden – sonst könnte jemand aussteigen, nachdem der
            // Organisator in seinem Namen zugesagt hat. Genau dagegen ist die
            // Hinterlegung gedacht. Bleibt die Lieferung ganz aus, greift die
            // Frist der Hinterlegung auf der Chain: danach kann der Teilnehmer
            // sein Geld auch ohne Zustimmung zurückholen.
            knoepfe += '<button class="btn-sm" onclick="gbStorno(\'' +
                       esc(meiner.escrow_id) + '\')">Betrag zurückfordern</button>';
        }
    }

    const leute = (g.teilnehmer || []).map(function (t) {
        return '<li>' + esc(t.name || t.fid.slice(0, 10) + '…') + ' – ' +
               zahl(t.menge) + ' ' + esc(g.einheit) + '</li>';
    }).join('');

    return '<div class="gb-karte' + (g.mein ? ' gb-mein' : '') + '">' +
      '<div class="gb-kopf"><strong>' + esc(g.titel) + '</strong>' +
        '<span class="gb-stand gb-' + esc(g.status) + '">' + esc(stand) + '</span></div>' +
      '<div class="meta">' + esc(g.ware) +
        (g.ort ? ' · ' + esc(g.plz || '') + ' ' + esc(g.ort) : '') +
        (g.haendler ? ' · Händler: ' + esc(g.haendler) : '') + '</div>' +
      '<div class="gb-balken" title="' + zahl(summe) + ' von ' + zahl(min) + '">' +
        '<div class="gb-fort" style="width:' + anteil.toFixed(1) + '%"></div></div>' +
      '<div class="meta">' + zahl(summe) + ' von ' + zahl(min) + ' ' + esc(g.einheit) +
        (g.erreicht ? ' – Mindestmenge erreicht' : '') +
        (g.preis_pro_einheit ? ' · ' + zahl(g.preis_pro_einheit) + ' FND je ' + esc(g.einheit) : '') +
        '</div>' +
      '<div class="meta">Annahmeschluss ' + datum(g.frist) +
        (g.liefertermin ? ' · Lieferung ' + esc(g.liefertermin) : '') +
        ' · Zahlung: ' + esc(zahlart) + '</div>' +
      (g.beschreibung ? '<div class="gb-text">' + esc(g.beschreibung) + '</div>' : '') +
      (leute ? '<details class="gb-leute"><summary>' + (g.teilnehmer || []).length +
               ' Teilnehmer</summary><ul>' + leute + '</ul></details>' : '') +
      '<div class="gb-knoepfe">' + knoepfe + '</div>' +
      '</div>';
}

async function gbMitmachen(id, einheit, zahlungsart, preis, orgWallet) {
    const m = prompt('Wie viel ' + einheit + ' möchtest du abnehmen?');
    if (m === null) return;
    const menge = parseFloat(String(m).replace(',', '.'));
    if (!isFinite(menge) || menge <= 0) {
        alert('Bitte eine Menge größer null angeben.');
        return;
    }

    // R697: Ohne Hinterlegung einfach eintragen.
    if (zahlungsart !== 'escrow') {
        await gbAktion(id, 'join', { menge: menge });
        return;
    }

    const betrag = menge * parseFloat(preis || 0);
    if (!isFinite(betrag) || betrag <= 0) {
        alert('Der zu hinterlegende Betrag lässt sich nicht berechnen.');
        return;
    }
    if (!orgWallet) {
        alert('Für diese Bestellung fehlt die Wallet-Adresse des Organisators.');
        return;
    }
    if (!confirm('Du nimmst ' + zahl(menge) + ' ' + einheit + ' ab.\n\n' +
                 zahl(betrag) + ' FND werden hinterlegt. Der Organisator kommt erst ' +
                 'daran, wenn DU die Lieferung bestätigst.\n\nFortfahren?')) return;

    // Die Hinterlegung unterschreibt der Teilnehmer mit seinen EIGENEN Wörtern.
    // Der Organisator kann sie dadurch weder anlegen noch freigeben.
    const seed = prompt('Gib deine Wallet-Wörter ein, um ' + zahl(betrag) +
                        ' FND zu hinterlegen:');
    if (!seed) return;
    const words = seed.trim().split(/\s+/);
    if (words.length < 12) { alert('Zu wenige Wörter.'); return; }

    try {
        const r = await fetch('/api/v1/escrow', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                listing_id: id,          // Sammelbestellung als Bezug
                seller: orgWallet,       // Organisator als Empfänger
                words: words,
                amount_fnd: String(betrag),
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        const eid = d.escrow_id || d.id;
        if (!eid) throw new Error('Keine Kennung der Hinterlegung erhalten.');
        // Erst jetzt eintragen – sonst stünde man in der Liste, ohne hinterlegt
        // zu haben, und der Organisator würde auf Geld warten, das nicht kommt.
        await gbAktion(id, 'join', { menge: menge, escrow_id: eid, bezahlt_fnd: betrag });
        alert('✓ ' + zahl(betrag) + ' FND hinterlegt. Du gibst sie nach der Lieferung frei.');
    } catch (e) {
        alert('✗ Hinterlegung fehlgeschlagen: ' + e.message +
              '\n\nDu wurdest NICHT eingetragen.');
    }
}

// Nach der Lieferung: Freigabe an den Organisator – nur der Teilnehmer selbst
// kann das, mit seinen eigenen Wörtern.
async function gbFreigeben(escrowId, betrag) {
    if (!confirm('Lieferung erhalten?\n\n' + zahl(betrag) +
                 ' FND werden an den Organisator freigegeben. Das lässt sich ' +
                 'nicht rückgängig machen.')) return;
    const seed = prompt('Gib deine Wallet-Wörter ein, um die Freigabe zu bestätigen:');
    if (!seed) return;
    const words = seed.trim().split(/\s+/);
    if (words.length < 12) { alert('Zu wenige Wörter.'); return; }
    try {
        const r = await fetch('/api/v1/escrow/' + encodeURIComponent(escrowId) + '/confirm', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ words: words }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        alert('✓ Freigegeben.');
        gbLaden();
    } catch (e) {
        alert('✗ ' + e.message);
    }
}

// Austritt oder Absage: Storno der eigenen Hinterlegung.
async function gbStorno(escrowId) {
    if (!confirm('Hinterlegten Betrag zurückfordern?')) return;
    const seed = prompt('Gib deine Wallet-Wörter ein:');
    if (!seed) return;
    const words = seed.trim().split(/\s+/);
    if (words.length < 12) { alert('Zu wenige Wörter.'); return; }
    try {
        const r = await fetch('/api/v1/escrow/' + encodeURIComponent(escrowId) + '/cancel', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ words: words }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        alert('✓ Storno beantragt.');
        gbLaden();
    } catch (e) {
        alert('✗ ' + e.message);
    }
}

async function gbAktion(id, aktion, daten) {
    try {
        const r = await fetch('/api/v1/groupbuy/' + encodeURIComponent(id) + '/' + aktion, {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(daten || {}),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        gbLaden();
    } catch (e) {
        alert('✗ ' + e.message);
    }
}

async function gbAnlegen(btn) {
    const st = document.getElementById('gb-neu-status');
    const w = function (id) { const e = document.getElementById(id); return e ? e.value.trim() : ''; };
    const n = function (id) { const v = parseFloat(w(id).replace(',', '.')); return isFinite(v) ? v : 0; };
    const frist = w('gb-frist');
    if (!frist) { st.textContent = '✗ Bitte einen Annahmeschluss angeben.'; return; }
    if (btn) btn.disabled = true;
    try {
        const r = await fetch('/api/v1/groupbuy', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                titel: w('gb-titel'), ware: w('gb-ware'), einheit: w('gb-einheit'),
                beschreibung: w('gb-text'), haendler: w('gb-haendler'),
                mindest_menge: n('gb-min'), max_menge: n('gb-max'),
                preis_pro_einheit: n('gb-preis'),
                zahlungsart: (document.getElementById('gb-zahlung') || {}).value || 'escrow',
                plz: w('gb-plz'), ort: w('gb-ort'),
                // Frist auf das Ende des gewählten Tages setzen – sonst liefe sie
                // um Mitternacht des Vortags ab, was niemand erwartet.
                frist: new Date(frist + 'T23:59:59').toISOString(),
                liefertermin: w('gb-liefer'),
            }),
        });
        const d = await r.json();
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        st.textContent = '✓ Angelegt. Jetzt kannst du selbst deine Menge eintragen.';
        ['gb-titel', 'gb-ware', 'gb-einheit', 'gb-min', 'gb-max', 'gb-preis',
         'gb-haendler', 'gb-plz', 'gb-ort', 'gb-liefer', 'gb-text'].forEach(function (id) {
            const e = document.getElementById(id); if (e) e.value = '';
        });
        setTimeout(function () { gbTab('offen'); }, 700);
    } catch (e) {
        st.textContent = '✗ ' + e.message;
    }
    if (btn) btn.disabled = false;
}

// Austreten: erst den hinterlegten Betrag zurückfordern, dann austragen.
// Die Reihenfolge ist wichtig – andersherum wäre der Eintrag weg und mit ihm
// der Weg zur Hinterlegung.
async function gbAustreten(id, escrowId) {
    if (escrowId) {
        if (!confirm('Du bist mit einem hinterlegten Betrag dabei.\n\n' +
                     'Zuerst wird der Betrag zurückgefordert, danach wirst du ' +
                     'ausgetragen. Fortfahren?')) return;
        const seed = prompt('Gib deine Wallet-Wörter ein, um den Betrag zurückzufordern:');
        if (!seed) return;
        const words = seed.trim().split(/\s+/);
        if (words.length < 12) { alert('Zu wenige Wörter.'); return; }
        try {
            const r = await fetch('/api/v1/escrow/' + encodeURIComponent(escrowId) + '/cancel', {
                method: 'POST', credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ words: words }),
            });
            const d = await r.json();
            if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        } catch (e) {
            alert('✗ Rückforderung fehlgeschlagen: ' + e.message +
                  '\n\nDu bleibst eingetragen – so bleibt der Weg zum Betrag offen.');
            return;
        }
    }
    await gbAktion(id, 'leave');
}

document.addEventListener('DOMContentLoaded', gbLaden);