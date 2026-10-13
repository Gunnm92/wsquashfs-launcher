#!/usr/bin/env bash
#
# test-setup.sh — vérifie l'installation : le lanceur (wsquashfs-launcher
# --doctor : FUSE, Wine/Proton, umu, DXVK, VKD3D, d7vk, manettes, dossiers)
# puis, si Docker est présent, l'image et docker compose.
#
# Code de retour : celui de --doctor (1 si aucun jeu ne peut être lancé).

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

REPO_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# Lanceur du dépôt, sinon celui installé dans le PATH.
find_launcher() {
    if [[ -x "${REPO_DIR}/wsquashfs-launcher" ]]; then
        echo "${REPO_DIR}/wsquashfs-launcher"
    else
        command -v wsquashfs-launcher
    fi
}

check_docker() {
    echo ""
    echo "Docker (facultatif)"
    if ! command -v docker &>/dev/null; then
        echo -e "  – docker absent : seule l'installation directe est utilisable"
        return 0
    fi
    if ! docker info &>/dev/null; then
        echo -e "  ${RED}✗${NC} docker ne répond pas (service arrêté ou droits insuffisants)"
        return 0
    fi
    echo -e "  ${GREEN}✓${NC} docker : $(docker --version)"
    if docker compose version &>/dev/null || command -v docker-compose &>/dev/null; then
        echo -e "  ${GREEN}✓${NC} docker compose"
    else
        echo -e "  ${YELLOW}–${NC} docker compose absent : run-docker.sh en a besoin"
    fi
    if docker image inspect wsquashfs-launcher:latest &>/dev/null; then
        echo -e "  ${GREEN}✓${NC} image wsquashfs-launcher:latest"
    else
        echo -e "  ${YELLOW}–${NC} image absente : make build-docker (ou docker compose build)"
    fi
}

main() {
    local launcher rc
    launcher=$(find_launcher)
    if [[ -z "$launcher" ]]; then
        echo -e "${RED}✗ wsquashfs-launcher introuvable (ni dans ${REPO_DIR}, ni dans le PATH)${NC}"
        echo "  Installation : ./install.sh"
        return 1
    fi
    echo "Lanceur : ${launcher}"
    echo ""
    "$launcher" --doctor
    rc=$?
    check_docker
    return "$rc"
}

main "$@"
exit
