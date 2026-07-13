package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/config"
	"bot-atendimento-promosystem/internal/llm"
	"bot-atendimento-promosystem/internal/store"
)

// recordingClassifier grava as chamadas e o histórico recebido, para asserir o
// agrupamento de mensagens.
type recordingClassifier struct {
	intent llm.Intent
	mu     sync.Mutex
	calls  int
	last   []llm.Turn
}

func (c *recordingClassifier) Classify(_ context.Context, _ string, history []llm.Turn) (llm.Intent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.last = append([]llm.Turn(nil), history...)
	return c.intent, nil
}

func (c *recordingClassifier) snapshot() (int, []llm.Turn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]llm.Turn(nil), c.last...)
}

// fakeTranscriber simula o STT, guardando os bytes recebidos.
type fakeTranscriber struct {
	text string
	err  error
	mu   sync.Mutex
	got  []byte
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, _, _ string) (string, error) {
	f.mu.Lock()
	f.got = append([]byte(nil), audio...)
	f.mu.Unlock()
	return f.text, f.err
}

// newConfiguredEngine monta uma engine com janela de debounce e transcritor
// customizados (espelha newTestEngineWith de engine_test.go).
func newConfiguredEngine(t *testing.T, cw *fakeChatwoot, classifier llm.Classifier, transcriber llm.Transcriber, window time.Duration) *Engine {
	t.Helper()
	cfg := &config.Config{
		ChatwootBaseURL:    cw.srv.URL,
		ChatwootAccountID:  "1",
		ChatwootAPIToken:   "token",
		LabelBot:           "fila-bot",
		LabelCSAT:          "fila-csat",
		DefaultSectorLabel: "fila-humano",
		Sectors:            map[string]string{"financeiro": "fila-financeiro"},
		CSATTimeout:        5 * time.Minute,
		CSATAttribute:      "csat_rating",
		DebounceWindow:     window,
		STTLanguage:        "pt",
	}
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	client := chatwoot.NewClient(cfg.ChatwootBaseURL, cfg.ChatwootAccountID, cfg.ChatwootAPIToken)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := New(cfg, st, client, classifier, transcriber, log)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condição não satisfeita em %s", timeout)
}

// TestDebounce_GroupsRapidMessages cobre o problema de o bot responder só à primeira
// mensagem: três mensagens em rajada são agrupadas em um único turno e geram uma
// única consulta ao modelo e uma única resposta.
func TestDebounce_GroupsRapidMessages(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	clf := &recordingClassifier{intent: llm.Intent{Action: "reply", Reply: "ok"}}
	eng := newConfiguredEngine(t, cw, clf, nil, 50*time.Millisecond)

	conv := chatwoot.Conversation{ID: 100, AccountID: 1, Labels: []string{"fila-bot"}}
	for _, m := range []string{"oi", "tudo bem?", "queria saber do pedido 123"} {
		eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
			MessageType:  chatwoot.MessageIncoming,
			Sender:       chatwoot.Sender{Type: "contact"},
			Content:      m,
			Conversation: conv,
		})
		time.Sleep(10 * time.Millisecond) // dentro da janela: rearma o timer
	}

	// Espera o flush completar (a resposta ao cliente é o último passo da triagem).
	waitFor(t, 2*time.Second, func() bool {
		return len(cw.findAll(http.MethodPost, "/conversations/100/messages")) == 1
	})

	calls, last := clf.snapshot()
	if calls != 1 {
		t.Fatalf("esperava 1 chamada ao LLM (mensagens agrupadas), got %d", calls)
	}
	if len(last) != 1 {
		t.Fatalf("esperava 1 turno do cliente combinado, got %d: %+v", len(last), last)
	}
	if want := "oi\ntudo bem?\nqueria saber do pedido 123"; last[0].Content != want {
		t.Errorf("conteúdo combinado inesperado:\n got %q\nwant %q", last[0].Content, want)
	}
	if msgs := cw.findAll(http.MethodPost, "/conversations/100/messages"); len(msgs) != 1 {
		t.Errorf("esperava exatamente 1 resposta ao cliente, got %d: %+v", len(msgs), msgs)
	}
}

// TestDebounce_MergesProactiveGreetingWithFirstMessage cobre a corrida que gerava
// saudação dupla: numa conversa recém-aberta pelo cliente, o conversation_updated
// (etiqueta do bot aplicada → saudação proativa) e o message_created (primeira
// mensagem) chegam quase juntos. Ambos passam pela mesma janela de debounce, então
// deve haver UMA única consulta ao modelo e UMA resposta, com a mensagem do cliente
// como turno — nunca duas saudações.
func TestDebounce_MergesProactiveGreetingWithFirstMessage(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	clf := &recordingClassifier{intent: llm.Intent{Action: "reply", Reply: "Olá! Como posso ajudar?"}}
	eng := newConfiguredEngine(t, cw, clf, nil, 50*time.Millisecond)

	conv := chatwoot.Conversation{ID: 318, AccountID: 1, Labels: []string{"fila-bot"}}

	// Primeira mensagem do cliente entra no debouncer.
	eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "Oi tudo bem?",
		Conversation: conv,
	})
	// Gatilho proativo (etiqueta recém-aplicada) chega logo em seguida, dentro da janela.
	eng.HandleConversationUpdated(context.Background(), &chatwoot.ConversationUpdated{
		Conversation: conv,
		ChangedAttributes: []map[string]chatwoot.ChangeValue{
			{"labels": {CurrentValue: []byte(`["fila-bot"]`), PreviousValue: []byte(`[]`)}},
		},
	})

	// Espera o flush completar (uma única resposta ao cliente).
	waitFor(t, 2*time.Second, func() bool {
		return len(cw.findAll(http.MethodPost, "/conversations/318/messages")) >= 1
	})
	// Dá folga para garantir que uma segunda resposta não apareça depois.
	time.Sleep(150 * time.Millisecond)

	calls, last := clf.snapshot()
	if calls != 1 {
		t.Fatalf("esperava 1 chamada ao LLM (gatilhos fundidos), got %d", calls)
	}
	if len(last) != 1 || last[0].Role != "customer" || last[0].Content != "Oi tudo bem?" {
		t.Fatalf("esperava um único turno do cliente com a mensagem, got %+v", last)
	}
	if msgs := cw.findAll(http.MethodPost, "/conversations/318/messages"); len(msgs) != 1 {
		t.Fatalf("esperava exatamente 1 resposta ao cliente (sem saudação dupla), got %d: %+v", len(msgs), msgs)
	}
}

