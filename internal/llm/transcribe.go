package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
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
}

// NewOpenAITranscriber cria um transcritor OpenAI-compatible.
func NewOpenAITranscriber(baseURL, apiKey, model string, timeout time.Duration, log *slog.Logger) *OpenAITranscriber {
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

	text := strings.TrimSpace(tr.Text)
	t.log.Info("stt: áudio transcrito", "model", t.model, "latency_ms", latency.Milliseconds(), "chars", len(text))
	return text, nil
}
