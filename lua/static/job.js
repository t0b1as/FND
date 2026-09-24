// job.js – Fundus Job Board
const S   = (typeof FUNDUS_STRINGS !== "undefined") ? FUNDUS_STRINGS : {};
const str = (k, p) => { let s = S[k] || k; if (p) Object.keys(p).forEach(key => s = s.replace(`%{${key}}`, p[key])); return s; };

document.getElementById("j-desc")?.addEventListener("input", onJobDescChange);

function onJobDescChange() {
    const hasContent = (document.getElementById("j-title")?.value.trim().length > 0) ||
                       (document.getElementById("j-desc")?.value.trim().length > 0);
    const btn = document.getElementById("job-analyze-btn");
    if (btn) btn.disabled = !hasContent;
}

async function polishJobText() {
    const btn      = document.getElementById("job-analyze-btn");
    const statusEl = document.getElementById("analysis-status");
    const statusTx = document.getElementById("analysis-status-text");
    if (btn) btn.disabled = true;
    if (statusEl) statusEl.style.display = "flex";
    if (statusTx) statusTx.textContent = str("analyzing");

    const payload = {
        type:        document.getElementById("j-type")?.value  || "offer",
        title:       document.getElementById("j-title")?.value || "",
        description: document.getElementById("j-desc")?.value  || "",
        language:    S.lang || "de",
    };

    try {
        // Job-Analyse nutzt denselben /analyze-Endpoint mit leerem Bild-Array
        // und übermittelt den Text als voice_text
        const fd = new FormData();
        fd.append("voice_text", `${payload.type}: ${payload.title}. ${payload.description}`);
        fd.append("language",   payload.language);

        const resp = await fetch("/api/v1/analyze", { method: "POST", body: fd });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const result = await resp.json();

        if (result.listing_text) {
            document.getElementById("j-desc").value  = result.listing_text;
        }
        if (result.keywords?.length) {
            document.getElementById("j-keywords").value = result.keywords.join(", ");
        }

        if (statusEl) statusEl.style.display = "none";
    } catch (err) {
        if (statusTx) { statusTx.textContent = str("analyze_error", { reason: err.message }); statusTx.className = "error"; }
    } finally {
        if (btn) btn.disabled = false;
    }
}

async function submitJob(event) {
    event.preventDefault();
    const submitBtn    = document.getElementById("submit-btn");
    const submitStatus = document.getElementById("submit-status");
    if (submitBtn) submitBtn.disabled = true;
    if (submitStatus) submitStatus.textContent = str("publishing");

    const payload = {
        title:       document.getElementById("j-title")?.value || "",
        type:        document.getElementById("j-type")?.value  || "offer",
        description: document.getElementById("j-desc")?.value  || "",
        keywords:    (document.getElementById("j-keywords")?.value || "")
                        .split(",").map(s => s.trim()).filter(Boolean),
    };

    // Eigenen Messenger-Kontaktschlüssel einbetten (falls aktiv), damit
    // Interessenten den Inserenten direkt verschlüsselt anschreiben können.
    try {
        const who = await fetch("/api/v1/messenger/whoami").then(r => r.json());
        if (who && who.active && who.public_key) {
            payload.contact_pub_key = who.public_key;
            payload.contact_fundus_id = who.fundus_id;
        }
    } catch (e) { /* ohne Kontaktschlüssel inserieren ist ok */ }

    try {
        const resp = await fetch("/api/v1/jobs", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
        const job = await resp.json();
        if (submitStatus) submitStatus.innerHTML = `<span class="success">${str("published")}</span>`;
        setTimeout(() => { window.location.href = "/jobs/" + job.id; }, 1500);
    } catch (err) {
        if (submitStatus) submitStatus.innerHTML = `<span class="error">${err.message}</span>`;
        if (submitBtn) submitBtn.disabled = false;
    }
}
