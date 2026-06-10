package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"bot-atendimento-promosystem/internal/cost"
)

// Transcriber converte áudio em texto (speech-to-text).
type Transcriber interface {
	// Transcribe transcreve o áudio bruto. filename deve ter uma extensão válida
	// (ex.: audio.ogg) para o provedor inferir o formato; language é opcional
	// (ex.: "pt").
	Transcribe(ctx context.Context, audio []byte, filename, language string) (string, error)
}

// OpenAITranscriber implementa Transcriber sobre qualquer endpoint compatível com
// a API /audio/transcriptions da OpenAI (OpenAI whisper-1, Groq whisper-large-v3...).
type OpenAITranscriber struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	log     *slog.Logger
	cost    *cost.Tracker
}

// NewOpenAITranscriber cria um transcritor OpenAI-compatible. tracker (opcional,
// pode ser nil) acumula o custo reportado pelo provedor em usage.cost.
func NewOpenAITranscriber(baseURL, apiKey, model string, timeout time.Duration, log *slog.Logger, tracker *cost.Tracker) *OpenAITranscriber {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &OpenAITranscriber{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
		log:     log,
		cost:    tracker,
	}
}

// Transcribe envia o áudio em multipart/form-data e devolve o texto transcrito.
func (t *OpenAITranscriber) Transcribe(ctx context.Context, audio []byte, filename, language string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audio); err != nil {
		return "", err
	}
	if err := mw.WriteField("model", t.model); err != nil {
		return "", err
	}
	if language != "" {
		if err := mw.WriteField("language", language); err != nil {
			return "", err
		}
	}
	if err := mw.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	endpoint := t.baseURL + "/audio/transcriptions"
	t.log.Info("stt: enviando áudio para transcrição",
		"model", t.model, "endpoint", endpoint, "bytes", len(audio), "filename", filename, "language", language)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	start := time.Now()
	resp, err := t.http.Do(req)
	if err != nil {
		t.log.Error("stt: falha na requisição", "model", t.model, "latency_ms", time.Since(start).Milliseconds(), "err", err)
		return "", fmt.Errorf("stt request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latency := time.Since(start)

	t.log.Debug("stt: resposta bruta", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))

	if resp.StatusCode >= 400 {
		t.log.Error("stt: status de erro", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))
		return "", fmt.Errorf("stt status %d: %s", resp.StatusCode, body)
	}

	var tr struct {
		Text  string `json:"text"`
		Usage *struct {
			Cost float64 `json:"cost"` // USD (OpenRouter devolve por padrão; 0 nos demais)
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decode stt response: %w", err)
	}
	if tr.Error != nil {
		return "", fmt.Errorf("stt error: %s", tr.Error.Message)
	}

	var costUSD float64
	if tr.Usage != nil {
		costUSD = tr.Usage.Cost
	}
	text := strings.TrimSpace(tr.Text)
	t.log.Info("stt: áudio transcrito", "model", t.model, "latency_ms", latency.Milliseconds(), "chars", len(text), "cost_usd", costUSD)
	t.cost.Add("stt", costUSD)
	return text, nil
}

// OpenRouterTranscriber implementa Transcriber sobre o endpoint de STT do
// OpenRouter, cujo formato difere do padrão OpenAI: o áudio vai em JSON, codificado
// em base64 (input_audio.data + format), em vez de multipart/form-data. O modelo
// também precisa do prefixo do provedor (ex.: "openai/whisper-large-v3").
type OpenRouterTranscriber struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	log     *slog.Logger
	cost    *cost.Tracker
}

// NewOpenRouterTranscriber cria um transcritor para a API de STT do OpenRouter.
// tracker (opcional, pode ser nil) acumula o custo reportado em usage.cost.
func NewOpenRouterTranscriber(baseURL, apiKey, model string, timeout time.Duration, log *slog.Logger, tracker *cost.Tracker) *OpenRouterTranscriber {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &OpenRouterTranscriber{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: timeout},
		log:     log,
		cost:    tracker,
	}
}

// Transcribe envia o áudio como JSON (base64) e devolve o texto transcrito.
func (t *OpenRouterTranscriber) Transcribe(ctx context.Context, audio []byte, filename, language string) (string, error) {
	format := audioFormat(filename)
	reqBody := map[string]any{
		"model": t.model,
		"input_audio": map[string]string{
			"data":   base64.StdEncoding.EncodeToString(audio),
			"format": format,
		},
	}
	if language != "" {
		reqBody["language"] = language
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	endpoint := t.baseURL + "/audio/transcriptions"
	t.log.Info("stt: enviando áudio para transcrição (openrouter)",
		"model", t.model, "endpoint", endpoint, "bytes", len(audio), "filename", filename, "format", format, "language", language)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	start := time.Now()
	resp, err := t.http.Do(req)
	if err != nil {
		t.log.Error("stt: falha na requisição", "model", t.model, "latency_ms", time.Since(start).Milliseconds(), "err", err)
		return "", fmt.Errorf("stt request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	latency := time.Since(start)

	t.log.Debug("stt: resposta bruta", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))

	if resp.StatusCode >= 400 {
		t.log.Error("stt: status de erro", "status", resp.StatusCode, "latency_ms", latency.Milliseconds(), "body", string(body))
		return "", fmt.Errorf("stt status %d: %s", resp.StatusCode, body)
	}

	var tr struct {
		Text  string `json:"text"`
		Usage *struct {
			Cost float64 `json:"cost"` // USD (OpenRouter devolve por padrão; 0 nos demais)
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decode stt response: %w", err)
	}
	if tr.Error != nil {
		return "", fmt.Errorf("stt error: %s", tr.Error.Message)
	}

	var costUSD float64
	if tr.Usage != nil {
		costUSD = tr.Usage.Cost
	}
	text := strings.TrimSpace(tr.Text)
	t.log.Info("stt: áudio transcrito", "model", t.model, "latency_ms", latency.Milliseconds(), "chars", len(text), "cost_usd", costUSD)
	t.cost.Add("stt", costUSD)
	return text, nil
}

// audioFormat deriva o campo "format" exigido pelo OpenRouter a partir da extensão
// do nome de arquivo. Voice notes do WhatsApp chegam como .oga (Ogg/Opus) -> "ogg".
func audioFormat(filename string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(filename), "."))
	switch ext {
	case "", "oga", "opus":
		return "ogg"
	case "mpga":
		return "mp3"
	default:
		return ext
	}
}
