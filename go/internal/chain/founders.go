package chain

// Gründungs-Validatoren (fest eincompiliert).
//
// Das Validator-Set muss auf ALLEN Nodes identisch berechnet werden – jeder Block
// trägt eine Prüfsumme des Sets (ValSetHash). Früher flossen lokale Quellen ein
// (FUNDUS_VALIDATORS aus der .env, von Peers "gelernte" Adressen, ein Solo-Start
// ohne Liste); unterschiedliche .env-Dateien führten zu unterschiedlichen Sets,
// die Nodes lehnten gegenseitig ihre Blöcke ab und die Chain zerfiel in mehrere.
//
// Jetzt: Set = Gründer (hier, gleiches Programm = gleiche Liste)
//            ∪ Produzenten der letzten ValidatorLookback Blöcke
//            ∪ aktive Staker.
// Weitere Nodes werden per Stake Validator (Wallet-Seite, Node-Wallet).
// Eine Änderung dieser Liste ist eine Konsensänderung: alle Nodes gleichzeitig.
var FoundingValidators = []string{
	"0x8749a56050a4bed3b15a996e666a6e03cf69467f", // fundus (10.10.11.25) – Node-Wallet
	"0xf6891fbe8321d8c53c8a3c9f4eac63d0cd47a6d9", // fnd    (10.10.11.39) – Node-Wallet
}

// foundingAddrs liefert die Gründer als Adressen (ungültige Einträge ignoriert).
func foundingAddrs() []Address {
	out := make([]Address, 0, len(FoundingValidators))
	for _, h := range FoundingValidators {
		if a, ok := AddressFromHex(h); ok {
			out = append(out, a)
		}
	}
	return out
}

// FoundingValidatorSet liefert das Gründer-Set (für die Konsens-Aktivierung).
func FoundingValidatorSet() (*ValidatorSet, error) {
	return NewValidatorSet(foundingAddrs())
}
