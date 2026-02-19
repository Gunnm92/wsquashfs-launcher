#!/bin/bash

# Script helper pour lancer facilement un fichier wsquashfs avec Docker

set -e

# Couleurs pour l'affichage
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Fonction d'aide
show_help() {
    cat << EOF
Usage: $0 <fichier.wsquashfs>

Script helper pour lancer des fichiers WSquashFS avec Docker.

Options:
    -h, --help          Afficher cette aide
    -b, --build         Reconstruire l'image Docker avant de lancer
    -v, --verbose       Mode verbose (afficher les logs Docker)

Exemples:
    $0 /path/to/game.wsquashfs
    $0 --build /path/to/game.wsquashfs
    $0 -v /games/mon-jeu.wsquashfs

Configuration:
    - Assurez-vous que X11 est accessible depuis Docker
    - Le fichier wsquashfs doit être lisible
    - Docker doit avoir les privilèges nécessaires pour FUSE

EOF
}

# Variables
BUILD=false
VERBOSE=false
WSQUASHFS_FILE=""

# Parsing des arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            show_help
            exit 0
            ;;
        -b|--build)
            BUILD=true
            shift
            ;;
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        *)
            WSQUASHFS_FILE="$1"
            shift
            ;;
    esac
done

# Vérification de l'argument
if [[ -z "$WSQUASHFS_FILE" ]]; then
    echo -e "${RED}Erreur : Vous devez spécifier un fichier .wsquashfs${NC}"
    echo ""
    show_help
    exit 1
fi

# Vérification de l'existence du fichier
if [[ ! -f "$WSQUASHFS_FILE" ]]; then
    echo -e "${RED}Erreur : Le fichier '$WSQUASHFS_FILE' n'existe pas${NC}"
    exit 1
fi

# Vérification de l'extension
if [[ "${WSQUASHFS_FILE##*.}" != "wsquashfs" ]]; then
    echo -e "${YELLOW}Attention : Le fichier ne semble pas avoir l'extension .wsquashfs${NC}"
fi

# Chemin absolu du fichier
WSQUASHFS_FILE=$(realpath "$WSQUASHFS_FILE")
WSQUASHFS_DIR=$(dirname "$WSQUASHFS_FILE")
WSQUASHFS_BASENAME=$(basename "$WSQUASHFS_FILE")

echo -e "${GREEN}WSquashFS Launcher - Docker${NC}"
echo "=================================="
echo "Fichier : $WSQUASHFS_BASENAME"
echo "Chemin  : $WSQUASHFS_DIR"
echo "=================================="
echo ""

# Construction de l'image si demandé
if [[ "$BUILD" == true ]]; then
    echo -e "${YELLOW}Construction de l'image Docker...${NC}"
    docker-compose build
    echo ""
fi

# Vérification que l'image existe
if ! docker images | grep -q wsquashfs-launcher; then
    echo -e "${YELLOW}L'image Docker n'existe pas. Construction...${NC}"
    docker-compose build
    echo ""
fi

# Autoriser les connexions X11 locales
echo -e "${GREEN}Configuration de X11...${NC}"
xhost +local:docker > /dev/null 2>&1 || echo -e "${YELLOW}Attention : Impossible de configurer xhost${NC}"

# Options Docker
DOCKER_OPTS=""
if [[ "$VERBOSE" == true ]]; then
    DOCKER_OPTS="--verbose"
fi

# Lancement du conteneur
echo -e "${GREEN}Lancement du jeu...${NC}"
echo ""

docker run --rm \
    --privileged \
    --cap-add=SYS_ADMIN \
    --device /dev/fuse:/dev/fuse \
    -v "$WSQUASHFS_DIR:/games:ro" \
    -v /tmp/.X11-unix:/tmp/.X11-unix:rw \
    -e DISPLAY=$DISPLAY \
    -e WINEDEBUG=-all \
    --network host \
    --ipc host \
    wsquashfs-launcher:latest \
    "/games/$WSQUASHFS_BASENAME"

EXIT_CODE=$?

echo ""
echo "=================================="
if [[ $EXIT_CODE -eq 0 ]]; then
    echo -e "${GREEN}Terminé avec succès${NC}"
else
    echo -e "${RED}Terminé avec des erreurs (code: $EXIT_CODE)${NC}"
fi
echo "=================================="

# Nettoyage X11
xhost -local:docker > /dev/null 2>&1 || true

exit $EXIT_CODE
