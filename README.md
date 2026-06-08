# Bot de Triagem — Chatwoot v4.4.0

Middleware em Go que atua como **bot de triagem** integrado ao Chatwoot. Não é um
chatbot conversacional: é uma **máquina de estados rígida** com três pilares:

1. **Identificação / gatilho** — reage a webhooks apenas em conversas com etiquetas de controle.
2. **Triagem por LLM** — um classificador de intenção (OpenAI-compatible) decide o setor e o bot
   **roteia trocando etiquetas** via API REST.
3. **CSAT com timeout** — ao receber a etiqueta de CSAT, pede nota 1–5 e aguarda **5 minutos**:
   respondeu → registra e resolve; estourou → resolve por timeout.

## Arquitetura

```
Chatwoot ─webhook→ handler (verifica HMAC, deduplica, responde 200) ─→ worker pool ─→ engine (FSM)
                                                                                         ├─ store (SQLite)
                                                                                         ├─ classifier (LLM)
                                                                                         └─ chatwoot client (REST)
reaper (goroutine, ticker 15s) ─→ fecha conversas de CSAT com prazo vencido (durável em SQLite)
```

| Pacote | Responsabilidade |
|--------|------------------|
| `internal/config`    | Carrega config de env/.env |
| `internal/chatwoot`  | Structs de webhook, verificação HMAC, cliente REST |
| `internal/store`     | SQLite: estado da FSM, prazos de CSAT, dedup de eventos |
| `internal/llm`       | Interface `Classifier` + implementação OpenAI-compatible |
| `internal/engine`    | FSM: triagem e CSAT |
| `internal/webhook`   | Handler HTTP + pool de workers |
| `internal/scheduler` | Reaper de timeout do CSAT |
| `cmd/bot`            | Wiring + graceful shutdown |

## Estados da conversa

`idle → triage → routed` (triagem) e `csat_pending → csat_done | closed_timeout` (CSAT).

## Configuração

Copie `.env.example` para `.env` e preencha. Variáveis principais:

| Var | Descrição |
|-----|-----------|
| `CHATWOOT_BASE_URL`, `CHATWOOT_ACCOUNT_ID`, `CHATWOOT_API_TOKEN` | Acesso à API (obrigatórios) |
| `WEBHOOK_SECRET` | Segredo HMAC do webhook (vazio desabilita verificação — só em dev) |
| `LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL` | Endpoint OpenAI-compatible |
| `LABEL_BOT`, `LABEL_CSAT` | Etiquetas de controle (ex.: `fila-bot`, `fila-csat`) |
| `SECTORS` | `chave=etiqueta` por vírgula (ex.: `financeiro=fila-financeiro,suporte=fila-suporte`) |
| `DEFAULT_SECTOR_LABEL` | Fila humana padrão (fallback) |
| `CSAT_TIMEOUT` | Janela de resposta (default `5m`) |
| `CSAT_ATTRIBUTE` | Custom attribute onde a nota é gravada |

## Configurar no Chatwoot

1. Crie as etiquetas (Labels): a do bot (`fila-bot`), a de CSAT (`fila-csat`) e uma por setor.
2. Crie um **custom attribute** de conversa com a chave `csat_rating` (tipo número).
3. Em **Settings → Integrations → Webhooks**, aponte para `https://SEU_HOST/webhook` e assine os
   eventos **Message created** e **Conversation updated**. Defina o mesmo `WEBHOOK_SECRET`.
4. Fluxo operacional:
   - Aplique `fila-bot` numa conversa → o bot inicia a triagem e roteia trocando a etiqueta.
   - Quando o atendente terminar, ele aplica `fila-csat` → o bot pede a nota e fecha (resposta ou timeout).

## Como rodar

```bash
export PATH=$PATH:/usr/local/go/bin   # Go 1.26 instalado aqui
cp .env.example .env                  # edite com seus valores
go run ./cmd/bot                      # sobe em :8080 (/webhook e /health)
```

## Como começar os testes

### 1. Suíte automatizada (sem rede, sem Chatwoot real)

```bash
export PATH=$PATH:/usr/local/go/bin
go test ./...            # tudo
go test ./... -v         # verboso
go test ./internal/engine -run TestCSAT -v   # só o fluxo de CSAT
```

Cobre: parse flexível de `message_type` (int/string), HMAC, roteamento por etiquetas,
parse de nota 1–5, fluxo de CSAT (com Chatwoot fake via `httptest`) e o reaper de timeout.

### 2. Smoke local com `curl` (simulando o Chatwoot)

Suba o bot com `WEBHOOK_SECRET=` vazio (pula HMAC) e um `LLM_*` válido, depois envie eventos:

```bash
# Inicia triagem (etiqueta fila-bot aplicada)
curl -X POST localhost:8080/webhook \
  -H 'Content-Type: application/json' \
  -d '{"event":"conversation_updated","id":567,"account_id":1,"status":"open","labels":["fila-bot"]}'

# Mensagem do cliente (dispara o classificador e o roteamento)
curl -X POST localhost:8080/webhook \
  -H 'Content-Type: application/json' \
  -d '{"event":"message_created","message_type":0,"private":false,
       "content":"quero pagar meu boleto",
       "sender":{"type":"contact"},
       "conversation":{"id":567,"account_id":1,"labels":["fila-bot"]}}'

# Inicia CSAT (etiqueta fila-csat) e responde com a nota
curl -X POST localhost:8080/webhook -H 'Content-Type: application/json' \
  -d '{"event":"conversation_updated","id":800,"account_id":1,"labels":["fila-csat"]}'
curl -X POST localhost:8080/webhook -H 'Content-Type: application/json' \
  -d '{"event":"message_created","message_type":0,"content":"5","sender":{"type":"contact"},
       "conversation":{"id":800,"account_id":1,"labels":["fila-csat"]}}'
```

As chamadas REST resultantes vão para `CHATWOOT_BASE_URL`; use uma conta de teste real ou aponte
para um mock. Os logs (JSON em stdout) mostram cada transição de estado.

### 3. Túnel para um Chatwoot real

```bash
ngrok http 8080   # use a URL pública em Settings → Integrations → Webhooks
```

Depois percorra o fluxo na UI: aplique `fila-bot`, converse, e ao final aplique `fila-csat`.
```

## Notas de design

- `POST /labels` do Chatwoot **substitui** o conjunto de etiquetas — o roteamento lê as etiquetas
  que vêm no webhook e reenvia a lista desejada.
- O timeout do CSAT é **durável**: persistido em SQLite e processado pelo reaper, sobrevivendo a
  reinicializações (não usa `time.AfterFunc` em memória).
- Assume **uma instância** (o reaper não usa lock distribuído).
