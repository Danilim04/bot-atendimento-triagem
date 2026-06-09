#!/usr/bin/env bash
#
# install-go.sh — baixa e instala SEMPRE a versão estável mais recente do Go.
#
# Uso:
#   ./scripts/install-go.sh                 # instala em /usr/local (padrão oficial)
#   GO_INSTALL_DIR=$HOME/.local ./scripts/install-go.sh   # instala sem sudo
#
# Variáveis de ambiente:
#   GO_INSTALL_DIR  diretório base onde o Go será extraído (cria $DIR/go).
#                   Padrão: /usr/local  (usa sudo se não tiver permissão).
#
# O script:
#   1. Descobre a versão estável mais recente em https://go.dev/VERSION
#   2. Detecta SO/arquitetura automaticamente
#   3. Baixa o tarball oficial e confere o checksum SHA-256
#   4. Remove a instalação antiga e extrai a nova
#   5. Mostra como colocar o Go no PATH

set -euo pipefail

INSTALL_DIR="${GO_INSTALL_DIR:-/usr/local}"
GO_ROOT="${INSTALL_DIR}/go"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[aviso]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[erro]\033[0m %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "comando obrigatório não encontrado: $1"; }

# --- dependências -----------------------------------------------------------
need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1"; }
  fetch_to() { curl -fSL --progress-bar "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO- "$1"; }
  fetch_to() { wget -q --show-progress "$1" -O "$2"; }
else
  die "preciso de 'curl' ou 'wget' instalado"
fi

# --- detecta SO/arquitetura -------------------------------------------------
case "$(uname -s)" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) die "SO não suportado por este script: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)        ARCH=amd64 ;;
  aarch64|arm64)       ARCH=arm64 ;;
  armv6l)              ARCH=armv6l ;;
  i386|i686)           ARCH=386 ;;
  *) die "arquitetura não suportada: $(uname -m)" ;;
esac

# --- versão mais recente ----------------------------------------------------
log "Descobrindo a versão estável mais recente do Go..."
VERSION="$(fetch 'https://go.dev/VERSION?m=text' | head -n1)"
[ -n "$VERSION" ] || die "não consegui obter a versão mais recente"
log "Versão mais recente: ${VERSION}"

# Já está instalada?
if [ -x "${GO_ROOT}/bin/go" ]; then
  CURRENT="$("${GO_ROOT}/bin/go" version | awk '{print $3}')"
  if [ "$CURRENT" = "$VERSION" ]; then
    log "Go ${VERSION} já está instalado em ${GO_ROOT}. Nada a fazer."
    exit 0
  fi
  log "Instalação atual: ${CURRENT} — atualizando para ${VERSION}."
fi

TARBALL="${VERSION}.${OS}-${ARCH}.tar.gz"
URL="https://go.dev/dl/${TARBALL}"

# --- download ---------------------------------------------------------------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

log "Baixando ${URL}"
fetch_to "$URL" "${TMP}/${TARBALL}"

# --- checksum SHA-256 -------------------------------------------------------
# O checksum oficial vem da API JSON (go.dev não serve mais arquivos .sha256).
sha256_oficial() {
  local json
  json="$(fetch 'https://go.dev/dl/?mode=json' 2>/dev/null)" || return 1
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$json" | jq -r ".[].files[] | select(.filename==\"${TARBALL}\") | .sha256" | head -n1
  else
    printf '%s' "$json" \
      | grep -A6 "\"filename\": \"${TARBALL}\"" \
      | grep -m1 '"sha256"' \
      | sed -E 's/.*"sha256": *"([0-9a-f]+)".*/\1/'
  fi
}

log "Verificando checksum SHA-256..."
EXPECTED="$(sha256_oficial || true)"
if [ -n "$EXPECTED" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "${TMP}/${TARBALL}" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL="$(shasum -a 256 "${TMP}/${TARBALL}" | awk '{print $1}')"
  else
    ACTUAL=""
    warn "sha256sum/shasum não encontrado — pulando verificação."
  fi
  if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
    die "$(printf 'checksum inválido!\n  esperado: %s\n  obtido:   %s' "$EXPECTED" "$ACTUAL")"
  fi
  [ -n "$ACTUAL" ] && log "Checksum confere."
else
  warn "não consegui obter o checksum oficial — prosseguindo sem verificar."
fi

# --- precisa de sudo? -------------------------------------------------------
SUDO=""
if [ ! -w "$INSTALL_DIR" ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
    warn "sem permissão de escrita em ${INSTALL_DIR}; usando sudo."
  else
    die "sem permissão de escrita em ${INSTALL_DIR} e 'sudo' indisponível. Defina GO_INSTALL_DIR para um diretório seu."
  fi
fi

# --- instala ----------------------------------------------------------------
log "Removendo instalação antiga em ${GO_ROOT} (se existir)..."
$SUDO rm -rf "$GO_ROOT"

log "Extraindo para ${INSTALL_DIR}..."
$SUDO mkdir -p "$INSTALL_DIR"
$SUDO tar -C "$INSTALL_DIR" -xzf "${TMP}/${TARBALL}"

# --- verificação final ------------------------------------------------------
INSTALLED="$("${GO_ROOT}/bin/go" version || true)"
[ -n "$INSTALLED" ] || die "instalação falhou: ${GO_ROOT}/bin/go não executou"
log "Instalado: ${INSTALLED}"

# --- dica de PATH -----------------------------------------------------------
if ! command -v go >/dev/null 2>&1 || [ "$(command -v go)" != "${GO_ROOT}/bin/go" ]; then
  cat <<EOF

Adicione o Go ao seu PATH (ex.: ~/.bashrc ou ~/.profile):

    export PATH="${GO_ROOT}/bin:\$PATH"

Depois recarregue o shell:  source ~/.bashrc
EOF
fi