#!/usr/bin/env bash
#
# deploy.sh — build da imagem + deploy do stack no Docker Swarm.
#
# O Docker Swarm (docker stack deploy) NÃO lê env_file/.env como o compose:
# ele só interpola ${VAR} a partir do ambiente do shell. Por isso este script
# exporta o .env (set -a; source; set +a) antes de subir o stack.
#
# Uso:
#   ./deploy.sh
#
# Variáveis opcionais:
#   STACK_NAME  nome do stack    (padrão: bot)
#   IMAGE       tag da imagem    (padrão: bot-atendimento-triagem:latest)
#   ENV_FILE    arquivo de env   (padrão: .env)
#   NO_BUILD=1  pula o build (reaproveita a imagem existente)

set -euo pipefail
cd "$(dirname "$0")"

STACK_NAME="${STACK_NAME:-bot}"
IMAGE="${IMAGE:-bot-atendimento-triagem:latest}"
ENV_FILE="${ENV_FILE:-.env}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[erro]\033[0m %s\n' "$*" >&2; exit 1; }

# .env precisa existir; o caminho leva barra para o 'source' não procurar no PATH.
[ -f "$ENV_FILE" ] || die "arquivo de ambiente não encontrado: $ENV_FILE"
case "$ENV_FILE" in */*) ;; *) ENV_FILE="./$ENV_FILE" ;; esac

# 1. Build da imagem (Swarm não builda no stack deploy).
if [ "${NO_BUILD:-0}" = "1" ]; then
  log "NO_BUILD=1 — pulando build, usando imagem existente ${IMAGE}"
else
  log "Build da imagem ${IMAGE}..."
  docker build -t "$IMAGE" .
fi

# 2. Exporta o .env para o ambiente (allexport).
log "Exportando variáveis de ${ENV_FILE} para o ambiente..."
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

# 3. Deploy do stack.
log "Subindo stack '${STACK_NAME}'..."
docker stack deploy -c stack.yml "$STACK_NAME"

log "Pronto. Acompanhe com:"
printf '    docker stack services %s\n' "$STACK_NAME"
printf '    docker service logs -f %s_bot\n' "$STACK_NAME"
if [ -n "${WEBHOOK_TOKEN:-}" ]; then
  printf '\nWebhook para cadastrar no Chatwoot:\n    https://%s/webhook?token=%s\n' \
    "${BOT_HOST:-bot-atendimento.oivox.com.br}" "$WEBHOOK_TOKEN"
fi
