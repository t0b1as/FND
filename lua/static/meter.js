// =============================================================================
//  meter.js – Smartmeter Live-Anzeige via Server-Sent Events
//  Verbindet sich mit GET /api/v1/meter/stream (SSE)
//  Strings kommen aus METER_STRINGS (von energy.lua injiziert)
// =============================================================================

const MS = (typeof METER_STRINGS !== "undefined") ? METER_STRINGS : {};

const elKwh  = document.getElementById("live-kwh");
const elWatt = document.getElementById("live-watt");
const elTs   = document.getElementById("live-ts");
const badge  = document.querySelector(".live-badge");

// Letzter bekannter Zustand für einfache Trendanzeige
let lastWatt = null;

function connectSSE() {
    const es = new EventSource("/api/v1/meter/stream");

    es.onopen = () => {
        if (badge) { badge.textContent = "LIVE"; badge.classList.remove("stale"); }
    };

    es.onmessage = (event) => {
        let data;
        try { data = JSON.parse(event.data); } catch { return; }

        if (elKwh)  elKwh.textContent  = typeof data.kwh  === "number" ? data.kwh.toFixed(4)  + " kWh" : "–";
        if (elWatt) {
            const w = typeof data.watt === "number" ? data.watt : null;
            if (w !== null) {
                const arrow = lastWatt === null ? "" : w > lastWatt ? " ↑" : w < lastWatt ? " ↓" : "";
                elWatt.textContent = w.toFixed(1) + " W" + arrow;
                lastWatt = w;
            }
        }
        if (elTs && data.timestamp) {
            // ISO-String in lokale Zeit umwandeln
            try {
                elTs.textContent = new Date(data.timestamp).toLocaleTimeString(
                    MS.lang || "de",
                    { hour: "2-digit", minute: "2-digit", second: "2-digit" }
                );
            } catch { elTs.textContent = data.timestamp; }
        }

        // Kurze Aufblend-Animation bei jedem neuen Wert
        [elKwh, elWatt, elTs].forEach(el => {
            if (!el) return;
            el.classList.remove("flash");
            void el.offsetWidth; // reflow
            el.classList.add("flash");
        });
    };

    es.onerror = () => {
        if (badge) { badge.textContent = "OFFLINE"; badge.classList.add("stale"); }
        es.close();
        // Reconnect nach 5 s
        setTimeout(connectSSE, 5000);
    };
}

connectSSE();
