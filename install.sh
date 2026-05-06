#!/bin/bash

# Script d'installation pour WSquashFS Launcher

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
    echo -e "${BLUE}║    WSquashFS Launcher - Installation   ║${NC}"
    echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
    echo ""
}

print_success() { echo -e "${GREEN}✓${NC} $1"; }
print_error()   { echo -e "${RED}✗${NC} $1"; }
print_info()    { echo -e "${YELLOW}→${NC} $1"; }

check_root() {
    if [[ $EUID -eq 0 ]]; then
        INSTALL_DIR="/usr/local/bin"
        USE_SUDO=""
    else
        INSTALL_DIR="$HOME/.local/bin"
        USE_SUDO=""
    fi
}

check_dependencies() {
    echo ""
    print_info "Vérification des dépendances..."
    echo ""

    local has_error=false

    # --- wine (obligatoire) ---
    if command -v wine &>/dev/null; then
        print_success "wine détecté"
    else
        print_error "wine manquant"
        echo "    Debian/Ubuntu : sudo apt install wine"
        echo "    Arch Linux    : sudo pacman -S wine"
        echo "    Fedora        : sudo dnf install wine"
        has_error=true
    fi

    # --- montage squashfs (squashfuse OU unsquashfs obligatoire) ---
    local has_squashfuse=false
    local has_unsquashfs=false
    command -v squashfuse   &>/dev/null && has_squashfuse=true
    command -v unsquashfs   &>/dev/null && has_unsquashfs=true

    if [[ "$has_squashfuse" == true ]] || [[ "$has_unsquashfs" == true ]]; then
        [[ "$has_squashfuse" == true ]] && print_success "squashfuse détecté"
        [[ "$has_unsquashfs" == true ]] && print_success "unsquashfs détecté"
    else
        print_error "squashfuse et unsquashfs manquants (au moins un requis)"
        echo ""
        if command -v apt-get &>/dev/null; then
            read -p "  Installer squashfuse et squashfs-tools ? [o/N] " -n 1 -r
            echo ""
            if [[ $REPLY =~ ^[OoYy]$ ]]; then
                ${USE_SUDO:+$USE_SUDO} apt-get install -y squashfuse squashfs-tools
                print_success "squashfuse et squashfs-tools installés"
                has_squashfuse=true
                has_unsquashfs=true
            fi
        else
            echo "    Debian/Ubuntu : sudo apt install squashfuse squashfs-tools"
            echo "    Arch Linux    : sudo pacman -S squashfuse squashfs-tools"
        fi
        [[ "$has_squashfuse" == false ]] && [[ "$has_unsquashfs" == false ]] && has_error=true
    fi

    # --- fuse-overlayfs (optionnel, mode overlay) ---
    if command -v fuse-overlayfs &>/dev/null; then
        print_success "fuse-overlayfs détecté  (mode overlay disponible)"
    else
        print_info  "fuse-overlayfs absent   (mode copy utilisé — fonctionnel)"
        echo "    Pour activer le mode overlay :"
        echo "    Debian/Ubuntu : sudo apt install fuse-overlayfs"
    fi

    echo ""
    if [[ "$has_error" == true ]]; then
        return 1
    fi
    print_success "Dépendances vérifiées"
    return 0
}

install_script() {
    echo ""
    print_info "Installation de wsquashfs-launcher..."

    if [[ ! -f "wsquashfs-launcher" ]]; then
        print_error "Fichier wsquashfs-launcher introuvable"
        echo "Exécutez ce script depuis le répertoire wsquashfs-launcher"
        return 1
    fi

    mkdir -p "$INSTALL_DIR"
    cp wsquashfs-launcher "$INSTALL_DIR/wsquashfs-launcher"
    chmod +x "$INSTALL_DIR/wsquashfs-launcher"

    print_success "Script installé : $INSTALL_DIR/wsquashfs-launcher"
}

create_mime_type() {
    echo ""
    read -p "Associer les fichiers .wsquashfs à wsquashfs-launcher ? [o/N] " -n 1 -r
    echo ""
    [[ ! $REPLY =~ ^[OoYy]$ ]] && return 0

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

    command -v update-mime-database &>/dev/null && \
        update-mime-database "$HOME/.local/share/mime" 2>/dev/null || true

    print_success "Type MIME créé"
}

check_path() {
    echo ""
    if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
        print_info "$INSTALL_DIR n'est pas dans votre PATH"

        local shell_rc=""
        [[ -n "$BASH_VERSION" ]] && shell_rc="$HOME/.bashrc"
        [[ -n "$ZSH_VERSION"  ]] && shell_rc="$HOME/.zshrc"

        if [[ -n "$shell_rc" ]]; then
            read -p "Ajouter automatiquement à $shell_rc ? [o/N] " -n 1 -r
            echo ""
            if [[ $REPLY =~ ^[OoYy]$ ]]; then
                echo "" >> "$shell_rc"
                echo "# WSquashFS Launcher" >> "$shell_rc"
                echo "export PATH=\"\$PATH:$INSTALL_DIR\"" >> "$shell_rc"
                print_success "PATH mis à jour dans $shell_rc"
                print_info "Rechargez votre shell : source $shell_rc"
            else
                echo "  Ajoutez manuellement à votre shell :"
                echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
            fi
        fi
    else
        print_success "$INSTALL_DIR est dans votre PATH"
    fi
}

show_usage() {
    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Installation terminée !          ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
    echo ""
    echo "  wsquashfs-launcher /path/to/game.wsquashfs"
    echo "  wsquashfs-launcher --clean"
    echo "  wsquashfs-launcher --help"
    echo ""
    echo "Variables d'environnement :"
    echo "  WSQUASHFS_SAVES_DIR   Répertoire des sauvegardes overlay"
    echo "  WSQUASHFS_CACHE       Répertoire du cache de travail"
    echo ""
}

uninstall() {
    print_header
    print_info "Désinstallation de WSquashFS Launcher..."
    echo ""

    if [[ -f "$INSTALL_DIR/wsquashfs-launcher" ]]; then
        rm "$INSTALL_DIR/wsquashfs-launcher"
        print_success "Script supprimé"
    else
        print_info "Script non trouvé dans $INSTALL_DIR"
    fi

    if [[ -f "$HOME/.local/share/mime/packages/wsquashfs.xml" ]]; then
        rm "$HOME/.local/share/mime/packages/wsquashfs.xml"
        command -v update-mime-database &>/dev/null && \
            update-mime-database "$HOME/.local/share/mime" 2>/dev/null || true
        print_success "Type MIME supprimé"
    fi

    echo ""
    print_info "Les sauvegardes et caches sont conservés :"
    echo "  ~/.local/share/wsquashfs/saves/"
    echo "  ~/.cache/wsquashfs/"
    echo ""
    echo "Pour tout supprimer :"
    echo "  rm -rf ~/.local/share/wsquashfs/ ~/.cache/wsquashfs/"
    echo ""
}

main() {
    print_header
    check_root
    print_info "Répertoire d'installation : $INSTALL_DIR"

    check_dependencies || exit 1
    install_script     || exit 1
    create_mime_type
    check_path
    show_usage
}

case "${1:-}" in
    --uninstall|-u) check_root; uninstall ;;
    --help|-h)
        echo "Usage: $0 [--uninstall|-u] [--help|-h]"
        echo "Sans option : installation standard"
        ;;
    *) main ;;
esac
