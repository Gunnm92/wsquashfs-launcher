#!/usr/bin/env bash
#
# install.sh — amorce l'installation de wsquashfs-launcher.
#
# Le binaire Go fait tout le reste (paquets système, runners, DXVK, lanceur) :
# ce script ne fait que le télécharger s'il est absent, puis lancer
# son installeur, « wsquashfs-launcher --setup ».
#
# Usage local  : bash install.sh [options]
# Usage distant: curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash -s -- [options]
#
# Options (transmises à --setup, sans tirets) :
#   yes        répond oui à toutes les questions
#   dry-run    montre ce qui serait fait, sans rien installer
#   uninstall  désinstalle le lanceur (sauvegardes et runners gardés)
#   help       aide de --setup

REPO="Gunnm92/wsquashfs-launcher"
# Le CDN de raw.githubusercontent.com garde chaque fichier de main jusqu'à 5 minutes,
# séparément : juste après une publication, binaire et empreinte peuvent différer. On
# télécharge donc à l'adresse du dernier commit de main (API GitHub), qui ne change
# jamais ; API injoignable : main.
COMMIT="$(curl -fsSL -A wsquashfs-launcher-installeur "https://api.github.com/repos/${REPO}/commits/main" 2>/dev/null \
    | grep -o '"sha": *"[0-9a-f]\{40\}"' | head -n1 | grep -o '[0-9a-f]\{40\}' || true)"
REPO_RAW="https://raw.githubusercontent.com/${REPO}/${COMMIT:-main}"

if [[ "$(id -u)" -eq 0 ]]; then
    BIN_DIR="/usr/local/bin"
else
    BIN_DIR="${HOME}/.local/bin"
fi
BIN="${BIN_DIR}/wsquashfs-launcher"

dry_run=false
for arg in "$@"; do
    case "$arg" in
        dry-run) dry_run=true ;;
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
        want="$(curl -fsSL "${REPO_RAW}/wsquashfs-launcher.sha256" | cut -d' ' -f1)"
        if [[ ! "$want" =~ ^[0-9a-f]{64}$ ]] || ! command -v sha256sum >/dev/null 2>&1 \
            || [[ "$(sha256sum "$tmp" | cut -d' ' -f1)" != "$want" ]]; then
            rm -f "$tmp"
            echo "Erreur : somme de contrôle du lanceur absente ou incorrecte" >&2
            exit 1
        fi
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
