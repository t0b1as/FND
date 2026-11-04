#!/usr/bin/env python3
# Wettersatelliten (R863): Überflüge der Meteor-M-Satelliten planen, den
# RTL-SDR-Stick für die Dauer eines Überflugs übernehmen, Bilder mit SatDump
# dekodieren – und den Stick danach IMMER an den gewählten Modus zurückgeben.
#
# Läuft als fundus-meteor.service (Root). Schreibt:
#   /run/fundus-funk/meteor-plan.json   nächste Überflüge (für die Seite)
#   /run/fundus-funk/meteor.lock        solange ein Überflug läuft (funk-modus.sh wartet)
#   <DataDir>/meteor/<zeit>_<sat>/      Bilder je Überflug + fundus.json
# Einstellung (vom Node geschrieben): <DataDir>/meteor-auto  ("an" / "aus")
#
# Kleine Pis (< 2 GB): erst aufzeichnen, nach dem Überflug mit niedrigster
# Priorität dekodieren – live hält ein Pi 3 nicht mit. Größere: live.

import datetime as dt
import glob
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request

DATA = os.environ.get("FUNDUS_DATA_DIR", "/opt/fundus/data")
RUN = "/run/fundus-funk"
ABLAGE = os.path.join(DATA, "meteor")
WERKZEUGE = os.path.dirname(os.path.realpath(__file__))
TLE_URL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=weather&FORMAT=tle"
SATELLITEN = {  # Name bei Celestrak → Frequenz in MHz (einstellbar)
    "METEOR-M2 3": float(os.environ.get("FUNDUS_METEOR_M2_3_MHZ", "137.9")),
    "METEOR-M2 4": float(os.environ.get("FUNDUS_METEOR_M2_4_MHZ", "137.9")),
}
MIN_EL = float(os.environ.get("FUNDUS_METEOR_MIN_ELEVATION", "25"))
BEHALTEN = 30  # so viele Überflüge bleiben gespeichert


def log(*a):
    print(dt.datetime.now().strftime("%H:%M:%S"), *a, flush=True)


def json_schreiben(pfad, daten):
    os.makedirs(os.path.dirname(pfad), exist_ok=True)
    tmp = pfad + ".neu"
    with open(tmp, "w") as f:
        json.dump(daten, f)
    os.chmod(tmp, 0o644)
    os.replace(tmp, pfad)


def automatik():
    try:
        return open(os.path.join(DATA, "meteor-auto")).read().strip() != "aus"
    except OSError:
        return True  # Voreinstellung: an


def standort():
    try:
        d = json.load(open(os.path.join(DATA, "location.json")))
        if d.get("lat") or d.get("lon"):
            return float(d["lat"]), float(d["lon"])
    except (OSError, ValueError, KeyError):
        pass
    return None


def bahndaten():
    """TLE höchstens einmal täglich holen; bei Netzproblemen die alten nehmen."""
    os.makedirs(ABLAGE, exist_ok=True)
    pfad = os.path.join(ABLAGE, "tle.txt")
    alt = time.time() - os.path.getmtime(pfad) if os.path.exists(pfad) else 1e9
    if alt > 86400:
        try:
            req = urllib.request.Request(TLE_URL, headers={"User-Agent": "Fundus-Meteor/1.0"})
            daten = urllib.request.urlopen(req, timeout=20).read().decode("ascii", "replace")
            if "METEOR" in daten:
                open(pfad, "w").write(daten)
                log("Bahndaten aktualisiert")
        except Exception as e:  # noqa: BLE001
            log("Bahndaten nicht ladbar:", e)
    zeilen = [z.strip() for z in open(pfad).read().splitlines()] if os.path.exists(pfad) else []
    tle = {}
    for i in range(len(zeilen) - 2):
        if zeilen[i] in SATELLITEN and zeilen[i + 1].startswith("1 ") and zeilen[i + 2].startswith("2 "):
            tle[zeilen[i]] = (zeilen[i + 1], zeilen[i + 2])
    return tle


