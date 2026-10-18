// Glühendes Knotennetz im Seitenhintergrund (R676)
//
// Das Bild passt zum Wesen der Sache: Fundus IST ein Netz aus Knoten, die sich
// miteinander verbinden. Der Hintergrund zeigt genau das.
//
// Sparsamkeit war hier die Hauptanforderung, nicht die Optik:
//   * Rücksicht auf die Einstellung "Bewegung reduzieren" – wer sie gesetzt
//     hat, bekommt ein ruhendes Bild statt gar keines.
//   * 30 Bilder je Sekunde statt 60. Bei dieser Trägheit sieht man keinen
//     Unterschied, es halbiert aber die Rechenlast.
//   * Ruht vollständig, sobald der Reiter im Hintergrund ist. Sonst rechnet
//     ein Handy in der Tasche stundenlang an einem unsichtbaren Bild.
//   * Knotenzahl richtet sich nach der Fläche und nach der Zahl der Kerne;
//     auf schwachen Geräten weniger.
//   * Das Leuchten entsteht über schatten statt über Weichzeichner-Filter –
//     letztere sind auf dem Handy um ein Vielfaches teurer.
(function () {
    'use strict';

    if (document.getElementById('bg-netz')) return;

    const leise = window.matchMedia &&
        window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    const leinwand = document.createElement('canvas');
    leinwand.id = 'bg-netz';
    document.body.appendChild(leinwand);
    const ctx = leinwand.getContext('2d', { alpha: true });
    if (!ctx) { leinwand.remove(); return; }

    let B = 0, H = 0, dpr = 1;
    let knoten = [];
    let laeuft = true, letzte = 0;

    const GRUEN = [0, 230, 118];
    const ABSTAND = 215;        // bis hierhin werden Knoten verbunden

    function anzahl() {
        const flaeche = window.innerWidth * window.innerHeight;
        let n = Math.round(flaeche / 15000);   // R676: dichter – vorher sah man kaum Verbindungen
        // Auf schwachen Geräten deutlich weniger: Die Zahl der Verbindungen
        // wächst quadratisch mit den Knoten, das ist der teure Teil.
        const kerne = navigator.hardwareConcurrency || 4;
        if (kerne <= 4) n = Math.round(n * 0.75);
        if (window.innerWidth < 700) n = Math.round(n * 0.7);
        return Math.max(26, Math.min(110, n));
    }

    function aufbauen() {
        dpr = Math.min(window.devicePixelRatio || 1, 2);  // mehr bringt nichts
        B = window.innerWidth;
        H = window.innerHeight;
        leinwand.width = Math.round(B * dpr);
        leinwand.height = Math.round(H * dpr);
        leinwand.style.width = B + 'px';
        leinwand.style.height = H + 'px';
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

        const n = anzahl();
        knoten = [];
        for (let i = 0; i < n; i++) {
            knoten.push({
                x: Math.random() * B,
                y: Math.random() * H,
                vx: (Math.random() - 0.5) * 0.22,   // sehr langsam: es soll wabern
                vy: (Math.random() - 0.5) * 0.22,
                r: 1.9 + Math.random() * 2.1,
                phase: Math.random() * Math.PI * 2,
                takt: 0.6 + Math.random() * 0.8,
            });
        }
    }

    function zeichnen(t) {
        const sek = t / 1000;
        ctx.clearRect(0, 0, B, H);

        // Verbindungen zuerst, damit die Knoten darüber liegen.
        ctx.lineWidth = 1;
        for (let i = 0; i < knoten.length; i++) {
            const a = knoten[i];
            for (let j = i + 1; j < knoten.length; j++) {
                const b = knoten[j];
                const dx = a.x - b.x, dy = a.y - b.y;
                const d2 = dx * dx + dy * dy;
                if (d2 > ABSTAND * ABSTAND) continue;
                const d = Math.sqrt(d2);
                // Je näher, desto heller – und ein langsames Pulsieren darüber.
                const nah = 1 - d / ABSTAND;
                const puls = 0.75 + 0.25 * Math.sin(sek * 0.7 + (i + j) * 0.35);
                ctx.strokeStyle = 'rgba(' + GRUEN[0] + ',' + GRUEN[1] + ',' + GRUEN[2] +
                                  ',' + (nah * nah * 0.40 * puls).toFixed(3) + ')';
                ctx.beginPath();
                ctx.moveTo(a.x, a.y);
                ctx.lineTo(b.x, b.y);
                ctx.stroke();
            }
        }

        // Knoten mit Schein. shadowBlur ist hier billiger als ein Filter.
        ctx.shadowColor = 'rgba(0,230,118,0.9)';
        for (const k of knoten) {
            const puls = 0.65 + 0.35 * Math.sin(sek * k.takt + k.phase);
            ctx.shadowBlur = 13 * puls;
            ctx.fillStyle = 'rgba(0,230,118,' + (0.50 + 0.40 * puls).toFixed(3) + ')';
            ctx.beginPath();
            ctx.arc(k.x, k.y, k.r * (0.85 + 0.3 * puls), 0, Math.PI * 2);
            ctx.fill();
        }
        ctx.shadowBlur = 0;
    }

    function bewegen() {
        for (const k of knoten) {
            k.x += k.vx;
            k.y += k.vy;
            // An den Rändern umkehren statt durchlaufen: So bleibt die
            // Verteilung gleichmäßig, ohne dass Knoten neu gewürfelt werden.
            if (k.x < -20) { k.x = -20; k.vx = -k.vx; }
            if (k.x > B + 20) { k.x = B + 20; k.vx = -k.vx; }
            if (k.y < -20) { k.y = -20; k.vy = -k.vy; }
            if (k.y > H + 20) { k.y = H + 20; k.vy = -k.vy; }
        }
    }

    function schleife(t) {
        if (!laeuft) return;
        // Auf 30 Bilder je Sekunde begrenzen.
        if (t - letzte >= 33) {
            letzte = t;
            bewegen();
            zeichnen(t);
        }
        requestAnimationFrame(schleife);
    }

    function anhalten() {
        laeuft = false;
    }
    function fortsetzen() {
        if (laeuft || leise) return;
        laeuft = true;
        letzte = 0;
        requestAnimationFrame(schleife);
    }

    aufbauen();

    if (leise) {
        // Ruhendes Bild: einmal zeichnen, nicht bewegen.
        zeichnen(0);
    } else {
        requestAnimationFrame(schleife);
    }

    document.addEventListener('visibilitychange', () => {
        if (document.hidden) anhalten(); else fortsetzen();
    });

    let neuTimer = null;
    window.addEventListener('resize', () => {
        clearTimeout(neuTimer);
        neuTimer = setTimeout(() => {
            aufbauen();
            if (leise) zeichnen(0);
        }, 200);
    });
})();
