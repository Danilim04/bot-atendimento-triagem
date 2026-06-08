// Package config carrega a configuração da aplicação a partir de variáveis de
// ambiente (com suporte opcional a um arquivo .env).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config agrega todos os parâmetros de execução do bot.
type Config struct {
	Port     string
	LogLevel string // debug | info | warn | error

	ChatwootBaseURL   string
	ChatwootAccountID string
	ChatwootAPIToken  string
	WebhookSecret     string

	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string
	LLMTimeout time.Duration

	DBPath string

	LabelBot           string
	LabelCSAT          string
	Sectors            map[string]string // chave retornada pelo LLM -> etiqueta da fila
	DefaultSectorLabel string            // fila humana padrão para fallback

	CSATTimeout    time.Duration
	CSATAttribute  string
	ReaperInterval time.Duration

	PromptPath string // opcional: sobrescreve o system prompt embutido
}

// Load lê o .env (se existir) e em seguida o ambiente, validando os campos
// obrigatórios.
func Load() (*Config, error) {
	_ = loadDotEnv(".env")

	cfg := &Config{
		Port:               getenv("PORT", "8080"),
		LogLevel:           strings.ToLower(getenv("LOG_LEVEL", "info")),
		ChatwootBaseURL:    strings.TrimRight(getenv("CHATWOOT_BASE_URL", ""), "/"),
		ChatwootAccountID:  getenv("CHATWOOT_ACCOUNT_ID", ""),
		ChatwootAPIToken:   getenv("CHATWOOT_API_TOKEN", ""),
		WebhookSecret:      getenv("WEBHOOK_SECRET", ""),
		LLMBaseURL:         strings.TrimRight(getenv("LLM_BASE_URL", "https://api.openai.com/v1"), "/"),
		LLMAPIKey:          getenv("LLM_API_KEY", ""),
		LLMModel:           getenv("LLM_MODEL", "gpt-4o-mini"),
		DBPath:             getenv("DB_PATH", "bot.db"),
		LabelBot:           getenv("LABEL_BOT", "fila-bot"),
		LabelCSAT:          getenv("LABEL_CSAT", "fila-csat"),
		DefaultSectorLabel: getenv("DEFAULT_SECTOR_LABEL", "fila-humano"),
		CSATAttribute:      getenv("CSAT_ATTRIBUTE", "csat_rating"),
		PromptPath:         getenv("PROMPT_PATH", ""),
		Sectors:            parseSectors(getenv("SECTORS", "")),
	}

	var err error
	if cfg.CSATTimeout, err = parseDuration(getenv("CSAT_TIMEOUT", "5m")); err != nil {
		return nil, fmt.Errorf("CSAT_TIMEOUT inválido: %w", err)
	}
	if cfg.LLMTimeout, err = parseDuration(getenv("LLM_TIMEOUT", "30s")); err != nil {
		return nil, fmt.Errorf("LLM_TIMEOUT inválido: %w", err)
	}
	if cfg.ReaperInterval, err = parseDuration(getenv("REAPER_INTERVAL", "15s")); err != nil {
		return nil, fmt.Errorf("REAPER_INTERVAL inválido: %w", err)
	}

	var missing []string
	if cfg.ChatwootBaseURL == "" {
		missing = append(missing, "CHATWOOT_BASE_URL")
	}
	if cfg.ChatwootAccountID == "" {
		missing = append(missing, "CHATWOOT_ACCOUNT_ID")
	}
	if cfg.ChatwootAPIToken == "" {
		missing = append(missing, "CHATWOOT_API_TOKEN")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("variáveis obrigatórias ausentes: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// SectorLabel devolve a etiqueta configurada para um setor retornado pelo LLM,
// ou a fila humana padrão quando o setor é desconhecido.
func (c *Config) SectorLabel(sector string) (label string, known bool) {
	if l, ok := c.Sectors[strings.ToLower(strings.TrimSpace(sector))]; ok {
		return l, true
	}
	return c.DefaultSectorLabel, false
}

// SectorNames devolve as chaves de setor válidas (para montar o prompt do LLM).
func (c *Config) SectorNames() []string {
	names := make([]string, 0, len(c.Sectors))
	for k := range c.Sectors {
		names = append(names, k)
	}
	return names
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// parseSectors interpreta "financeiro=fila-financeiro,suporte=fila-suporte".
func parseSectors(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// loadDotEnv faz um carregamento mínimo de um arquivo .env (KEY=VALUE por linha,
// linhas iniciadas por # são ignoradas). Não sobrescreve variáveis já definidas.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}