// TestDebounce_ProactiveGreetingAloneStillGreets garante que, quando a etiqueta é
// aplicada numa conversa sem mensagem do cliente (o caso do atendente), a saudação
// proativa ainda acontece ao fim da janela.
func TestDebounce_ProactiveGreetingAloneStillGreets(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	clf := &recordingClassifier{intent: llm.Intent{Action: "reply", Reply: "Olá! Sou o Claudio."}}
	eng := newConfiguredEngine(t, cw, clf, nil, 50*time.Millisecond)

	conv := chatwoot.Conversation{ID: 319, AccountID: 1, Labels: []string{"fila-bot"}}
	eng.HandleConversationUpdated(context.Background(), &chatwoot.ConversationUpdated{
		Conversation: conv,
		ChangedAttributes: []map[string]chatwoot.ChangeValue{
			{"labels": {CurrentValue: []byte(`["fila-bot"]`), PreviousValue: []byte(`[]`)}},
		},
	})

	waitFor(t, 2*time.Second, func() bool {
		return len(cw.findAll(http.MethodPost, "/conversations/319/messages")) == 1
	})

	calls, last := clf.snapshot()
	if calls != 1 {
		t.Fatalf("esperava 1 chamada ao LLM (saudação proativa), got %d", calls)
	}
	if len(last) != 0 {
		t.Fatalf("saudação proativa não deve ter turnos do cliente, got %+v", last)
	}
}

// TestTriage_TranscribesAudio cobre a resposta a áudio: uma mensagem só com anexo de
// áudio é baixada, transcrita e o texto vira o turno do cliente na triagem.
func TestTriage_TranscribesAudio(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()

	audioBytes := []byte("FAKE-OPUS-AUDIO")
	audioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write(audioBytes)
	}))
	defer audioSrv.Close()

	clf := &recordingClassifier{intent: llm.Intent{Action: "reply", Reply: "ok"}}
	tr := &fakeTranscriber{text: "quero pagar meu boleto"}
	eng := newConfiguredEngine(t, cw, clf, tr, 0) // debounce off => síncrono

	eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "", // áudio não traz texto
		Attachments:  []chatwoot.Attachment{{FileType: "audio", DataURL: audioSrv.URL + "/a.ogg"}},
		Conversation: chatwoot.Conversation{ID: 200, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	calls, last := clf.snapshot()
	if calls != 1 {
		t.Fatalf("esperava 1 chamada ao LLM após transcrição, got %d", calls)
	}
	if len(last) != 1 || last[0].Content != "quero pagar meu boleto" {
		t.Errorf("esperava o texto transcrito como turno do cliente, got %+v", last)
	}
	tr.mu.Lock()
	got := string(tr.got)
	tr.mu.Unlock()
	if got != string(audioBytes) {
		t.Errorf("transcriber recebeu bytes diferentes do anexo baixado: %q", got)
	}
	if msgs := cw.findAll(http.MethodPost, "/conversations/200/messages"); len(msgs) != 1 || msgs[0].body["content"] != "ok" {
		t.Errorf("esperava a resposta do modelo, got %+v", msgs)
	}
}

// TestTriage_AudioTranscriptionFailureAsksToType cobre a falha de STT: o bot não
// chama o modelo e pede para o cliente digitar.
func TestTriage_AudioTranscriptionFailureAsksToType(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()

	audioSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer audioSrv.Close()

	clf := &recordingClassifier{intent: llm.Intent{Action: "reply", Reply: "não deveria ser enviado"}}
	tr := &fakeTranscriber{err: errors.New("boom")}
	eng := newConfiguredEngine(t, cw, clf, tr, 0)

	eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Attachments:  []chatwoot.Attachment{{FileType: "audio", DataURL: audioSrv.URL + "/a.ogg"}},
		Conversation: chatwoot.Conversation{ID: 201, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	if calls, _ := clf.snapshot(); calls != 0 {
		t.Errorf("não deveria chamar o LLM quando a transcrição falha, got %d", calls)
	}
	msgs := cw.findAll(http.MethodPost, "/conversations/201/messages")
	if len(msgs) != 1 || msgs[0].body["content"] != audioFallbackMsg {
		t.Errorf("esperava a mensagem de fallback pedindo texto, got %+v", msgs)
	}
}
