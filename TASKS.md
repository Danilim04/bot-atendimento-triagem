# TASKS — Bot de Triagem Chatwoot (Go)

> Tracker persistente de implementação. Mantenha em sincronia ao iniciar/concluir cada etapa.
> Design completo (SDD): `~/.claude/plans/contexto-e-objetivo-do-cuddly-cerf.md`
> Convenção de status: `[ ]` pendente · `[~]` em andamento · `[x]` concluído

## Contexto rápido
Middleware Go = bot de triagem para **Chatwoot v4.4.0**. FSM rígida: identifica cliente →
classifica intenção via LLM → roteia por etiquetas → CSAT (1–5) com timeout de 5 min.
Decisões fixadas: **SQLite + reaper** (durável), **LLM OpenAI-compatible**, CSAT em
**custom attribute `csat_rating` + nota privada**. Go 1.26.4 em `/usr/local/go/bin/go`.
Módulo: `bot-atendimento-promosystem`. Diretório greenfield.

## Decisões de arquitetura (não reabrir sem motivo)
- Handler HTTP responde `200` rápido → verify assinatura → dedup → worker pool assíncrono.
- `POST /labels` do Chatwoot **substitui** o conjunto inteiro → roteamento lê labels atuais
  (vêm no webhook) e reenvia a lista desejada.
- `message_type` no webhook pode vir **int (0..3) OU string** → tipo `MessageType` flexível.
- Timeout CSAT via tabela `csat_deadlines` + goroutine `reaper` (ticker 15s, claim atômico),
  **não** `time.AfterFunc`. Reconcilia no boot.
- Filtro de borda: só processa `message_created` com `incoming` + `sender.type=="contact"` +
  `private==false`, e conversas com label de controle (`fila-bot`/`fila-csat`).

## Revisão de arquitetura (2026-06-05) — IA orquestra a triagem
Revisada a decisão original "FSM rígida, LLM só classificador": **a IA agora conduz a
conversa de triagem** turno a turno (saudar/perguntar/rotear), via contrato
`{action: reply|route, reply, sector, reason}`. A **saudação é contextual** (gerada
pelo modelo — não há mais texto fixo nem caso de borda `firstContact`). Entrada
unificada em `engine.handleTriage(ctx, conv, customer *string)` (`customer==nil` =
saudação proativa ao aplicar a etiqueta). O **código mantém o andaime durável e os
guarda-corpos**: CSAT + reaper + timeout intactos, dedup, mecânica de etiquetas,
validação de setor + fila padrão, disjuntor `maxTriageTurns`. Plano:
`~/.claude/plans/talvez-seja-melhor-se-keen-snowglobe.md`. Logs detalhados +
`LOG_LEVEL` configurável também adicionados.

## Tasks
- [x] **1. Bootstrap** — `go.mod` (module `bot-atendimento-promosystem`), `internal/config/config.go`
  (env vars: CHATWOOT_BASE_URL, CHATWOOT_ACCOUNT_ID, CHATWOOT_API_TOKEN, WEBHOOK_SECRET,
  LLM_BASE_URL, LLM_API_KEY, LLM_MODEL, DB_PATH, PORT, LABEL_BOT, LABEL_CSAT, SECTORS,
  CSAT_TIMEOUT=5m, CSAT_ATTRIBUTE=csat_rating), `.env.example`, `.gitignore`.
- [x] **2. Webhook structs + signature** — `internal/chatwoot/types.go`
  (MessageCreated, ConversationUpdated, Conversation, Sender, Account, Inbox, Meta, Change;
  `MessageType` com UnmarshalJSON flexível). `internal/chatwoot/signature.go` (HMAC-SHA256,
  comparação em tempo constante). `types_test.go`, `signature_test.go`.
- [x] **3. REST client** — `internal/chatwoot/client.go`: GetLabels, SetLabels, SendMessage,
  SendInputSelect (1–5), ToggleStatus, SetCustomAttributes, helper RouteToSector
  (remove fromLabel, add toLabel). Timeout + retries em 5xx/transientes.
- [x] **4. SQLite store** — `internal/store/store.go` (interface) + `sqlite.go`
  (`modernc.org/sqlite`, CGO-free). Tabelas: conversation_state, csat_deadlines (claim atômico
  via UPDATE condicional), processed_events (dedup por X-Chatwoot-Delivery). Migrations inline.
- [x] **5. LLM classifier** — `internal/llm/classifier.go` (interface Classifier + tipo Intent
  {action: reply|route, reply, sector, reason}) + `openai.go` (/chat/completions, JSON mode,
  temp 0, com logs de I/O). `internal/engine/system_prompt.txt`: condutor de triagem (saúda,
  pergunta curta, roteia), setores válidos da config, proíbe sair do escopo, só JSON.
  Validação: sector fora da lista → fila default. (Ver Revisão de arquitetura 2026-06-05.)
- [x] **6. Engine FSM** — `internal/engine/{fsm.go,triage.go,csat.go}`. Estados:
  IDLE, TRIAGE, ROUTED, CSAT_PENDING, CSAT_DONE, CLOSED_TIMEOUT. Triagem conduzida pela IA
  via `handleTriage` (saudação contextual + perguntas + route decididos pelo modelo;
  guarda-corpos no código); CSAT parseia 1–5 (texto ou input_select), inválido → reforço
  sem reiniciar timer.
- [x] **7. Webhook HTTP layer** — `internal/webhook/{handler.go,dispatcher.go}`:
  verify → dedup → 200 imediato → enfileira em worker pool (canal bufferizado) → dispatcher
  roteia evento para a engine.
- [x] **8. Reaper** — `internal/scheduler/reaper.go`: ticker 15s, SELECT deadlines vencidos,
  claim atômico, ToggleStatus=resolved idempotente, reconciliação no boot.
- [x] **9. Wiring** — `cmd/bot/main.go`: carrega config, abre DB+migrate, instancia client/llm/
  engine, sobe HTTP server + reaper, graceful shutdown (signal + context).
- [x] **10. Integração + docs** — testes com Chatwoot fake (`httptest`) cobrindo
  triagem→roteamento e CSAT respondido/timeout; `README.md` (setup, env, config de webhook no
  Chatwoot, etiquetas). `go vet ./...` e `go build ./...` limpos.

## Como retomar (qualquer sessão)
1. Ler este arquivo + o SDD para reconstruir contexto.
2. Conferir o que já existe: `find . -name '*.go' | sort`.
3. Continuar pela primeira task `[ ]`/`[~]`. Build de sanidade:
   `PATH=$PATH:/usr/local/go/bin go build ./... && go vet ./... && go test ./...`.
