#!/usr/bin/env bash
#
# run-docker.sh — lance un jeu dans le conteneur wsquashfs-launcher (service
# de docker-compose.yml : FUSE, GPU, X11, son, volumes de sauvegardes).
#
# Usage : run-docker.sh [--build] [options du lanceur] <jeu.wsquashfs | dossier du jeu> [arguments]
#
#   run-docker.sh ~/roms/windows/MonJeu.wsquashfs
#   run-docker.sh --build ~/roms/windows/MonJeu.wsquashfs
#   run-docker.sh --exec ~/roms/windows/MonJeu.wsquashfs winecfg
#   run-docker.sh --doctor
#
# Le dossier du jeu est monté sur /games : en lecture seule pour une image
# .wsquashfs (le lanceur n'y écrit jamais), en écriture pour un jeu installé
# (--install), qui écrit dans son propre dossier.

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

COMPOSE_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SERVICE=wsquashfs-launcher

usage() {
    sed -n '3,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# docker compose (v2), sinon docker-compose (v1).
compose() {
    if docker compose version &>/dev/null; then
        docker compose "$@"
    elif command -v docker-compose &>/dev/null; then
        docker-compose "$@"
    else
        echo -e "${RED}Erreur : docker compose ou docker-compose requis${NC}" >&2
        return 1
    fi
}

main() {
    local build=false args=() game="" rc=0
    while [[ $# -gt 0 ]]; do
        case "$1" in
            -h|--help)  usage; return 0 ;;
            -b|--build) build=true ;;
            *)
                # Premier argument qui désigne un jeu existant : monté sur
                # /games, remplacé par son chemin dans le conteneur.
                if [[ -z "$game" && ( -f "$1" || -d "$1" ) && "$1" != -* ]]; then
                    game=$(realpath "$1")
                    # Montage /games de docker-compose.yml
                    export WSQUASHFS_GAMES WSQUASHFS_GAMES_MODE=ro
                    WSQUASHFS_GAMES=$(dirname "$game")
                    [[ -d "$game" ]] && WSQUASHFS_GAMES_MODE=rw
                    args+=("/games/$(basename "$game")")
                else
                    args+=("$1")
                fi
                ;;
        esac
        shift
    done
    ((${#args[@]})) || { usage; return 1; }

    cd "$COMPOSE_DIR" || return 1
    # UID/GID de l'hôte pour la construction de l'image et les chemins du son
    export GID
    GID=$(id -g)
    export UID

    if [[ "$build" == true ]] || ! docker image inspect "${SERVICE}:latest" &>/dev/null; then
        echo -e "${YELLOW}Construction de l'image...${NC}"
        compose build || return 1
    fi

    # Affichage X11 ouvert à l'utilisateur courant seulement, le temps du jeu
    xhost "+SI:localuser:$(id -un)" &>/dev/null \
        || echo -e "${YELLOW}Attention : xhost indisponible, l'affichage X11 peut être refusé${NC}"

    [[ -n "$game" ]] && echo -e "${GREEN}Jeu :${NC} ${game}"
    compose run --rm "$SERVICE" "${args[@]}" || rc=$?

    xhost "-SI:localuser:$(id -un)" &>/dev/null
    if ((rc == 0)); then
        echo -e "${GREEN}Terminé${NC}"
    else
        echo -e "${RED}Terminé avec des erreurs (code ${rc})${NC}" >&2
    fi
    return "$rc"
}

main "$@"
exit
