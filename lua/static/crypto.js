// Client-seitige Verschlüsselung (End-to-End) mit dem Fundus-Krypto-Stack:
// XChaCha20-Poly1305 + Argon2id — via libsodium.js (window.sodium).
// Das Passwort verlässt den Browser NIE; Server/Storage-Node sehen nur
// Chiffretext. Selbstbeschreibendes Format — keine Backend-Krypto nötig:
//
//   [ magic "FNDENC" (6B) ][ version=2 (1B) ][ salt (16B) ][ nonce (24B) ][ XChaCha20-Poly1305 ciphertext ]
//
//   salt  = crypto_pwhash_SALTBYTES (16)  → Argon2id
//   nonce = crypto_aead_xchacha20poly1305_ietf_NPUBBYTES (24)
//   key   = 32 Byte (crypto_aead_..._KEYBYTES)

(function () {
  const MAGIC = "FNDENC";   // 6 Bytes
  const VERSION = 2;        // 2 = XChaCha20-Poly1305 + Argon2id

  function strBytes(s) { return new TextEncoder().encode(s); }

  async function ready() {
    if (!window.sodium) throw new Error("libsodium.js nicht geladen");
    await window.sodium.ready;
    return window.sodium;
  }

  // Argon2id-Schlüsselableitung (interaktive Parameter: schnell genug, ~64 MB)
  function deriveKey(s, password, salt) {
    return s.crypto_pwhash(
      s.crypto_aead_xchacha20poly1305_ietf_KEYBYTES, // 32
      password,
      salt,
      s.crypto_pwhash_OPSLIMIT_INTERACTIVE,
      s.crypto_pwhash_MEMLIMIT_INTERACTIVE,
      s.crypto_pwhash_ALG_ARGON2ID13
    );
  }

  // Verschlüsselt eine Datei/Blob → Blob mit Header (Ganzdatei im RAM).
  async function encryptBlob(file, password) {
    const s = await ready();
    const data = new Uint8Array(await file.arrayBuffer());
    const salt = s.randombytes_buf(s.crypto_pwhash_SALTBYTES);          // 16
    const nonce = s.randombytes_buf(s.crypto_aead_xchacha20poly1305_ietf_NPUBBYTES); // 24
    const key = deriveKey(s, password, salt);
    const ct = s.crypto_aead_xchacha20poly1305_ietf_encrypt(data, null, null, nonce, key);

    const magic = strBytes(MAGIC);
    const out = new Uint8Array(magic.length + 1 + salt.length + nonce.length + ct.length);
    let off = 0;
    out.set(magic, off); off += magic.length;
    out[off] = VERSION; off += 1;
    out.set(salt, off); off += salt.length;
    out.set(nonce, off); off += nonce.length;
    out.set(ct, off);
    return new Blob([out], { type: "application/octet-stream" });
  }

  // Prüft den Magic-Header.
  function isEncrypted(buf) {
    const magic = strBytes(MAGIC);
    if (buf.byteLength < magic.length) return false;
    const head = new Uint8Array(buf, 0, magic.length);
    for (let i = 0; i < magic.length; i++) if (head[i] !== magic[i]) return false;
    return true;
  }

  // Entschlüsselt einen ArrayBuffer mit Header → Uint8Array (Klartext).
  async function decryptBuffer(buf, password) {
    const s = await ready();
    const magic = strBytes(MAGIC);
    let off = magic.length + 1; // magic + version
    const salt = new Uint8Array(buf, off, s.crypto_pwhash_SALTBYTES); off += s.crypto_pwhash_SALTBYTES;
    const nlen = s.crypto_aead_xchacha20poly1305_ietf_NPUBBYTES;
    const nonce = new Uint8Array(buf, off, nlen); off += nlen;
    const ct = new Uint8Array(buf.slice(off));
    const key = deriveKey(s, password, salt);
    // wirft bei falschem Passwort/Manipulation (Poly1305-Tag)
    return s.crypto_aead_xchacha20poly1305_ietf_decrypt(null, ct, null, nonce, key);
  }

  window.FundusCrypto = { encryptBlob, isEncrypted, decryptBuffer, MAGIC, ready };
})();
