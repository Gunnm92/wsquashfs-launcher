#!/bin/bash

# Script de test pour vérifier l'installation et la configuration

set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "========================================="
echo "Test de configuration WSquashFS Launcher"
echo "========================================="
echo ""

# Fonction pour vérifier une commande
check_command() {
    local cmd=$1
    local name=$2
    local required=$3

    if command -v "$cmd" &> /dev/null; then
        echo -e "${GREEN}✅ $name trouvé${NC}"
        $cmd --version 2>&1 | head -1 || true
        return 0
    else
        if [[ "$required" == "required" ]]; then
            echo -e "${RED}❌ $name NON TROUVÉ (REQUIS)${NC}"
        else
            echo -e "${YELLOW}⚠️  $name non trouvé (optionnel)${NC}"
        fi
        return 1
    fi
}

echo "=== Vérification des dépendances de base ==="
echo ""

ERRORS=0

check_command "squashfuse" "SquashFuse" "required" || ((ERRORS++))
check_command "wine" "Wine" "required" || ((ERRORS++))
check_command "dos2unix" "dos2unix" "required" || ((ERRORS++))

echo ""
echo "=== Vérification des dépendances optionnelles ==="
echo ""

check_command "docker" "Docker" "optional"
check_command "docker-compose" "Docker Compose" "optional"
check_command "flatpak" "Flatpak" "optional"

echo ""
echo "=== Vérification de Bottles (si Flatpak présent) ==="
echo ""

if command -v flatpak &> /dev/null; then
    if flatpak list | grep -q com.usebottles.bottles; then
        echo -e "${GREEN}✅ Bottles trouvé${NC}"
    else
        echo -e "${YELLOW}⚠️  Bottles non installé (optionnel)${NC}"
    fi
else
    echo -e "${YELLOW}⚠️  Flatpak non disponible, impossible de vérifier Bottles${NC}"
fi

echo ""
echo "=== Vérification de Wine ==="
echo ""

if command -v wine &> /dev/null; then
    echo "Version Wine :"
    wine --version
    echo ""
    echo "Architecture supportée :"
    WINEARCH=win64 wine --version &> /dev/null && echo "  - win64 : ✅"
    WINEARCH=win32 wine --version &> /dev/null && echo "  - win32 : ✅"
fi

echo ""
echo "=== Vérification des scripts ==="
echo ""

if [[ -x "./wsquashfs-launcher" ]]; then
    echo -e "${GREEN}✅ wsquashfs-launcher exécutable${NC}"
else
    echo -e "${RED}❌ wsquashfs-launcher non exécutable${NC}"
    ((ERRORS++))
fi

if [[ -x "./wsquashfs-launcher-docker" ]]; then
    echo -e "${GREEN}✅ wsquashfs-launcher-docker exécutable${NC}"
else
    echo -e "${YELLOW}⚠️  wsquashfs-launcher-docker non exécutable${NC}"
fi

if [[ -x "./run-docker.sh" ]]; then
    echo -e "${GREEN}✅ run-docker.sh exécutable${NC}"
else
    echo -e "${YELLOW}⚠️  run-docker.sh non exécutable${NC}"
fi

echo ""
echo "=== Vérification de Docker (si disponible) ==="
echo ""

if command -v docker &> /dev/null; then
    echo "Version Docker :"
    docker --version

    echo ""
    echo "Docker est-il en cours d'exécution ?"
    if docker info &> /dev/null; then
        echo -e "${GREEN}✅ Docker daemon actif${NC}"
    else
        echo -e "${RED}❌ Docker daemon non actif${NC}"
        echo "Lancez Docker avec : sudo systemctl start docker"
    fi

    echo ""
    echo "Images Docker :"
    docker images | grep wsquashfs-launcher || echo -e "${YELLOW}⚠️  Aucune image wsquashfs-launcher trouvée${NC}"
    echo "Pour construire l'image : make build-docker ou docker-compose build"
fi

echo ""
echo "=== Vérification de FUSE ==="
echo ""

if [[ -e /dev/fuse ]]; then
    echo -e "${GREEN}✅ /dev/fuse présent${NC}"
    ls -l /dev/fuse
else
    echo -e "${RED}❌ /dev/fuse non trouvé${NC}"
    echo "FUSE est nécessaire pour monter les fichiers squashfs"
    ((ERRORS++))
fi

echo ""
echo "=== Vérification des permissions ==="
echo ""

if groups | grep -qw fuse; then
    echo -e "${GREEN}✅ Utilisateur dans le groupe 'fuse'${NC}"
else
    echo -e "${YELLOW}⚠️  Utilisateur pas dans le groupe 'fuse'${NC}"
    echo "Pour ajouter : sudo usermod -aG fuse \$USER"
fi

echo ""
echo "========================================="
echo "Résumé"
echo "========================================="

if [[ $ERRORS -eq 0 ]]; then
    echo -e "${GREEN}✅ Tous les composants requis sont présents !${NC}"
    echo ""
    echo "Vous pouvez maintenant utiliser :"
    echo "  - Version desktop : ./wsquashfs-launcher <fichier.wsquashfs>"
    echo "  - Version Docker  : ./run-docker.sh <fichier.wsquashfs>"
    echo ""
    echo "Pour installer la version desktop :"
    echo "  make install-desktop"
    echo ""
    echo "Pour construire l'image Docker :"
    echo "  make build-docker"
else
    echo -e "${RED}❌ Il manque $ERRORS composant(s) requis${NC}"
    echo ""
    echo "Installation des dépendances :"
    echo ""
    echo "Debian/Ubuntu :"
    echo "  sudo apt install squashfuse wine dos2unix"
    echo ""
    echo "Arch Linux :"
    echo "  sudo pacman -S squashfuse wine dos2unix"
    echo ""
    echo "Fedora :"
    echo "  sudo dnf install squashfuse wine dos2unix"
fi

echo ""
echo "========================================="

exit $ERRORS
