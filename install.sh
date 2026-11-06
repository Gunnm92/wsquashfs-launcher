#!/usr/bin/env bash
#
# install.sh — amorce l'installation de wsquashfs-launcher.
#
# Le binaire Go fait tout le reste (paquets système, runners, DXVK, lanceur,
# agent) : ce script ne fait que le télécharger s'il est absent, puis lancer
# son installeur, « wsquashfs-launcher --setup ».
#
# Usage local  : bash install.sh [options]
# Usage distant: curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash
#
# Options (transmises à --setup) :
#   --yes, -y        répond oui à toutes les questions
#   --dry-run, -n    montre ce qui serait fait, sans rien installer
#   --agent          installe l'agent pour wsquashfs-manager
#   --uninstall, -u  désinstalle le lanceur (sauvegardes et runners gardés)
#   --help, -h       aide de --setup

REPO_RAW="https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main"

if [[ "$(id -u)" -eq 0 ]]; then
    BIN_DIR="/usr/local/bin"
else
    BIN_DIR="${HOME}/.local/bin"
fi
BIN="${BIN_DIR}/wsquashfs-launcher"

dry_run=false
for arg in "$@"; do
    case "$arg" in
        --dry-run|-n) dry_run=true ;;
    esac
done

# Copie locale (clone du dépôt) ou téléchargement depuis GitHub.
local_bin=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    local_bin="$(dirname "${BASH_SOURCE[0]}")/wsquashfs-launcher"
fi

if [[ -n "$local_bin" && -f "$local_bin" ]]; then
    if [[ "$dry_run" == true ]]; then
        echo "[simulation] copie de ${local_bin} vers ${BIN}"
    else
        mkdir -p "$BIN_DIR" && cp "$local_bin" "${BIN}.new" && chmod 755 "${BIN}.new" \
            && mv -f "${BIN}.new" "$BIN" || { echo "Erreur : copie de ${local_bin} impossible" >&2; exit 1; }
    fi
elif [[ ! -x "$BIN" ]]; then
    if [[ "$dry_run" == true ]]; then
        echo "[simulation] téléchargement de ${REPO_RAW}/wsquashfs-launcher vers ${BIN}"
    else
        command -v curl >/dev/null 2>&1 || { echo "Erreur : curl requis pour télécharger le lanceur" >&2; exit 1; }
        mkdir -p "$BIN_DIR" || exit 1
        tmp="${BIN}.new.$$"
        curl -fsSL "${REPO_RAW}/wsquashfs-launcher" -o "$tmp" || { rm -f "$tmp"; echo "Erreur : téléchargement impossible" >&2; exit 1; }
        if [[ "$(head -c 4 "$tmp")" != $'\x7fELF' ]]; then
            rm -f "$tmp"
            echo "Erreur : fichier téléchargé invalide (binaire attendu)" >&2
            exit 1
        fi
        chmod 755 "$tmp" && mv -f "$tmp" "$BIN" || { rm -f "$tmp"; exit 1; }
    fi
fi

if [[ "$dry_run" == true && ! -x "$BIN" ]]; then
    echo "[simulation] puis : ${BIN} --setup $*"
    exit 0
fi

exec "$BIN" --setup "$@"
