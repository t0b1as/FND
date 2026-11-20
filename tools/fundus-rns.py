#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
fundus-rns – Funkdienst für Fundus (R945).

Verbindet ein RNode-Funkmodul (z. B. Heltec WiFi LoRa 32 V3 mit RNode-
Firmware) über Reticulum mit Fundus. Nachrichten laufen als LXMF – damit
sind auch Sideband, MeshChat und Nomad Network erreichbar.

  * Funkeinstellungen: <data>/funk.json (schreibt Fundus, Admin → Funk)
  * Konten:            <data>/konten/<fid>.json – Schlüssel aus der Wallet
                       abgeleitet (schreibt Fundus beim Einschalten „Über Funk
                       erreichbar“). Bleiben hier, damit Nachrichten auch
                       ankommen, wenn niemand angemeldet ist.
  * Verlauf:           <data>/verlauf/<fid>.json
  * Schnittstelle:     HTTP über Unix-Socket <data>/api.sock (nur Fundus-Benutzer)

Jedes Konto bekommt einen eigenen LXMF-Router (LXMF erlaubt nur eine
Empfangsidentität pro Router); alle teilen dasselbe Reticulum.
"""

import argparse
import glob
import json
import os
import socket
import socketserver
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler
from urllib.parse import parse_qs, urlparse

VERSION = "R945"

STANDARD_FUNK = {
    "aktiv": True,
    "port": "auto",            # "auto" oder /dev/serial/by-id/…
    "frequenz": 869525000,     # 869,4–869,65 MHz: 10 % Sendezeit, bis 500 mW ERP
    "bandbreite": 250000,
    "sf": 9,
    "cr": 5,
    "sendeleistung": 14,       # dBm
    "airtime_lang": 9.0,       # % über 15 min – unter der 10-%-Grenze
    "airtime_kurz": 25.0,      # % über 15 s
    "tcp": "",                 # zusätzlich: "server:4242" oder "client:host:4242"
}

ARGS = None
LOCK = threading.RLock()
STATUS = {"start": time.time(), "geraet": None, "geraet_fehler": "", "rns": None}
RETICULUM = None
ROUTER = {}        # fid -> (router, destination)
GEHOERT = {}       # adresse -> {name, zeit, hops}
EREIGNISSE = []    # [{seq, …}]
SEQ = [0]
AUSGEHEND = {}     # lxm-hash -> (fid, eintrag)
NEU = threading.Condition(LOCK)


def log(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


def pfad(*teile):
    return os.path.join(ARGS.data, *teile)


def json_lesen(p, standard):
    try:
        with open(p, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return standard


def json_schreiben(p, daten, modus=0o600):
    os.makedirs(os.path.dirname(p), exist_ok=True)
    tmp = p + ".neu"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(daten, f, ensure_ascii=False)
    os.chmod(tmp, modus)
    os.replace(tmp, p)


def sichere_fid(fid):
    fid = (fid or "").strip().lower()
    if not fid or len(fid) > 100 or any(c not in "0123456789abcdefx" for c in fid):
        raise ValueError("ungültige Konto-Kennung")
    return fid


def sichere_adresse(a):
    a = (a or "").strip().lower().replace("<", "").replace(">", "").replace(":", "")
    if len(a) != 32 or any(c not in "0123456789abcdef" for c in a):
        raise ValueError("Funkadresse muss 32 Hex-Zeichen haben")
    return a


def ereignis(**e):
    with NEU:
        SEQ[0] += 1
        e["seq"] = SEQ[0]
        EREIGNISSE.append(e)
        del EREIGNISSE[:-500]
        NEU.notify_all()


# ── RNode finden ──────────────────────────────────────────────────────────────

def rnode_pruefen(port):
    """KISS-Erkennung: CMD_DETECT (0x08) mit 0x73 → Antwort 0x46."""
    try:
        import serial
    except ImportError:
        return False
    try:
        s = serial.Serial(port, 115200, timeout=0.2)
    except Exception:
        return False
    try:
        time.sleep(2.5)  # ESP32 startet beim Öffnen neu
        s.reset_input_buffer()
        s.write(bytes([0xC0, 0x08, 0x73, 0xC0]))
        ende = time.time() + 3
        puffer = b""
        while time.time() < ende:
            puffer += s.read(64)
            if bytes([0xC0, 0x08, 0x46]) in puffer:
                return True
        return False
    except Exception:
        return False
    finally:
        try:
            s.close()
        except Exception:
            pass


def rnode_suchen():
    kandidaten = sorted(glob.glob("/dev/serial/by-id/*"))
    bevorzugt = [k for k in kandidaten if any(x in k for x in ("CP210", "Silicon_Labs", "CH340", "CH9102", "Espressif", "JTAG", "RNode"))]
    for k in bevorzugt + [k for k in kandidaten if k not in bevorzugt]:
        if rnode_pruefen(k):
            return k
    for k in sorted(glob.glob("/dev/ttyUSB*") + glob.glob("/dev/ttyACM*")):
        if rnode_pruefen(k):
            return k
    return None


# ── Reticulum starten ─────────────────────────────────────────────────────────

def config_schreiben(funk, port):
    zeilen = [
        "[reticulum]",
        "  enable_transport = No",
        "  share_instance = Yes",
        f"  instance_name = {ARGS.instanz}",
        "  panic_on_interface_error = No",
        "",
        "[logging]",
        "  loglevel = 3",
        "",
        "[interfaces]",
    ]
    if port:
        zeilen += [
            "  [[Fundus RNode]]",
            "    type = RNodeInterface",
            "    enabled = yes",
            f"    port = {port}",
            f"    frequency = {int(funk['frequenz'])}",
            f"    bandwidth = {int(funk['bandbreite'])}",
            f"    txpower = {int(funk['sendeleistung'])}",
            f"    spreadingfactor = {int(funk['sf'])}",
            f"    codingrate = {int(funk['cr'])}",
            f"    airtime_limit_long = {float(funk['airtime_lang'])}",
            f"    airtime_limit_short = {float(funk['airtime_kurz'])}",
            "",
        ]
    tcp = (funk.get("tcp") or "").strip()
    if tcp.startswith("server:"):
        zeilen += ["  [[Fundus TCP]]", "    type = TCPServerInterface", "    enabled = yes",
                   "    listen_ip = 0.0.0.0", f"    listen_port = {int(tcp.split(':')[1])}", ""]
    elif tcp.startswith("client:"):
        _, host, p = tcp.split(":", 2)
        zeilen += ["  [[Fundus TCP]]", "    type = TCPClientInterface", "    enabled = yes",
                   f"    target_host = {host}", f"    target_port = {int(p)}", ""]
    os.makedirs(pfad("rns"), exist_ok=True)
    with open(pfad("rns", "config"), "w", encoding="utf-8") as f:
        f.write("\n".join(zeilen) + "\n")


class Ankuendigungen:
    aspect_filter = "lxmf.delivery"

    def received_announce(self, destination_hash, announced_identity, app_data):
        import RNS
        import LXMF
        try:
            name = LXMF.display_name_from_app_data(app_data) or ""
        except Exception:
            name = ""
        adr = destination_hash.hex()
        with LOCK:
            GEHOERT[adr] = {"name": name[:60], "zeit": int(time.time()),
                            "hops": RNS.Transport.hops_to(destination_hash)}
            if len(GEHOERT) > 500:
                for k in sorted(GEHOERT, key=lambda k: GEHOERT[k]["zeit"])[:100]:
                    del GEHOERT[k]
        json_schreiben(pfad("gehoert.json"), GEHOERT, 0o600)
        ereignis(art="gehoert", adresse=adr, name=name[:60])


def reticulum_starten():
    global RETICULUM
    import RNS
    funk = dict(STANDARD_FUNK)
    funk.update(json_lesen(pfad("funk.json"), {}))
    port = None
    if funk.get("aktiv", True):
        port = funk.get("port") or "auto"
        if port == "auto":
            port = rnode_suchen()
            if not port:
                STATUS["geraet_fehler"] = "Kein RNode gefunden – Heltec angeschlossen und RNode-Firmware aufgespielt?"
        elif not os.path.exists(port):
            STATUS["geraet_fehler"] = f"{port} nicht vorhanden"
            port = None
    STATUS["geraet"] = port
    STATUS["funk"] = funk
    config_schreiben(funk, port)
    RETICULUM = RNS.Reticulum(configdir=pfad("rns"))
    RNS.Transport.register_announce_handler(Ankuendigungen())
    STATUS["rns"] = getattr(RNS, "__version__", "?")
    log("Reticulum gestartet", "Gerät:", port or "–", STATUS["geraet_fehler"])


# ── Konten ────────────────────────────────────────────────────────────────────

def verlauf_datei(fid):
    return pfad("verlauf", fid + ".json")


def verlauf_anhaengen(fid, eintrag):
    with LOCK:
        v = json_lesen(verlauf_datei(fid), [])
        v.append(eintrag)
        del v[:-2000]
        json_schreiben(verlauf_datei(fid), v)


def verlauf_status(fid, mid, status):
    with LOCK:
        v = json_lesen(verlauf_datei(fid), [])
        for e in v:
            if e.get("id") == mid:
                e["status"] = status
        json_schreiben(verlauf_datei(fid), v)


HAUPT = threading.main_thread()
AUFGABEN = __import__("queue").Queue()


def im_hauptthread(f, *a):
    """LXMRouter setzt Signal-Handler – das geht nur im Hauptthread."""
    if threading.current_thread() is HAUPT:
        return f(*a)
    ergebnis = {}
    fertig = threading.Event()

    def lauf():
        try:
            ergebnis["wert"] = f(*a)
        except Exception as e:
            ergebnis["fehler"] = e
        fertig.set()

    AUFGABEN.put(lauf)
    if not fertig.wait(60):
        raise RuntimeError("Hauptthread antwortet nicht")
    if "fehler" in ergebnis:
        raise ergebnis["fehler"]
    return ergebnis["wert"]


def konto_starten(fid, key_hex, name):
    return im_hauptthread(_konto_starten, fid, key_hex, name)


def _konto_starten(fid, key_hex, name):
    import RNS
    import LXMF
    with LOCK:
        if fid in ROUTER:
            router, dest = ROUTER[fid]
            dest.display_name = name or None
            return dest
        ident = RNS.Identity.from_bytes(bytes.fromhex(key_hex))
        speicher = pfad("lxmf", fid)
        os.makedirs(speicher, exist_ok=True)
        router = LXMF.LXMRouter(identity=ident, storagepath=speicher, autopeer=False)
        dest = router.register_delivery_identity(ident, display_name=name or None)

        def empfangen(msg, fid=fid):
            try:
                von = msg.source_hash.hex()
                text = msg.content_as_string() or ""
                titel = msg.title_as_string() if hasattr(msg, "title_as_string") else ""
                name = (GEHOERT.get(von) or {}).get("name", "")
                e = {"id": msg.hash.hex() if msg.hash else os.urandom(8).hex(), "richtung": "ein", "mit": von,
                     "text": text[:4000], "titel": (titel or "")[:200], "zeit": int(msg.timestamp or time.time()),
                     "status": "empfangen", "name": name}
                verlauf_anhaengen(fid, e)
                ereignis(art="nachricht", fid=fid, von=von, name=name, text=text[:500], zeit=e["zeit"])
                log("Funk ←", von[:8], "an", fid[:10])
            except Exception:
                traceback.print_exc()

        router.register_delivery_callback(empfangen)
        ROUTER[fid] = (router, dest)
    log("Konto über Funk erreichbar:", fid[:10], dest.hash.hex())
    return dest


def lxmf_adresse(key_hex):
    """LXMF-Adresse (Ziel „lxmf.delivery“) zum Schlüssel – auch ohne laufendes Reticulum."""
    import RNS
    ident = RNS.Identity.from_bytes(bytes.fromhex(key_hex))
    return RNS.Destination.hash_from_name_and_identity("lxmf.delivery", ident).hex()


def konto_ankuendigen(fid):
    with LOCK:
        if fid not in ROUTER:
            return False
        router, dest = ROUTER[fid]
    try:
        router.announce(dest.hash)
        return True
    except Exception:
        traceback.print_exc()
        return False


def konten_laden():
    for p in sorted(glob.glob(pfad("konten", "*.json"))):
        k = json_lesen(p, None)
        if not k:
            continue
        try:
            konto_starten(k["fid"], k["key"], k.get("name", ""))
        except Exception:
            traceback.print_exc()


def ankuendigen_regelmaessig():
    # Alle 30 min ankündigen – häufiger kostet Sendezeit.
    time.sleep(20)
    while True:
        for fid in list(ROUTER):
            konto_ankuendigen(fid)
            time.sleep(5)
        time.sleep(1800)


WARTEND = []  # [(fid, an, text, mid, seit)] – Gegenstelle noch unbekannt
WARTE_MAX = 24 * 3600


def senden(fid, an, text):
    import RNS
    with LOCK:
        if fid not in ROUTER:
            raise ValueError("Konto ist nicht über Funk erreichbar – zuerst einschalten")
    mid = os.urandom(8).hex()
    e = {"id": mid, "richtung": "aus", "mit": an, "text": text[:4000], "zeit": int(time.time()),
         "status": "unterwegs", "name": (GEHOERT.get(an) or {}).get("name", "")}
    if RNS.Identity.recall(bytes.fromhex(an)) is None:
        # Über Funk kommen Ankündigungen verzögert oder gar nicht an: Nachricht
        # vormerken und zustellen, sobald die Gegenstelle zu hören ist.
        e["status"] = "wartet"
        verlauf_anhaengen(fid, e)
        with LOCK:
            WARTEND.append((fid, an, text, mid, time.time()))
        RNS.Transport.request_path(bytes.fromhex(an))
        return e
    verlauf_anhaengen(fid, e)
    _abschicken(fid, an, text, mid)
    return e


def warteschlange():
    """Vorgemerkte Nachrichten zustellen, sobald die Gegenstelle bekannt ist."""
    import RNS
    letzte_anfrage = {}
    while True:
        time.sleep(3)
        with LOCK:
            liste = list(WARTEND)
        for eintrag in liste:
            fid, an, text, mid, seit = eintrag
            ziel = bytes.fromhex(an)
            if RNS.Identity.recall(ziel) is not None:
                with LOCK:
                    if eintrag in WARTEND:
                        WARTEND.remove(eintrag)
                verlauf_status(fid, mid, "unterwegs")
                ereignis(art="status", fid=fid, id=mid, status="unterwegs")
                try:
                    _abschicken(fid, an, text, mid)
                except Exception:
                    traceback.print_exc()
            elif time.time() - seit > WARTE_MAX:
                with LOCK:
                    if eintrag in WARTEND:
                        WARTEND.remove(eintrag)
                verlauf_status(fid, mid, "gescheitert")
                ereignis(art="status", fid=fid, id=mid, status="gescheitert")
            elif time.time() - letzte_anfrage.get(an, 0) > 300:
                letzte_anfrage[an] = time.time()   # Pfadanfrage höchstens alle 5 min
                RNS.Transport.request_path(ziel)


def _abschicken(fid, an, text, mid):
    import RNS
    import LXMF
    with LOCK:
        router, quelle = ROUTER[fid]
    ident = RNS.Identity.recall(bytes.fromhex(an))
    ziel = RNS.Destination(ident, RNS.Destination.OUT, RNS.Destination.SINGLE, "lxmf", "delivery")
    lxm = LXMF.LXMessage(ziel, quelle, text, title="", desired_method=LXMF.LXMessage.DIRECT)
    lxm.try_propagation_on_fail = False

    def zugestellt(m, fid=fid, mid=mid):
        verlauf_status(fid, mid, "zugestellt")
        ereignis(art="status", fid=fid, id=mid, status="zugestellt")

    def gescheitert(m, fid=fid, mid=mid):
        verlauf_status(fid, mid, "gescheitert")
        ereignis(art="status", fid=fid, id=mid, status="gescheitert")

    lxm.register_delivery_callback(zugestellt)
    lxm.register_failed_callback(gescheitert)
    router.handle_outbound(lxm)
    log("Funk →", an[:8], "von", fid[:10])


# ── Status ────────────────────────────────────────────────────────────────────

def status():
    import RNS
    schnitt = []
    for i in list(getattr(RNS.Transport, "interfaces", [])):
        d = {"name": str(getattr(i, "name", i)), "online": bool(getattr(i, "online", False)),
             "rx": int(getattr(i, "rxb", 0) or 0), "tx": int(getattr(i, "txb", 0) or 0),
             "bitrate": int(getattr(i, "bitrate", 0) or 0)}
        for feld, ziel in (("r_airtime_short", "airtime_kurz"), ("r_airtime_long", "airtime_lang"),
                           ("r_channel_load_short", "kanal_kurz"), ("r_channel_load_long", "kanal_lang"),
                           ("r_stat_rssi", "rssi"), ("r_stat_snr", "snr")):
            v = getattr(i, feld, None)
            if v is not None:
                d[ziel] = round(float(v), 2)
        schnitt.append(d)
    with LOCK:
        konten = {fid: d.hash.hex() for fid, (r, d) in ROUTER.items()}
        gehoert = len(GEHOERT)
    return {"version": VERSION, "rns": STATUS.get("rns"), "laeuft_seit": int(STATUS["start"]),
            "geraet": STATUS.get("geraet"), "geraet_fehler": STATUS.get("geraet_fehler", ""),
            "funk": STATUS.get("funk"), "schnittstellen": schnitt, "konten": konten, "gehoert": gehoert}


# ── Schnittstelle ─────────────────────────────────────────────────────────────

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def antwort(self, code, daten):
        b = json.dumps(daten, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def eingabe(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n > 64 * 1024:
            raise ValueError("zu groß")
        return json.loads(self.rfile.read(n) or b"{}")

    def do_GET(self):
        try:
            u = urlparse(self.path)
            q = {k: v[0] for k, v in parse_qs(u.query).items()}
            if u.path == "/status":
                return self.antwort(200, status())
            if u.path == "/gehoert":
                with LOCK:
                    return self.antwort(200, {"gehoert": GEHOERT})
            if u.path == "/verlauf":
                fid = sichere_fid(q.get("fid"))
                v = json_lesen(verlauf_datei(fid), [])
                if q.get("mit"):
                    mit = sichere_adresse(q["mit"])
                    v = [e for e in v if e.get("mit") == mit]
                return self.antwort(200, {"verlauf": v[-500:]})
            if u.path == "/konto":
                fid = sichere_fid(q.get("fid"))
                k = json_lesen(pfad("konten", fid + ".json"), {})
                return self.antwort(200, {"erreichbar": bool(k), "adresse": lxmf_adresse(k["key"]) if k else "",
                                          "name": k.get("name", ""), "aktiv": fid in ROUTER})
            if u.path == "/ereignisse":
                nach = int(q.get("nach") or 0)
                warten = min(float(q.get("warten") or 25), 55)
                with NEU:
                    if not any(e["seq"] > nach for e in EREIGNISSE):
                        NEU.wait(warten)
                    neu = [e for e in EREIGNISSE if e["seq"] > nach]
                    return self.antwort(200, {"ereignisse": neu, "seq": SEQ[0]})
            return self.antwort(404, {"fehler": "unbekannt"})
        except ValueError as e:
            return self.antwort(400, {"fehler": str(e)})
        except Exception as e:
            traceback.print_exc()
            return self.antwort(500, {"fehler": str(e)})

    def do_POST(self):
        try:
            u = urlparse(self.path)
            d = self.eingabe()
            if u.path == "/konto":
                fid = sichere_fid(d.get("fid"))
                if not d.get("erreichbar", True):
                    with LOCK:
                        r = ROUTER.pop(fid, None)
                    if r:
                        try:
                            im_hauptthread(r[0].exit_handler)
                        except Exception:
                            pass
                    try:
                        os.remove(pfad("konten", fid + ".json"))
                    except FileNotFoundError:
                        pass
                    return self.antwort(200, {"erreichbar": False, "neustart_noetig": r is not None})
                key = (d.get("key") or "").strip().lower()
                if len(key) != 128 or any(c not in "0123456789abcdef" for c in key):
                    raise ValueError("Schlüssel ungültig")
                name = (d.get("name") or "").strip()[:40]
                json_schreiben(pfad("konten", fid + ".json"), {"fid": fid, "key": key, "name": name})
                if RETICULUM is None:
                    # Schlüssel ist gespeichert; erreichbar, sobald Reticulum läuft.
                    return self.antwort(200, {"erreichbar": True, "adresse": lxmf_adresse(key), "name": name,
                                              "hinweis": STATUS.get("geraet_fehler", "")})
                dest = konto_starten(fid, key, name)
                threading.Thread(target=konto_ankuendigen, args=(fid,), daemon=True).start()
                return self.antwort(200, {"erreichbar": True, "adresse": dest.hash.hex(), "name": name})
            if u.path == "/ankuendigen":
                return self.antwort(200, {"ok": konto_ankuendigen(sichere_fid(d.get("fid")))})
            if u.path == "/senden":
                fid = sichere_fid(d.get("fid"))
                an = sichere_adresse(d.get("an"))
                text = (d.get("text") or "").strip()
                if not text or len(text) > 2000:
                    raise ValueError("Text fehlt oder ist zu lang (max. 2000 Zeichen)")
                return self.antwort(200, {"nachricht": senden(fid, an, text)})
            if u.path == "/neustart":
                self.antwort(200, {"ok": True})
                threading.Thread(target=lambda: (time.sleep(0.5), os._exit(3)), daemon=True).start()
                return
            return self.antwort(404, {"fehler": "unbekannt"})
        except ValueError as e:
            return self.antwort(400, {"fehler": str(e)})
        except Exception as e:
            traceback.print_exc()
            return self.antwort(500, {"fehler": str(e)})


class UnixHTTPServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def get_request(self):
        req, _ = super().get_request()
        return req, ("lokal", 0)


def main():
    global ARGS
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", default="/opt/fundus/data/rns")
    ap.add_argument("--sock", default="/opt/fundus/data/rns/api.sock")
    ap.add_argument("--instanz", default="fundus", help="Reticulum-Instanzname (für Tests mehrerer Dienste)")
    ARGS = ap.parse_args()
    os.makedirs(ARGS.data, exist_ok=True)
    os.chmod(ARGS.data, 0o700)
    GEHOERT.update(json_lesen(pfad("gehoert.json"), {}))
    try:
        reticulum_starten()
    except Exception as e:
        traceback.print_exc()
        STATUS["geraet_fehler"] = "Reticulum startet nicht: " + str(e)
    if RETICULUM is not None:
        konten_laden()
        threading.Thread(target=ankuendigen_regelmaessig, daemon=True).start()
        threading.Thread(target=warteschlange, daemon=True).start()
    try:
        os.remove(ARGS.sock)
    except FileNotFoundError:
        pass
    os.makedirs(os.path.dirname(ARGS.sock), exist_ok=True)
    srv = UnixHTTPServer(ARGS.sock, Handler)
    os.chmod(ARGS.sock, 0o660)
    log("fundus-rns", VERSION, "bereit:", ARGS.sock)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    while True:
        try:
            AUFGABEN.get()()
        except Exception:
            traceback.print_exc()


if __name__ == "__main__":
    main()