def richtung(az):
    return ["N", "NO", "O", "SO", "S", "SW", "W", "NW"][int((az % 360) / 45 + 0.5) % 8]


def planen(stunden=24):
    from skyfield.api import EarthSatellite, load, wgs84

    ort = standort()
    if not ort:
        return {"stand": int(time.time()), "fehler": "Kein Standort – Einstellungen → Standort", "ueberfluege": []}
    ts = load.timescale()
    beob = wgs84.latlon(ort[0], ort[1])
    t0 = ts.now()
    t1 = ts.from_datetime(dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=stunden))
    liste = []
    for name, (l1, l2) in bahndaten().items():
        sat = EarthSatellite(l1, l2, name, ts)
        zeiten, ereignisse = sat.find_events(beob, t0, t1, altitude_degrees=10.0)
        auf = hoch = None
        for t, e in zip(zeiten, ereignisse):
            if e == 0:
                auf, hoch = t, None
            elif e == 1 and auf is not None:
                hoch = t
            elif e == 2 and auf is not None and hoch is not None:
                el, _, _ = (sat - beob).at(hoch).altaz()
                if el.degrees >= MIN_EL:
                    _, az_auf, _ = (sat - beob).at(auf).altaz()
                    _, az_ab, _ = (sat - beob).at(t).altaz()
                    spur = []
                    s0, s1 = auf.utc_datetime(), t.utc_datetime()
                    schritt = 0
                    while s0 + dt.timedelta(seconds=schritt) <= s1:
                        pos = sat.at(ts.from_datetime(s0 + dt.timedelta(seconds=schritt)))
                        try:
                            sp = wgs84.subpoint_of(pos)
                        except AttributeError:  # ältere skyfield-Versionen
                            sp = wgs84.subpoint(pos)
                        spur.append([round(sp.latitude.degrees, 3), round(sp.longitude.degrees, 3)])
                        schritt += 30
                    liste.append({
                        "sat": name, "mhz": SATELLITEN[name],
                        "aos": int(auf.utc_datetime().timestamp()), "los": int(t.utc_datetime().timestamp()),
                        "max_el": round(el.degrees), "von": richtung(az_auf.degrees), "nach": richtung(az_ab.degrees),
                        "spur": spur,
                    })
                auf = hoch = None
    liste.sort(key=lambda p: p["aos"])
    return {"stand": int(time.time()), "standort": ort, "min_el": MIN_EL, "ueberfluege": liste}


def ram_mb():
    try:
        for z in open("/proc/meminfo"):
            if z.startswith("MemTotal"):
                return int(z.split()[1]) // 1024
    except OSError:
        pass
    return 0


def lauf(befehl, sekunden=None, nice=False):
    if nice:
        befehl = ["nice", "-n", "19", "ionice", "-c3"] + befehl
    log("starte:", " ".join(befehl))
    try:
        r = subprocess.run(befehl, timeout=sekunden, capture_output=True, text=True)
        if r.returncode != 0:
            log("Exit", r.returncode, (r.stderr or r.stdout)[-400:])
        return r.returncode
    except subprocess.TimeoutExpired:
        return 0  # Aufnahme endet planmäßig per Zeitlimit
    except FileNotFoundError as e:
        log("fehlt:", e)
        return 127


