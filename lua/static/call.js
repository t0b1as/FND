// Anrufe auf JEDER Seite (R692)
//
// Bisher steckte die gesamte Anrufmechanik in messenger.js und lief damit nur,
// solange die Messenger-Seite offen war. Ich habe drei Releases lang versucht,
// das über Benachrichtigungen zu lösen – das war der falsche Weg: Eine Meldung
// sagt einem nur, DASS jemand anruft. Annehmen konnte man ihn trotzdem nur im
// Messenger.
//
// Dieses Modul bringt die Mechanik selbst mit: Es nimmt eingehende Anrufe
// entgegen, baut sein Anrufbild bei Bedarf selbst auf und führt das Gespräch.
// Geladen wird es auf jeder Seite.
//
// Auf der Messenger-Seite hält es sich heraus – die hat ihre eigene, erprobte
// Umsetzung. Zwei Umsetzungen sind eine Altlast, die ich offen benenne: Sie
// gehören zusammengeführt, sobald diese hier sich bewährt hat. Mitten in der
// Fehlersuche die funktionierende Seite umzubauen wäre der falsche Moment.
(function () {
    'use strict';

    if (location.pathname.indexOf('/messenger') === 0) return;   // dort übernimmt messenger.js
    if (window.FundusCall) return;

    let pc = null, lokalerStrom = null, gegenueber = null;
    let offenesAngebot = null, eisPuffer = [], video = false;
    let eisServer = [{ urls: 'stun:stun.l.google.com:19302' }];
    let box = null, ton = null, vibro = null;

    function log(stufe, zusatz) {
        console.log('[Anruf] ' + stufe + (zusatz ? ': ' + zusatz : ''));
    }

    // ── Ton ───────────────────────────────────────────────────────────────
    // Eigener Klingelton statt des Umwegs über msgnotify: Der muss hier
    // jederzeit startbar und sofort abschaltbar sein.
    let tonCtx = null;
    (function tonVorbereiten() {
        const wecken = function () {
            try {
                const AC = window.AudioContext || window.webkitAudioContext;
                if (!AC) return;
                if (!tonCtx) tonCtx = new AC();
                if (tonCtx.state === 'suspended') tonCtx.resume().catch(function () {});
            } catch (e) {}
        };
        ['pointerdown', 'touchstart', 'keydown', 'click'].forEach(function (e) {
            document.addEventListener(e, wecken, { passive: true });
        });
    })();

    function klingelnAn(anrufer) {
        klingelnAus();
        const muster = function () {
            try {
                if (!tonCtx) {
                    const AC = window.AudioContext || window.webkitAudioContext;
                    if (AC) tonCtx = new AC();
                }
                if (!tonCtx) return;
                if (tonCtx.state === 'suspended') tonCtx.resume().catch(function () {});
                let t = tonCtx.currentTime;
                [[660, 0.35], [0, 0.12], [880, 0.35]].forEach(function (p) {
                    if (!p[0]) { t += p[1]; return; }
                    const o = tonCtx.createOscillator(), g = tonCtx.createGain();
                    o.type = 'sine'; o.frequency.value = p[0];
                    g.gain.setValueAtTime(0, t);
                    g.gain.linearRampToValueAtTime(anrufer ? 0.07 : 0.10, t + 0.02);
                    g.gain.setValueAtTime(anrufer ? 0.07 : 0.10, t + p[1] - 0.02);
                    g.gain.linearRampToValueAtTime(0, t + p[1]);
                    o.connect(g); g.connect(tonCtx.destination);
                    o.start(t); o.stop(t + p[1]);
                    t += p[1];
                });
            } catch (e) {}
        };
        muster();
        ton = setInterval(muster, anrufer ? 3100 : 1900);
        if (!anrufer && navigator.vibrate) {
            const v = function () { try { navigator.vibrate([400, 200, 400]); } catch (e) {} };
            v();
            vibro = setInterval(v, 2200);
        }
    }

    function klingelnAus() {
        if (ton) { clearInterval(ton); ton = null; }
        if (vibro) { clearInterval(vibro); vibro = null; }
        if (navigator.vibrate) { try { navigator.vibrate(0); } catch (e) {} }
    }

    // ── Oberfläche ────────────────────────────────────────────────────────
    function bildAufbauen() {
        if (box) return box;
        box = document.createElement('div');
        box.id = 'fnd-call';
        box.innerHTML =
            '<div class="fc-box">' +
            '  <div class="fc-wer"></div>' +
            '  <div class="fc-stand"></div>' +
            '  <div class="fc-videos">' +
            '    <video class="fc-remote" autoplay playsinline></video>' +
            '    <video class="fc-local" autoplay playsinline muted></video>' +
            '  </div>' +
            '  <div class="fc-knoepfe"></div>' +
            '</div>';
        document.body.appendChild(box);
        return box;
    }

    function knopf(txt, klasse, fn) {
        const b = document.createElement('button');
        b.textContent = txt; b.className = klasse || '';
        b.onclick = fn;
        return b;
    }

    function zeigen(wer, stand, knoepfe, mitVideo) {
        bildAufbauen();
        box.querySelector('.fc-wer').textContent = wer;
        box.querySelector('.fc-stand').textContent = stand;
        box.classList.toggle('mit-video', !!mitVideo);
        const k = box.querySelector('.fc-knoepfe');
        k.textContent = '';
        knoepfe.forEach(function (b) { k.appendChild(b); });
        box.style.display = 'flex';
    }

    function verstecken() {
        if (box) box.style.display = 'none';
    }

    // ── Signalweg ─────────────────────────────────────────────────────────
    async function signalSenden(signal) {
        if (!gegenueber) return;
        const r = await fetch('/api/v1/messenger/send', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                recipient_id: gegenueber.fundusID,
                recipient_pub_key: gegenueber.publicKey || '',
                signal: signal, type: 'signal',
            }),
        });
        if (!r.ok) {
            let grund = 'HTTP ' + r.status;
            try { const d = await r.json(); if (d.error) grund = d.error; } catch (e) {}
            throw new Error(grund);
        }
    }

    async function eisLaden() {
        try {
            const r = await fetch('/api/v1/messenger/ice', { credentials: 'same-origin' });
            if (!r.ok) return;
            const d = await r.json();
            if (Array.isArray(d.iceServers) && d.iceServers.length) eisServer = d.iceServers;
        } catch (e) {}
    }

    function verbindungAufbauen() {
        const v = new RTCPeerConnection({ iceServers: eisServer });
        if (lokalerStrom) lokalerStrom.getTracks().forEach(function (t) { v.addTrack(t, lokalerStrom); });
        v.ontrack = function (ev) {
            klingelnAus();
            const r = box && box.querySelector('.fc-remote');
            if (r && ev.streams && ev.streams[0]) r.srcObject = ev.streams[0];
            standSetzen('verbunden');
        };
        v.onicecandidate = function (ev) {
            if (ev.candidate) signalSenden({ type: 'ice', candidate: JSON.stringify(ev.candidate) }).catch(function () {});
        };
        v.onconnectionstatechange = function () {
            log('Verbindung', v.connectionState);
            if (v.connectionState === 'failed') beenden('Verbindung fehlgeschlagen');
        };
        return v;
    }

    function standSetzen(t) {
        if (box) box.querySelector('.fc-stand').textContent = t;
    }

    async function eisPufferLeeren() {
        const q = eisPuffer; eisPuffer = [];
        for (const c of q) {
            try { await pc.addIceCandidate(new RTCIceCandidate(c)); } catch (e) {}
        }
    }

    async function medien(mitVideo) {
        if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
            throw new Error('Dieser Browser gibt Mikrofon und Kamera nicht frei.');
        }
        return navigator.mediaDevices.getUserMedia({
            audio: true,
            video: mitVideo ? { width: { ideal: 640 }, height: { ideal: 480 } } : false,
        });
    }

    // ── Eingehender Anruf ─────────────────────────────────────────────────
    async function eingehend(signal, von) {
        if (pc) { log('bereits im Gespräch', ''); return; }
        if (!signal || !signal.sdp) return;
        video = (signal.type === 'call_video');
        gegenueber = { fundusID: von, publicKey: '' };
        try { offenesAngebot = JSON.parse(signal.sdp); } catch (e) { return; }
        log('Anruf empfangen – es klingelt', von);
        klingelnAn(false);
        zeigen((video ? '📹 ' : '📞 ') + 'Anruf von ' + String(von).slice(0, 10) + '…',
            'klingelt …',
            [knopf('Annehmen', 'fc-an', annehmen), knopf('Ablehnen', 'fc-ab', function () {
                signalSenden({ type: 'hangup' }).catch(function () {});
                beenden('abgelehnt');
            })], video);
        // Nach 45 s von selbst aufhören.
        setTimeout(function () { if (offenesAngebot) beenden('nicht angenommen'); }, 45000);
    }

    async function annehmen() {
        klingelnAus();
        standSetzen('verbinde …');
        try {
            await eisLaden();
            lokalerStrom = await medien(video);
            const lv = box.querySelector('.fc-local');
            if (lv) lv.srcObject = lokalerStrom;
            pc = verbindungAufbauen();
            await pc.setRemoteDescription(offenesAngebot);
            offenesAngebot = null;
            const antwort = await pc.createAnswer();
            await pc.setLocalDescription(antwort);
            await signalSenden({ type: 'answer', sdp: JSON.stringify(antwort) });
            await eisPufferLeeren();
            zeigen(box.querySelector('.fc-wer').textContent, 'verbunden',
                [knopf('Auflegen', 'fc-ab', function () {
                    signalSenden({ type: 'hangup' }).catch(function () {});
                    beenden('aufgelegt');
                })], video);
        } catch (e) {
            log('Annehmen fehlgeschlagen', e.message || '');
            standSetzen('✗ ' + (e.message || 'fehlgeschlagen'));
            setTimeout(function () { beenden('Fehler'); }, 2500);
        }
    }

    function beenden(grund) {
        log('beendet', grund || '');
        klingelnAus();
        offenesAngebot = null; eisPuffer = [];
        if (pc) { try { pc.close(); } catch (e) {} pc = null; }
        if (lokalerStrom) { lokalerStrom.getTracks().forEach(function (t) { t.stop(); }); lokalerStrom = null; }
        gegenueber = null;
        verstecken();
    }

    // Vom globalen Nachrichtenkanal (msgnotify.js) hereingereichte Signale.
    async function signalEmpfangen(signal, von) {
        if (!signal) return;
        switch (signal.type) {
            case 'call_audio':
            case 'call_video':
                await eingehend(signal, von);
                break;
            case 'answer':
                if (pc) {
                    try {
                        await pc.setRemoteDescription(JSON.parse(signal.sdp));
                        await eisPufferLeeren();
                    } catch (e) {}
                }
                break;
            case 'ice':
                try {
                    const c = JSON.parse(signal.candidate);
                    if (pc && pc.remoteDescription) await pc.addIceCandidate(new RTCIceCandidate(c));
                    else eisPuffer.push(c);
                } catch (e) {}
                break;
            case 'hangup':
                beenden('Gegenseite hat aufgelegt');
                break;
        }
    }

    window.FundusCall = {
        signal: signalEmpfangen,
        incoming: eingehend,
        hangup: beenden,
        laeuft: function () { return !!(pc || offenesAngebot); },
    };
    log('bereit', location.pathname);
})();
