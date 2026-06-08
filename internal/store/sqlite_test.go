package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := NewSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMarkProcessed_Dedup(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	fresh, err := st.MarkProcessed(ctx, "delivery-1")
	if err != nil || !fresh {
		t.Fatalf("primeira entrega deveria ser nova: fresh=%v err=%v", fresh, err)
	}
	fresh, err = st.MarkProcessed(ctx, "delivery-1")
	if err != nil || fresh {
		t.Fatalf("entrega duplicada deveria retornar fresh=false: fresh=%v err=%v", fresh, err)
	}
}

func TestClaimExpiredDeadlines(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// Vencido -> deve ser reivindicado.
	mustNoErr(t, st.CreateDeadline(ctx, 100, 1, now.Add(-time.Minute)))
	// Futuro -> não deve aparecer.
	mustNoErr(t, st.CreateDeadline(ctx, 200, 1, now.Add(time.Hour)))
	// Respondido -> não deve aparecer mesmo vencido.
	mustNoErr(t, st.CreateDeadline(ctx, 300, 1, now.Add(-time.Minute)))
	mustNoErr(t, st.MarkDeadlineAnswered(ctx, 300))

	claimed, err := st.ClaimExpiredDeadlines(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ConversationID != 100 {
		t.Fatalf("esperava reivindicar apenas a conversa 100, got %+v", claimed)
	}

	// Após CompleteDeadline, não reaparece.
	mustNoErr(t, st.CompleteDeadline(ctx, 100))
	claimed, err = st.ClaimExpiredDeadlines(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("prazo concluído não deveria reaparecer: %+v", claimed)
	}
}

func TestStateRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if got := mustState(t, st, 42); got != nil {
		t.Fatalf("estado inexistente deveria ser nil, got %+v", got)
	}
	mustNoErr(t, st.SetState(ctx, &ConversationState{ConversationID: 42, AccountID: 1, State: StateTriage, Data: `{"x":1}`}))
	got := mustState(t, st, 42)
	if got == nil || got.State != StateTriage || got.Data != `{"x":1}` {
		t.Fatalf("estado não persistido corretamente: %+v", got)
	}
	// Upsert.
	mustNoErr(t, st.SetState(ctx, &ConversationState{ConversationID: 42, AccountID: 1, State: StateRouted}))
	got = mustState(t, st, 42)
	if got.State != StateRouted {
		t.Fatalf("upsert não atualizou estado: %+v", got)
	}
}

func mustState(t *testing.T, st *SQLiteStore, id int64) *ConversationState {
	t.Helper()
	s, err := st.GetState(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