def aufnehmen(p):
    kurz = p["sat"].replace("METEOR-", "").replace(" ", "-")
    ziel = os.path.join(ABLAGE, dt.datetime.utcfromtimestamp(p["aos"]).strftime("%Y%m%d-%H%M") + "_" + kurz)
    os.makedirs(ziel, exist_ok=True)
    info = dict(p, zustand="aufnahme", bilder=[])
    json_schreiben(os.path.join(ziel, "fundus.json"), info)
    json_schreiben(os.path.join(RUN, "meteor.lock"), {"sat": p["sat"], "los": p["los"], "ordner": os.path.basename(ziel)})
    roh = os.path.join(ABLAGE, "aufnahme.u8")
    live = ram_mb() >= 2000
    try:
        for d in ("readsb", "fundus-vdl2", "fundus-ais"):
            subprocess.run(["systemctl", "stop", d], capture_output=True)
        time.sleep(2)
        dauer = max(60, p["los"] - int(time.time()) + 15)
        hz = str(int(p["mhz"] * 1e6))
        if live:
            lauf(["satdump", "live", "meteor_m2-x_lrpt", ziel, "--source", "rtlsdr", "--samplerate", "1.024e6",
                  "--frequency", hz, "--gain", "40", "--timeout", str(dauer)], sekunden=dauer + 120)
        else:
            # 288 kS/s reicht für das 72-kbit/s-Signal und hält die Datei klein (~0,5 GB / 15 min)
            lauf(["rtl_sdr", "-f", hz, "-s", "288000", "-g", "40", roh], sekunden=dauer)
    finally:
        try:
            os.remove(os.path.join(RUN, "meteor.lock"))
        except OSError:
            pass
        # Stick zurück an den gewählten Modus – immer, auch nach Fehlern.
        subprocess.run(["/bin/bash", os.path.join(WERKZEUGE, "funk-modus.sh")], capture_output=True)
    if not live and os.path.exists(roh):
        info["zustand"] = "dekodiert"
        json_schreiben(os.path.join(ziel, "fundus.json"), info)
        lauf(["satdump", "meteor_m2-x_lrpt", "baseband", roh, ziel, "--samplerate", "288000",
              "--baseband_format", "u8"], sekunden=3 * 3600, nice=True)
        os.remove(roh)
    bilder = sorted(os.path.relpath(b, ziel) for b in glob.glob(os.path.join(ziel, "**", "*.png"), recursive=True))
    info.update(zustand="fertig", bilder=bilder)
    json_schreiben(os.path.join(ziel, "fundus.json"), info)
    for wurzel, ordner, dateien in os.walk(ziel):
        for n in ordner:
            os.chmod(os.path.join(wurzel, n), 0o755)
        for n in dateien:
            os.chmod(os.path.join(wurzel, n), 0o644)
    os.chmod(ziel, 0o755)
    log(f"Überflug {p['sat']}: {len(bilder)} Bild(er)")
    # Nur die letzten BEHALTEN Überflüge aufheben.
    alle = sorted(d for d in glob.glob(os.path.join(ABLAGE, "2*")) if os.path.isdir(d))
    for alt in alle[:-BEHALTEN]:
        shutil.rmtree(alt, ignore_errors=True)


def main():
    os.makedirs(ABLAGE, exist_ok=True)
    os.chmod(ABLAGE, 0o755)
    erledigt = set()
    plan, geplant = None, 0
    while True:
        if plan is None or time.time() - geplant > 1800:
            try:
                plan = planen()
            except Exception as e:  # noqa: BLE001
                plan = {"stand": int(time.time()), "fehler": f"Planung fehlgeschlagen: {e}", "ueberfluege": []}
            geplant = time.time()
            plan["automatik"] = automatik()
            json_schreiben(os.path.join(RUN, "meteor-plan.json"), plan)
        jetzt = time.time()
        naechster = next((p for p in plan.get("ueberfluege", []) if p["los"] > jetzt + 60 and
                          (p["sat"], p["aos"]) not in erledigt), None)
        if not automatik() or naechster is None:
            time.sleep(60)
            continue
        warten = naechster["aos"] - 20 - jetzt
        if warten > 0:
            time.sleep(min(warten, 60))
            continue
        erledigt.add((naechster["sat"], naechster["aos"]))
        try:
            aufnehmen(naechster)
        except Exception as e:  # noqa: BLE001
            log("Aufnahme fehlgeschlagen:", e)
        plan = None  # neu planen


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(0)
