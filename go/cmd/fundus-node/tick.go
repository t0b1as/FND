package main

// Tick-Intervall des PoA-Produktions-Loops (FND-001).
//
// Bisher stand in runBlockProductionLoop:
//
//	tickInterval := time.Second
//	if chain.BlockTime == 0 {
//	    tickInterval = time.Duration(chain.BlockTime) * time.Second  // = 0
//	}
//
// Die Bedingung ist invertiert: genau im Fall BlockTime == 0 wird das Intervall
// auf 0 gesetzt, und time.NewTicker(0) panict. Aktuell ist BlockTime 5, der Zweig
// also tot — bis jemand BlockTime konfigurierbar macht. Dann fällt der Node beim
// Start um, nicht im Betrieb, was die Ursache gut versteckt.

import "time"

// minTickInterval ist die Untergrenze. Der Loop tickt bewusst feiner als
// BlockTime, damit die zeitbasierte Fallback-Rotation zügig greift; häufigeres
// Ticken erzeugt keine zusätzlichen Blöcke, es prüft nur öfter, ob dieser Node
// an der Reihe ist.
const minTickInterval = time.Second

// tickIntervalFor liefert ein garantiert positives Tick-Intervall.
// Zusage: Rückgabe > 0 für jede Eingabe — das schließt den NewTicker-Panic aus.
//
// Kein Überlauf möglich: der Multiplikationszweig ist nur für Werte unterhalb
// einer Sekunde erreichbar, und BlockTime zählt in ganzen Sekunden.
func tickIntervalFor(blockTimeSeconds uint64) time.Duration {
	if blockTimeSeconds == 0 {
		return minTickInterval
	}
	if blockTimeSeconds < uint64(minTickInterval/time.Second) {
		return time.Duration(blockTimeSeconds) * time.Second
	}
	return minTickInterval
}
