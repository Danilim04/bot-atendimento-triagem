# syntax=docker/dockerfile:1

# ---------- build ----------
# Pinado na versão do go.mod (go 1.26.4) para builds reprodutíveis.
FROM golang:1.26.4-alpine AS build

WORKDIR /src

# Baixa dependências primeiro (camada cacheada enquanto go.mod/go.sum não mudam).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Código-fonte e compilação. CGO desligado: o driver SQLite é Go puro
# (modernc.org/sqlite), então geramos um binário estático que roda em qualquer
# imagem mínima (alpine/scratch/distroless).
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/bot ./cmd/bot

# ---------- runtime ----------
FROM alpine:3.20

# ca-certificates: chamadas HTTPS p/ Chatwoot e LLM/STT. tzdata: durações/horários.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app \
    && mkdir -p /data \
    && chown app:app /data

COPY --from=build /out/bot /usr/local/bin/bot

# /data é o volume persistente do SQLite. WORKDIR aqui garante que mesmo um
# DB_PATH relativo (ex.: "bot.db") caia no volume.
WORKDIR /data
VOLUME ["/data"]

# Defaults; podem ser sobrescritos por env_file/environment no compose.
ENV PORT=8080 \
    DB_PATH=/data/bot.db

USER app
EXPOSE 8080

# Forma shell para expandir $PORT em tempo de execução.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/health" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/bot"]