package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAIClassifier implementa Classifier sobre qualquer endpoint compatível com
// a API /chat/completions da OpenAI (OpenAI, Groq, OpenRouter, vLLM, Ollama...).
type OpenAIClassifier struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	log     *slog.Logger
}

// NewOpenAI cria um classificador OpenAI-compatible.
func NewOpenAI(baseURL, apiKey, model string, timeout time.Duration, log *slog.Logger) *OpenAIClassifier {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &OpenAIClassifier{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
		log:     log,
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Classify monta a conversa (system + turnos) e parseia a resposta JSON em Intent.
func (o *OpenAIClassifier) Classify(ctx context.Context, systemPrompt string, history []Turn) (Intent, error) {
	msgs := make([]chatMessage, 0, len(history)+1)
	msgs = append(msgs, chatMessage{Role: "system", Content: systemPrompt})
	for _, t := range history {
		role := "user"
		if t.Role == "bot" {
			role = "assistant"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: t.Content})
	}

	reqBody := chatRequest{
		Model:          o.model,
		Messages:       msgs,
		Temperature:    0,
		ResponseFormat: map[string]any{"type": "json_object"},
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Intent{}, err
	}

	// Resumo no nível info (sempre visível); payload completo só em debug.
	o.log.Info("llm: enviando ao modelo",
		"model", o.model,
		"endpoint", o.baseURL+"/chat/completions",
		"num_mensagens", len(msgs),
		"ultima_msg", lastUserContent(history),
	)
	o.log.Debug("llm: payload da requisição", "body", string(payload))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Intent{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	start := time.Now()
	resp, err := o.http.Do(req)
	if err != nil {
		o.log.Error("llm: falha na requisição", "model", o.model, "latency_ms", time.Since(start).Milliseconds(), "err", err)
		return Intent{}, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latency := time.Since(start)

	o.log.Debug("llm: resposta bruta", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))

	if resp.StatusCode >= 400 {
		o.log.Error("llm: status de erro", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))
		return Intent{}, fmt.Errorf("llm status %d: %s", resp.StatusCode, body)
	}

	var cr chatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return Intent{}, fmt.Errorf("decode llm response: %w", err)
	}
	if cr.Error != nil {
		o.log.Error("llm: erro retornado pelo modelo", "err", cr.Error.Message)
		return Intent{}, fmt.Errorf("llm error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Intent{}, fmt.Errorf("llm sem choices")
	}

	raw := cr.Choices[0].Message.Content
	content := extractJSON(raw)
	var intent Intent
	if err := json.Unmarshal([]byte(content), &intent); err != nil {
		o.log.Error("llm: intent inválido", "raw", raw, "err", err)
		return Intent{}, fmt.Errorf("intent inválido %q: %w", content, err)
	}

	o.log.Info("llm: resposta do modelo",
		"model", o.model,
		"latency_ms", latency.Milliseconds(),
		"action", intent.Action,
		"sector", intent.Sector,
		"reply", intent.Reply,
		"reason", intent.Reason,
		"raw", raw,
	)
	return intent, nil
}

// lastUserContent devolve o conteúdo do último turno do cliente, para o resumo
// de log da requisição ao modelo.
func lastUserContent(history []Turn) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "customer" {
			return history[i].Content
		}
	}
	return ""
}

// extractJSON isola o objeto JSON, mesmo se o modelo envolver em ```json ... ```.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
