/*
Copyright 2026 UnPoilTefal.
SPDX-License-Identifier: MIT
*/

package controller

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func mustParse(t *testing.T, expr string) cron.Schedule {
	t.Helper()
	s, err := cron.ParseStandard(expr)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDueAndNextCatchesUpOnlyTheLatestOccurrence(t *testing.T) {
	now := time.Date(2026, 10, 4, 2, 30, 30, 0, time.UTC)
	last := now.AddDate(0, 0, -30) // opérateur arrêté un mois : ~43 000 échéances manquées

	due, next := dueAndNext(mustParse(t, "* * * * *"), time.UTC, last, now)

	if want := time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("due = %v, attendu %v", due, want)
	}
	if want := time.Date(2026, 10, 4, 2, 31, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %v, attendu %v", next, want)
	}
}

func TestDueAndNextWithRareSchedule(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, paris)
	last := time.Date(2026, 9, 1, 0, 0, 0, 0, paris)

	due, next := dueAndNext(mustParse(t, "0 2 * * *"), paris, last, now)

	if want := time.Date(2026, 10, 4, 2, 0, 0, 0, paris); !due.Equal(want) {
		t.Fatalf("due = %v, attendu %v", due, want)
	}
	if want := time.Date(2026, 10, 5, 2, 0, 0, 0, paris); !next.Equal(want) {
		t.Fatalf("next = %v, attendu %v", next, want)
	}
}

func TestDueAndNextNothingDueYet(t *testing.T) {
	now := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	due, _ := dueAndNext(mustParse(t, "0 2 * * *"), time.UTC, now.Add(-time.Hour), now)
	if !due.IsZero() {
		t.Fatalf("due = %v, attendu aucune échéance", due)
	}
}

func TestDueAndNextNeverFiringCronTerminates(t *testing.T) {
	now := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	due, next := dueAndNext(mustParse(t, "0 0 30 2 *"), time.UTC, now.AddDate(-1, 0, 0), now)
	if !due.IsZero() || !next.IsZero() {
		t.Fatalf("due = %v, next = %v, attendu zéro pour un 30 février", due, next)
	}
}
