// Leichter, robuster Rich-Text-Editor. Ein contenteditable-Div mit einfachen
// Formatier-Buttons (Fett/Kursiv/Unterstrichen/Liste). Der Inhalt wird als
// bereinigtes HTML ins zugehörige versteckte <textarea> synchronisiert, das
// dann normal mit dem Formular gesendet und SERVERSEITIG sanitized wird.
(function(){
  const ALLOWED = new Set(["B","STRONG","I","EM","U","P","BR","UL","OL","LI","SPAN","DIV"]);

  // Reinigt einen HTML-Baum: entfernt nicht erlaubte Tags/Attribute (behält Text).
  function clean(node) {
    const kids = Array.from(node.childNodes);
    for (const c of kids) {
      if (c.nodeType === 1) {
        if (!ALLOWED.has(c.tagName)) {
          // Tag auflösen: Kinder an die Stelle heben
          while (c.firstChild) node.insertBefore(c.firstChild, c);
          node.removeChild(c);
          continue;
        }
        // Alle Attribute außer style entfernen; style auf sichere Props begrenzen
        for (const attr of Array.from(c.attributes)) {
          if (attr.name !== "style") c.removeAttribute(attr.name);
        }
        if (c.getAttribute("style")) {
          const safe = c.getAttribute("style").split(";").filter(d => {
            const p = d.split(":")[0].trim().toLowerCase();
            return ["font-weight","font-style","text-decoration"].includes(p);
          }).join(";");
          if (safe) c.setAttribute("style", safe); else c.removeAttribute("style");
        }
        clean(c);
      } else if (c.nodeType !== 3) {
        node.removeChild(c); // Kommentare etc. weg
      }
    }
  }

  function sync(editor, textarea) {
    const tmp = editor.cloneNode(true);
    clean(tmp);
    textarea.value = tmp.innerHTML;
  }

  window.initRichEditor = function(targetId) {
    const textarea = document.getElementById(targetId);
    const editor = document.getElementById(targetId + "-edit");
    if (!textarea || !editor) return;

    // Vorhandenen Inhalt (aus %s im HTML) übernehmen ist schon im DOM.
    // Toolbar-Buttons verdrahten.
    const toolbar = editor.previousElementSibling;
    if (toolbar && toolbar.classList.contains("rt-toolbar")) {
      toolbar.querySelectorAll(".rt-btn").forEach(btn => {
        btn.addEventListener("mousedown", e => {
          e.preventDefault(); // Fokus im Editor halten
          document.execCommand(btn.dataset.cmd, false, null);
          sync(editor, textarea);
        });
      });
    }

    // Bei jeder Änderung synchronisieren.
    editor.addEventListener("input", () => sync(editor, textarea));
    editor.addEventListener("blur", () => sync(editor, textarea));

    // Beim Einfügen: nur Klartext übernehmen (kein Word-Markup-Chaos).
    editor.addEventListener("paste", e => {
      e.preventDefault();
      const text = (e.clipboardData || window.clipboardData).getData("text/plain");
      document.execCommand("insertText", false, text);
    });

    sync(editor, textarea); // Initial-Sync
  };
})();
