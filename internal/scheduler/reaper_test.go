package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/store"
)

func TestReaper_ClosesExpiredConversation(t *testing.T) {
	var mu sync.Mutex
	var resolved []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if body["status"] == "resolved" {
			mu.Lock()
			resolved = append(resolved, 1) // basta contar
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	// Prazo já vencido.
	if err := st.CreateDeadline(ctx, 700, 1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	cw := chatwoot.NewClient(srv.URL, "1", "token")
	r := New(st, cw, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// tick de boot processa o prazo vencido imediatamente.
	rctx, cancel := context.WithCancel(ctx)
	go r.Run(rctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(resolved)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("reaper não resolveu a conversa vencida no tempo esperado")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	// Prazo não deve reaparecer (foi concluído).
	claimed, err := st.ClaimExpiredDeadlines(ctx, time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Errorf("prazo deveria estar concluído, got %+v", claimed)
	}
}
