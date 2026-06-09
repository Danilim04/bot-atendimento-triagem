package chatwoot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxAttachmentBytes limita o tamanho de um anexo baixado (ex.: áudio para STT).
const maxAttachmentBytes = 25 << 20 // 25 MiB

// Client é o cliente REST de saída para a Application API do Chatwoot.
type Client struct {
	baseURL   string
	accountID string
	token     string
	http      *http.Client
	maxRetry  int
}

// NewClient cria um cliente para uma conta específica.
func NewClient(baseURL, accountID, token string) *Client {
	return &Client{
		baseURL:   baseURL,
		accountID: accountID,
		token:     token,
		http:      &http.Client{Timeout: 15 * time.Second},
		maxRetry:  2,
	}
}

// SelectOption é uma opção de um content_type input_select (ex.: notas 1–5).
type SelectOption struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

func (c *Client) convPath(convID int64, suffix string) string {
	return fmt.Sprintf("%s/api/v1/accounts/%s/conversations/%d%s", c.baseURL, c.accountID, convID, suffix)
}

// GetLabels devolve as etiquetas atuais da conversa.
func (c *Client) GetLabels(ctx context.Context, convID int64) ([]string, error) {
	var resp struct {
		Payload []string `json:"payload"`
	}
	if err := c.do(ctx, http.MethodGet, c.convPath(convID, "/labels"), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Payload, nil
}

// SetLabels substitui o conjunto completo de etiquetas da conversa (semântica de
// "set" da API do Chatwoot).
func (c *Client) SetLabels(ctx context.Context, convID int64, labels []string) error {
	if labels == nil {
		labels = []string{}
	}
	body := map[string]any{"labels": labels}
	return c.do(ctx, http.MethodPost, c.convPath(convID, "/labels"), body, nil)
}

// SendMessage cria uma mensagem na conversa. private=true gera uma nota interna.
func (c *Client) SendMessage(ctx context.Context, convID int64, content string, private bool) error {
	body := map[string]any{
		"content":      content,
		"message_type": "outgoing",
		"private":      private,
	}
	return c.do(ctx, http.MethodPost, c.convPath(convID, "/messages"), body, nil)
}

// SendInputSelect envia uma mensagem interativa com opções selecionáveis.
func (c *Client) SendInputSelect(ctx context.Context, convID int64, content string, options []SelectOption) error {
	body := map[string]any{
		"content":      content,
		"message_type": "outgoing",
		"private":      false,
		"content_type": "input_select",
		"content_attributes": map[string]any{
			"items": options,
		},
	}
	return c.do(ctx, http.MethodPost, c.convPath(convID, "/messages"), body, nil)
}

// ToggleStatus altera o status da conversa (open/resolved/pending/snoozed).
func (c *Client) ToggleStatus(ctx context.Context, convID int64, status string) error {
	body := map[string]any{"status": status}
	return c.do(ctx, http.MethodPost, c.convPath(convID, "/toggle_status"), body, nil)
}

// SetCustomAttributes grava/atualiza atributos personalizados na conversa.
func (c *Client) SetCustomAttributes(ctx context.Context, convID int64, attrs map[string]any) error {
	body := map[string]any{"custom_attributes": attrs}
	return c.do(ctx, http.MethodPost, c.convPath(convID, "/custom_attributes"), body, nil)
}

// DownloadAttachment baixa o conteúdo de um anexo (ex.: áudio) a partir da sua
// data_url. URLs públicas de ActiveStorage ignoram o header de auth; o token é
// enviado mesmo assim para instâncias que exijam autenticação. Devolve os bytes e
// o Content-Type da resposta.
func (c *Client) DownloadAttachment(ctx context.Context, rawURL string) ([]byte, string, error) {
	url := rawURL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = c.baseURL + rawURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("api_access_token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download anexo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, "", fmt.Errorf("download anexo %s: status %d: %s", url, resp.StatusCode, b)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes))
	if err != nil {
		return nil, "", fmt.Errorf("ler anexo: %w", err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// do executa a requisição com (de)serialização JSON e retries em erros
// transientes (5xx / falhas de rede).
func (c *Client) do(ctx context.Context, method, url string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("api_access_token", c.token)

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("chatwoot %s %s: status %d: %s", method, url, resp.StatusCode, bodyBytes)
			continue // transiente: retry
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("chatwoot %s %s: status %d: %s", method, url, resp.StatusCode, bodyBytes)
		}
		if out != nil && len(bodyBytes) > 0 {
			if err := json.Unmarshal(bodyBytes, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	return fmt.Errorf("chatwoot request falhou após %d tentativas: %w", c.maxRetry+1, lastErr)
}
