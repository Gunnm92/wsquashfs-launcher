#!/bin/bash

# Script d'installation pour WSquashFS Launcher
# Installe wsquashfs-run comme une application système

set -e

# Couleurs
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Fonction d'affichage
print_header() {
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║  WSquashFS Launcher - Installation    ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    echo ""
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

print_info() {
    echo -e "${YELLOW}→${NC} $1"
}

# Vérifier si on est root
check_root() {
    INSTALL_DIR="/usr/bin"
    if [[ $EUID -eq 0 ]]; then
        USE_SUDO=""
    else
        USE_SUDO="sudo"
    fi
}

# Détecter si on est dans un conteneur Docker/LXC
is_docker() {
    [[ -f "/.dockerenv" ]] || grep -qE 'docker|lxc|containerd' /proc/1/cgroup 2>/dev/null
}

# Vérifier les dépendances
check_dependencies() {
    echo ""
    print_info "Vérification des dépendances..."

    local missing_wine=false
    local has_error=false

    if ! command -v wine &> /dev/null; then
        missing_wine=true
    fi

    # -------------------------------------------------------
    # squashfuse + fuse-overlayfs (machine physique et Docker)
    # montage sans extraction, overlay userspace sans root
    # -------------------------------------------------------
    local missing_squashfuse=false
    local missing_fuse_overlayfs=false

    command -v squashfuse &> /dev/null || missing_squashfuse=true
    command -v fuse-overlayfs &> /dev/null || missing_fuse_overlayfs=true

    if [[ "$missing_squashfuse" == "true" ]] || [[ "$missing_fuse_overlayfs" == "true" ]]; then
        [[ "$missing_squashfuse" == "true" ]]     && print_error "squashfuse manquant"
        [[ "$missing_fuse_overlayfs" == "true" ]] && print_error "fuse-overlayfs manquant"
        echo ""

        if command -v apt-get &>/dev/null; then
            local pkgs=""
            [[ "$missing_squashfuse" == "true" ]]     && pkgs="$pkgs squashfuse"
            [[ "$missing_fuse_overlayfs" == "true" ]] && pkgs="$pkgs fuse-overlayfs"
            read -p "  Installer${pkgs} automatiquement ? [o/N] " -n 1 -r
            echo ""
            if [[ $REPLY =~ ^[OoYy]$ ]]; then
                $USE_SUDO apt-get update -qq
                $USE_SUDO apt-get install -y $pkgs
                print_success "Paquets installés :$pkgs"
                missing_squashfuse=false
                missing_fuse_overlayfs=false
            fi
        else
            echo "  Installation manuelle :"
            echo "    Debian/Ubuntu : sudo apt install squashfuse fuse-overlayfs"
            echo "    Arch Linux    : sudo pacman -S squashfuse fuse-overlayfs"
            echo "    Fedora        : sudo dnf install squashfuse fuse-overlayfs"
            echo ""
        fi
    fi

    if [[ "$missing_squashfuse" == "true" ]] || [[ "$missing_fuse_overlayfs" == "true" ]]; then
        print_error "squashfuse et fuse-overlayfs sont requis"
        has_error=true
    else
        print_success "Mode actif : squashfuse + fuse-overlayfs (sans root)"
    fi

    # -------------------------------------------------------
    # Wine
    # -------------------------------------------------------
    if [[ "$missing_wine" == "true" ]]; then
        echo ""
        print_error "wine est manquant"
        echo ""
        echo "  Installation manuelle :"
        echo "    Debian/Ubuntu : sudo apt install wine"
        echo "    Arch Linux    : sudo pacman -S wine"
        echo "    Fedora        : sudo dnf install wine"
        has_error=true
    else
        print_success "wine détecté"
    fi

    echo ""
    if [[ "$has_error" == "true" ]]; then
        return 1
    fi

    print_success "Toutes les dépendances sont installées"
    return 0
}

# Installation
install_script() {
    echo ""
    print_info "Installation de wsquashfs-run..."

    # Vérifier que le script existe
    if [[ ! -f "wsquashfs-run" ]]; then
        print_error "Fichier wsquashfs-run introuvable"
        echo "Veuillez exécuter ce script depuis le répertoire wsquashfs-launcher"
        return 1
    fi

    # Créer le répertoire d'installation si nécessaire
    if [[ ! -d "$INSTALL_DIR" ]]; then
        mkdir -p "$INSTALL_DIR"
        print_success "Répertoire créé : $INSTALL_DIR"
    fi

    # Copier le script
    if [[ -n "$USE_SUDO" ]]; then
        $USE_SUDO cp wsquashfs-run "$INSTALL_DIR/wsquashfs-run"
        $USE_SUDO chmod +x "$INSTALL_DIR/wsquashfs-run"
    else
        cp wsquashfs-run "$INSTALL_DIR/wsquashfs-run"
        chmod +x "$INSTALL_DIR/wsquashfs-run"
    fi

    print_success "Script installé dans : $INSTALL_DIR/wsquashfs-run"
}

# Créer l'association de fichier pour .wsquashfs
create_mime_type() {
    echo ""
    read -p "Voulez-vous associer les fichiers .wsquashfs ? [o/N] " -n 1 -r
    echo ""

    if [[ ! $REPLY =~ ^[OoYy]$ ]]; then
        return 0
    fi

    local mime_dir="$HOME/.local/share/mime/packages"
    mkdir -p "$mime_dir"

    cat > "$mime_dir/wsquashfs.xml" << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<mime-info xmlns="http://www.freedesktop.org/standards/shared-mime-info">
    <mime-type type="application/x-wsquashfs">
        <comment>WSquashFS Archive (Batocera)</comment>
        <glob pattern="*.wsquashfs"/>
        <magic priority="50">
            <match type="string" offset="0" value="hsqs"/>
        </magic>
    </mime-type>
</mime-info>
EOF

    # Mettre à jour la base de données MIME
    if command -v update-mime-database &> /dev/null; then
        update-mime-database "$HOME/.local/share/mime" 2>/dev/null || true
    fi

    print_success "Type MIME créé : $mime_dir/wsquashfs.xml"
}

# Vérifier que PATH contient le répertoire d'installation
check_path() {
    echo ""
    if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
        print_info "$INSTALL_DIR n'est pas dans votre PATH"

        local shell_rc=""
        if [[ -n "$BASH_VERSION" ]]; then
            shell_rc="$HOME/.bashrc"
        elif [[ -n "$ZSH_VERSION" ]]; then
            shell_rc="$HOME/.zshrc"
        fi

        if [[ -n "$shell_rc" ]]; then
            echo ""
            echo "Ajoutez cette ligne à votre $shell_rc :"
            echo ""
            echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
            echo ""
            read -p "Voulez-vous que je l'ajoute automatiquement ? [o/N] " -n 1 -r
            echo ""

            if [[ $REPLY =~ ^[OoYy]$ ]]; then
                echo "" >> "$shell_rc"
                echo "# WSquashFS Launcher" >> "$shell_rc"
                echo "export PATH=\"\$PATH:$INSTALL_DIR\"" >> "$shell_rc"
                print_success "PATH mis à jour dans $shell_rc"
                print_info "Rechargez votre shell ou tapez : source $shell_rc"
            fi
        fi
    else
        print_success "$INSTALL_DIR est dans votre PATH"
    fi
}

# Afficher les informations d'utilisation
show_usage() {
    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║     Installation terminée ! 🎉        ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
    echo ""
    echo "Utilisation :"
    echo ""
    echo "  wsquashfs-run /path/to/game.wsquashfs"
    echo ""
    echo "Exemples :"
    echo "  wsquashfs-run \"Bug Busters.wsquashfs\""
    echo "  wsquashfs-run ~/Games/MyGame.wsquashfs"
    echo ""
    echo "Documentation :"
    echo "  README.md       - Documentation complète"
    echo "  QUICKSTART.md   - Guide de démarrage rapide"
    echo "  pegasus-example - Configuration Pegasus Frontend"
    echo ""
    echo "Variables d'environnement :"
    echo "  WSQUASHFS_SAVES_DIR   - Répertoire des sauvegardes"
    echo "  WSQUASHFS_WINEPREFIX  - Répertoire des préfixes Wine"
    echo ""
}

# Désinstallation
uninstall() {
    print_header
    print_info "Désinstallation de WSquashFS Launcher..."
    echo ""

    if [[ -f "$INSTALL_DIR/wsquashfs-run" ]]; then
        if [[ -n "$USE_SUDO" ]]; then
            $USE_SUDO rm "$INSTALL_DIR/wsquashfs-run"
        else
            rm "$INSTALL_DIR/wsquashfs-run"
        fi
        print_success "Script désinstallé"
    else
        print_info "Script non trouvé dans $INSTALL_DIR"
    fi

    # Supprimer le type MIME
    if [[ -f "$HOME/.local/share/mime/packages/wsquashfs.xml" ]]; then
        rm "$HOME/.local/share/mime/packages/wsquashfs.xml"
        if command -v update-mime-database &> /dev/null; then
            update-mime-database "$HOME/.local/share/mime" 2>/dev/null || true
        fi
        print_success "Type MIME supprimé"
    fi

    echo ""
    print_info "Les sauvegardes et préfixes Wine sont conservés dans :"
    echo "  ~/.local/share/wsquashfs/"
    echo ""
    echo "Pour les supprimer aussi :"
    echo "  rm -rf ~/.local/share/wsquashfs/"
    echo "  rm -rf ~/.cache/wsquashfs/"
    echo ""
}

# Programme principal
main() {
    print_header

    # Vérifier le mode
    check_root

    print_info "Mode d'installation : ${INSTALL_DIR}"

    # Vérifier les dépendances
    if ! check_dependencies; then
        exit 1
    fi

    # Installer
    if ! install_script; then
        exit 1
    fi

    # Créer l'association MIME pour .wsquashfs
    create_mime_type

    # Vérifier PATH
    check_path

    # Afficher les infos d'utilisation
    show_usage
}

# Gestion des arguments
case "${1:-}" in
    --uninstall|-u)
        check_root
        uninstall
        ;;
    --help|-h)
        echo "Usage: $0 [OPTIONS]"
        echo ""
        echo "Options:"
        echo "  --help, -h        Afficher cette aide"
        echo "  --uninstall, -u   Désinstaller WSquashFS Launcher"
        echo ""
        echo "Sans option : Installation standard"
        ;;
    *)
        main
        ;;
esac
