package api

// TTL / Aufräumen persönlicher Daten.
//
// Ohne Ablauf wuchsen Mailbox, Keydir, Kontakte, E-Mail-Verzeichnis und
// Messenger-Verläufe unbegrenzt – auch mit Daten, die über das Netz von fremden
// Nutzern hereinkommen. Täglich (erster Lauf 10 min nach Start):
//
//   Mailbox/Outbox  (nicht abgeholte Nachrichten)      FUNDUS_TTL_MAILBOX_DAYS   (60)
//   Keydir, Kontakte, E-Mail-Verzeichnis                FUNDUS_TTL_DIRECTORY_DAYS (365)
//   Messenger-Verlauf                                   FUNDUS_TTL_HISTORY_DAYS   (365)
//
// Einträge von Nutzern, deren HEIM-Node dieser Node ist, bleiben bei Keydir und
// Kontakten unangetastet – sonst verlöre ein Nutzer nach längerer Pause seine
// Kontakte oder seine Erreichbarkeit. Verläufe: einzelne Nachrichten altern beim
// Login aus (Schlüssel nötig), ganze Verzeichnisse lange inaktiver Nutzer nach
// der doppelten Frist.

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/fundus/node/internal/messenger"
	"github.com/fundus/node/internal/storage"
)

var retentionStart sync.Once

func (s *Server) runRetention(ctx context.Context) {
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.retentionOnce()
		timer.Reset(24 * time.Hour)
	}
}

func (s *Server) retentionOnce() {
	if s.store == nil || s.cfg == nil {
		return
	}
	days := func(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
	type rule struct {
		rt       storage.RecordType
		maxAge   time.Duration
		keepHome bool // Einträge eigener Heim-Nutzer behalten
	}
	rules := []rule{
		{storage.RecordMailbox, days(s.cfg.TTLMailboxDays), false},
		{storage.RecordOutbox, days(s.cfg.TTLMailboxDays), false},
		{storage.RecordKeyDir, days(s.cfg.TTLDirectoryDays), true},
		{storage.RecordContacts, days(s.cfg.TTLDirectoryDays), true},
		{storage.RecordEmailDir, days(s.cfg.TTLDirectoryDays), true},
	}
	total := map[string]int{}
	for _, r := range rules {
		if r.maxAge <= 0 {
			continue
		}
		recs, err := s.store.List(r.rt)
		if err != nil {
			continue
		}
		cutoff := time.Now().Add(-r.maxAge)
		for _, rec := range recs {
			ts := rec.UpdatedAt
			if ts.IsZero() {
				ts = rec.CreatedAt
			}
			if ts.IsZero() || ts.After(cutoff) {
				continue
			}
			if r.keepHome && s.isHomeFor(recordFundusID(rec)) {
				continue
			}
			if s.store.Delete(r.rt, rec.ID) == nil {
				total[string(r.rt)]++
			}
		}
	}
	if s.cfg.DataDir != "" && s.cfg.TTLHistoryDays > 0 {
		if n := messenger.PruneInactiveUsers(s.cfg.DataDir, days(2*s.cfg.TTLHistoryDays)); n > 0 {
			total["history_users"] = n
		}
	}
	if len(total) > 0 && s.log != nil {
		s.log.Info("TTL-Aufräumen", zap.Any("geloescht", total))
	}
}

// recordFundusID ermittelt die FundusID, zu der ein persönlicher Record gehört.
func recordFundusID(rec *storage.Record) string {
	if v, _ := rec.Data["fundus_id"].(string); v != "" {
		return strings.ToLower(v)
	}
	for _, pre := range []string{"keydir:", "contacts:"} {
		if strings.HasPrefix(rec.ID, pre) {
			return strings.ToLower(strings.TrimPrefix(rec.ID, pre))
		}
	}
	return strings.ToLower(rec.OwnerID)
}
