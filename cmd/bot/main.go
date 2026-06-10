// Command bot é o middleware de triagem para o Chatwoot v4.4.0.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/config"
	"bot-atendimento-promosystem/internal/cost"
	"bot-atendimento-promosystem/internal/engine"
	"bot-atendimento-promosystem/internal/llm"
	"bot-atendimento-promosystem/internal/scheduler"
	"bot-atendimento-promosystem/internal/store"
	"bot-atendimento-promosystem/internal/webhook"
)

func main() {
	// Logger de boot (nível fixo) até carregarmos o nível configurado.
	bootLog := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		bootLog.Error("config inválida", "err", err)
		os.Exit(1)
	}

	// Logger definitivo com o nível de LOG_LEVEL (use "debug" para ver payloads
	// completos enviados/recebidos do modelo e do Chatwoot).
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}))
	slog.SetDefault(log)
	log.Info("log inicializado", "level", cfg.LogLevel)

	st, err := store.NewSQLite(cfg.DBPath)
	if err != nil {
		log.Error("abrir store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	cw := chatwoot.NewClient(cfg.ChatwootBaseURL, cfg.ChatwootAccountID, cfg.ChatwootAPIToken)
	costTracker := cost.New(log)
	classifier := llm.NewOpenAI(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMTimeout, log, costTracker)

	var transcriber llm.Transcriber
	if cfg.STTEnabled() {
		switch cfg.STTStyle {
		case "openrouter":
			transcriber = llm.NewOpenRouterTranscriber(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel, cfg.STTTimeout, log, costTracker)
		default:
			transcriber = llm.NewOpenAITranscriber(cfg.STTBaseURL, cfg.STTAPIKey, cfg.STTModel, cfg.STTTimeout, log, costTracker)
		}
		log.Info("transcrição de áudio habilitada", "stt_style", cfg.STTStyle, "stt_model", cfg.STTModel, "stt_endpoint", cfg.STTBaseURL)
	} else {
		log.Info("transcrição de áudio desabilitada (STT_BASE_URL/STT_MODEL não definidos)")
	}

	eng, err := engine.New(cfg, st, cw, classifier, transcriber, log)
	if err != nil {
		log.Error("inicializar engine", "err", err)
		os.Exit(1)
	}

	if cfg.WebhookSecret == "" && cfg.WebhookToken == "" {
		log.Warn("webhook SEM autenticação: defina WEBHOOK_TOKEN (token na URL ?token=...) ou WEBHOOK_SECRET; do contrário, qualquer um que alcance /webhook pode injetar eventos")
	}
	log.Info("configure este URL no webhook do Chatwoot (Settings → Integrations → Webhooks)", "url", webhookURL(cfg))
	srv := webhook.NewServer(cfg.WebhookSecret, cfg.WebhookToken, st, eng, log, 4, 256)
	reaper := scheduler.New(st, cw, cfg.ReaperInterval, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go srv.Start(ctx)
	go reaper.Run(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", srv.ServeHTTP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("servidor iniciado", "port", cfg.Port, "sectors", cfg.SectorNames())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("encerrando...", "custo_total_usd_sessao", costTracker.Total())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}

// webhookURL monta a URL que deve ser cadastrada no webhook do Chatwoot, incluindo
// o token (?token=...) quando configurado. Usa PUBLIC_BASE_URL como host; se vazio,
// emprega um placeholder para lembrar o operador de informar o domínio público.
func webhookURL(cfg *config.Config) string {
	base := cfg.PublicBaseURL
	if base == "" {
		base = "https://<seu-host>"
	}
	u := base + "/webhook"
	if cfg.WebhookToken != "" {
		u += "?token=" + url.QueryEscape(cfg.WebhookToken)
	}
	return u
}

// parseLogLevel converte o valor de LOG_LEVEL em um slog.Level (default: info).
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
