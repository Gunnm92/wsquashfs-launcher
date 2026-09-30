#!/bin/bash

# Script d'installation pour WSquashFS Launcher
# Usage local  : bash install.sh
# Usage distant: curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash

set -e

REPO_RAW="https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main"

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

WINE_INSTALL_DIR="$HOME/.local/share/wsquashfs/wine"
UMU_INSTALL_DIR="$HOME/.local/share/wsquashfs/umu"
# Emplacement standard des Proton de Steam : le lanceur, Steam et Heroic
# les y trouvent tous.
PROTON_INSTALL_DIR="$HOME/.local/share/Steam/compatibilitytools.d"

check_root() {
    if [[ $EUID -eq 0 ]]; then
        INSTALL_DIR="/usr/local/bin"
        USE_SUDO=""
    else
        INSTALL_DIR="$HOME/.local/bin"
        USE_SUDO="sudo"
    fi
}

SUDO=""

_apt_install() {
    $SUDO apt-get install -y "$@" \
        && print_success "$* installé(s)" \
        || { print_error "Échec installation : $*"; return 1; }
}

_dpkg_installed() {
    dpkg -l "$1" 2>/dev/null | grep -q '^ii'
}

check_dependencies() {
    echo ""
    print_info "Vérification des dépendances..."
    echo ""

    local has_error=false

    if ! command -v apt-get &>/dev/null; then
        print_error "apt-get requis (Debian/Ubuntu/dérivés)"
        return 1
    fi

    if [[ $EUID -ne 0 ]]; then
        if ! command -v sudo &>/dev/null; then
            print_error "sudo absent — relancez avec : sudo bash $0"
            return 1
        fi
        SUDO="sudo"
        print_info "Non-root : sudo utilisé pour les opérations système"
    fi

    # --- wine32:i386 ---
    if _dpkg_installed wine32:i386; then
        print_success "wine32:i386 déjà installé"
    else
        print_info "Installation de wine32:i386..."
        $SUDO dpkg --add-architecture i386 && $SUDO apt-get update \
            && _apt_install wine32:i386 \
            || has_error=true
    fi

    # --- libxkbcommon0:i386 ---
    # wine-tkg (Kron4ek, non WoW64) fait tourner les jeux 32 bits avec de
    # vraies bibliothèques 32 bits : sans elle, winewayland.drv 32 bits ne se
    # charge pas et un jeu 32 bits n'a aucun pilote d'affichage ("no driver
    # could be loaded", constaté sur Virtua Tennis 4).
    if _dpkg_installed libxkbcommon0:i386; then
        print_success "libxkbcommon0:i386 déjà installé"
    else
        print_info "Installation de libxkbcommon0:i386..."
        _apt_install libxkbcommon0:i386 || has_error=true
    fi

    # --- squashfuse + squashfs-tools ---
    if _dpkg_installed squashfuse && _dpkg_installed squashfs-tools; then
        print_success "squashfuse et squashfs-tools déjà installés"
    else
        print_info "Installation de squashfuse et squashfs-tools..."
        _apt_install squashfuse squashfs-tools || has_error=true
    fi

    # --- xz-utils ---
    if _dpkg_installed xz-utils; then
        print_success "xz-utils déjà installé"
    else
        print_info "Installation de xz-utils..."
        _apt_install xz-utils || has_error=true
    fi

    # --- fuse-overlayfs (optionnel, mode overlay) ---
    if _dpkg_installed fuse-overlayfs; then
        print_success "fuse-overlayfs déjà installé (mode overlay disponible)"
    else
        print_info "Installation de fuse-overlayfs..."
        _apt_install fuse-overlayfs \
            || print_info "fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)"
    fi

    # --- MangoHud (optionnel : mangohud wsquashfs-launcher jeu.wsquashfs) ---
    if _dpkg_installed mangohud; then
        print_success "mangohud déjà installé"
    else
        print_info "Installation de mangohud (facultatif)..."
        _apt_install mangohud mangohud:i386 \
            || _apt_install mangohud \
            || print_info "mangohud indisponible — overlay MangoHud non disponible"
    fi

    echo ""
    if [[ "$has_error" == true ]]; then
        return 1
    fi
    print_success "Dépendances vérifiées"
    return 0
}

# Cherche un Wine compatible avec les prefixes Batocera (.wsquashfs).
# Retourne le chemin ou chaîne vide.
find_compatible_wine() {
    local candidates=(
        "/usr/wine/wine-tkg/bin/wine"
        "/usr/wine/wine-proton/bin/wine"
        "/usr/wine/ge-custom/bin/wine"
    )
    local d w
    for d in "$WINE_INSTALL_DIR" "/opt"; do
        [[ -d "$d" ]] || continue
        while IFS= read -r -d '' w; do
            [[ -x "$w" ]] && candidates+=("$w")
        done < <(find "$d" -maxdepth 3 -name "wine" -path "*/bin/wine" -print0 2>/dev/null)
    done
    for c in "${candidates[@]}"; do
        [[ -x "$c" ]] && echo "$c" && return 0
    done
    return 1
}

# Télécharge url vers dest (fichier ou - pour stdout). Affiche les erreurs HTTP.
_download() {
    local url="$1" dest="$2"
    if command -v curl &>/dev/null; then
        if [[ "$dest" == "-" ]]; then
            curl -fsSL "$url"
        else
            curl -fL --progress-bar "$url" -o "$dest"
        fi
    elif command -v wget &>/dev/null; then
        if [[ "$dest" == "-" ]]; then
            wget -qO- "$url"
        else
            wget --show-progress -q "$url" -O "$dest"
        fi
    else
        print_error "curl ou wget requis"
        return 1
    fi
}

# Récupère la dernière version depuis l'API GitHub (fallback sur $2 si échec)
_github_latest_tag() {
    local repo="$1" fallback="$2"
    local tag
    tag=$(_download "https://api.github.com/repos/${repo}/releases/latest" - 2>/dev/null \
        | grep '"tag_name"' | head -1 \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/') || true
    echo "${tag:-$fallback}"
}

_extract_wine() {
    local archive="$1" label="$2"
    mkdir -p "$WINE_INSTALL_DIR"
    local top_dir
    top_dir=$(tar -tf "$archive" 2>/dev/null | head -1 | cut -d/ -f1)
    [[ -z "$top_dir" ]] && return 1

    # Extraire dans /tmp (fs local) pour éviter les erreurs de liens durs sur les
    # systèmes de fichiers réseau (shfs Unraid, CIFS…) qui ne les supportent pas.
    local local_tmp
    local_tmp=$(mktemp -d)
    if ! tar -xf "$archive" -C "$local_tmp" 2>/dev/null; then
        rm -rf "$local_tmp"
        return 1
    fi

    # Nom final : label fourni (ex: wine-ge-GE-Proton8-26) ou top_dir d'origine
    local final_name="${label:-$top_dir}"
    rm -rf "${WINE_INSTALL_DIR:?}/${final_name}"
    # cp -rp : préserve modes/timestamps mais pas les liens durs (compatibilité shfs)
    if ! cp -rp "${local_tmp}/${top_dir}" "${WINE_INSTALL_DIR}/${final_name}" 2>/dev/null; then
        rm -rf "$local_tmp"
        return 1
    fi
    rm -rf "$local_tmp"
    find "${WINE_INSTALL_DIR}/${final_name}" -maxdepth 2 -name "wine" -path "*/bin/wine" 2>/dev/null | head -1
}

download_wine_tkg() {
    local version
    print_info "Recherche de la dernière version wine-tkg (Kron4ek)..."
    version=$(_github_latest_tag "Kron4ek/Wine-Builds" "11.8")
    local filename="wine-${version}-staging-tkg-amd64.tar.xz"
    local url="https://github.com/Kron4ek/Wine-Builds/releases/download/${version}/${filename}"
    local tmp; tmp=$(mktemp -d)
    print_info "Téléchargement de $filename (~87 Mo)..."
    if _download "$url" "${tmp}/${filename}"; then
        local installed
        installed=$(_extract_wine "${tmp}/${filename}")
        rm -rf "$tmp"
        if [[ -x "$installed" ]]; then
            print_success "wine-tkg installé : $installed"
            return 0
        fi
        print_error "Extraction échouée (archive corrompue ?)"
    else
        print_error "Téléchargement échoué : $url"
    fi
    rm -rf "$tmp"
    return 1
}

download_wine_ge() {
    local tag
    print_info "Recherche de la dernière version wine-ge (GloriousEggroll)..."
    tag=$(_github_latest_tag "GloriousEggroll/wine-ge-custom" "GE-Proton8-26")
    local filename="wine-lutris-${tag}-x86_64.tar.xz"
    local url="https://github.com/GloriousEggroll/wine-ge-custom/releases/download/${tag}/${filename}"
    local tmp; tmp=$(mktemp -d)
    print_info "Téléchargement de $filename (~234 Mo)..."
    if _download "$url" "${tmp}/${filename}"; then
        local installed
        installed=$(_extract_wine "${tmp}/${filename}" "wine-ge-${tag}")
        rm -rf "$tmp"
        if [[ -x "$installed" ]]; then
            print_success "wine-ge installé : $installed"
            return 0
        fi
        print_error "Extraction échouée (archive corrompue ?)"
    else
        print_error "Téléchargement échoué : $url"
    fi
    rm -rf "$tmp"
    return 1
}

check_wine_batocera() {
    echo ""
    print_info "Vérification des Wine compatibles Batocera..."
    echo ""

    local found_tkg found_ge
    found_tkg=$(find "$WINE_INSTALL_DIR" /usr/wine/wine-tkg -maxdepth 3 -name "wine" -path "*/bin/wine" 2>/dev/null | head -1) || true
    found_ge=$(find "$WINE_INSTALL_DIR" /usr/wine/ge-custom /usr/wine/wine-proton -maxdepth 3 -name "wine" -path "*/bin/wine" 2>/dev/null | head -1) || true

    [[ -n "$found_tkg" ]] && print_success "wine-tkg trouvé : $found_tkg"
    [[ -n "$found_ge"  ]] && print_success "wine-ge  trouvé : $found_ge"

    local need_tkg=false need_ge=false
    [[ -z "$found_tkg" ]] && need_tkg=true
    [[ -z "$found_ge"  ]] && need_ge=true

    if [[ "$need_tkg" == false && "$need_ge" == false ]]; then
        return 0
    fi

    echo ""
    print_info "Les prefixes .wsquashfs sont conçus pour wine-tkg ou wine-ge."
    echo "    Le Wine système peut provoquer des incompatibilités de DLLs."
    echo "    Les deux versions peuvent être utiles selon le jeu."
    echo ""
    [[ "$need_tkg" == true ]] && echo "    wine-tkg  manquant  (Kron4ek staging, ~87 Mo)"
    [[ "$need_ge"  == true ]] && echo "    wine-ge   manquant  (GloriousEggroll, ~234 Mo)"
    echo ""
    echo "    Options :"
    echo "    1) Installer les deux  (~321 Mo)  — recommandé"
    [[ "$need_tkg" == true ]] && echo "    2) wine-tkg seulement (~87 Mo)"
    [[ "$need_ge"  == true ]] && echo "    3) wine-ge  seulement (~234 Mo)"
    echo "    4) Ignorer"
    echo ""
    read -p "  Choix [1/2/3/4] : " -n 1 -r
    echo ""
    case "$REPLY" in
        1)
            [[ "$need_tkg" == true ]] && { download_wine_tkg || true; }
            [[ "$need_ge"  == true ]] && { download_wine_ge  || true; }
            ;;
        2) [[ "$need_tkg" == true ]] && { download_wine_tkg || true; } ;;
        3) [[ "$need_ge"  == true ]] && { download_wine_ge  || true; } ;;
        *) print_info "Wine système sera utilisé — certaines DLLs peuvent manquer" ;;
    esac
}

# --- Proton + umu ---------------------------------------------------------
# Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes créés
# par Proton (Heroic, Steam) sont lancés via umu-run et un Proton, comme
# Heroic : le Wine de Proton lancé seul donnait écran noir ou plantage.

# Un Proton déjà installé là où le lanceur le cherche ?
# UMU-Proton recherché en particulier (30/09) : le lanceur le préfère aux
# autres Proton pour les nouveaux prefixes — Proton de Valve, sa SDL gère la
# DualSense en manette XInput avec la vibration ; GE-Proton la force en
# hidraw, sans vibration en mode XInput.
find_proton() {
    local base d
    for base in "$PROTON_INSTALL_DIR" "$HOME/.steam/root/compatibilitytools.d" \
                "$HOME/.config/heroic/tools/proton" /usr/share/steam/compatibilitytools.d; do
        for d in "$base"/UMU-Proton*; do
            [[ -x "${d}/proton" && -x "${d}/files/bin/wine" ]] && { echo "$d"; return 0; }
        done
    done 2>/dev/null
    return 1
}

download_umu() {
    local tag
    if ! command -v python3 &>/dev/null; then
        print_error "python3 requis pour umu-run"
        return 1
    fi
    print_info "Recherche de la dernière version d'umu-launcher..."
    tag=$(_github_latest_tag "Open-Wine-Components/umu-launcher" "1.4.4")
    local filename="umu-launcher-${tag}-zipapp.tar"
    local url="https://github.com/Open-Wine-Components/umu-launcher/releases/download/${tag}/${filename}"
    local tmp; tmp=$(mktemp -d)
    print_info "Téléchargement de $filename (< 1 Mo)..."
    if _download "$url" "${tmp}/${filename}" && tar -xf "${tmp}/${filename}" -C "$tmp" \
       && [[ -f "${tmp}/umu/umu-run" ]]; then
        rm -rf "$UMU_INSTALL_DIR" && mkdir -p "$UMU_INSTALL_DIR" "$INSTALL_DIR"
        cp "${tmp}/umu/umu-run" "${UMU_INSTALL_DIR}/umu-run"
        chmod +x "${UMU_INSTALL_DIR}/umu-run"
        ln -sfn "${UMU_INSTALL_DIR}/umu-run" "${INSTALL_DIR}/umu-run"
        rm -rf "$tmp"
        print_success "umu-run ${tag} installé : ${INSTALL_DIR}/umu-run"
        return 0
    fi
    print_error "Téléchargement ou extraction échoué : $url"
    rm -rf "$tmp"
    return 1
}

download_umu_proton() {
    local tag
    print_info "Recherche de la dernière version d'UMU-Proton..."
    tag=$(_github_latest_tag "Open-Wine-Components/umu-proton" "UMU-Proton-10.0-4")
    local base="https://github.com/Open-Wine-Components/umu-proton/releases/download/${tag}"
    local tmp; tmp=$(mktemp -d)
    print_info "Téléchargement de ${tag}.tar.gz (~470 Mo)..."
    if ! _download "${base}/${tag}.tar.gz" "${tmp}/${tag}.tar.gz" \
       || ! _download "${base}/${tag}.sha512sum" "${tmp}/${tag}.sha512sum"; then
        print_error "Téléchargement échoué : ${base}"
        rm -rf "$tmp"; return 1
    fi
    if ! (cd "$tmp" && sha512sum -c --quiet "${tag}.sha512sum" >/dev/null 2>&1); then
        print_error "Somme de contrôle SHA-512 incorrecte, archive ignorée"
        rm -rf "$tmp"; return 1
    fi
    # Extraction en local (/tmp) puis copie, comme _extract_wine : pas de
    # liens durs sur les systèmes de fichiers réseau (shfs Unraid, CIFS…).
    # Dossier racine lu dans l'archive.
    local top_dir
    top_dir=$(tar -tzf "${tmp}/${tag}.tar.gz" 2>/dev/null | head -1 | cut -d/ -f1) || true
    if [[ -n "$top_dir" ]] && tar -xzf "${tmp}/${tag}.tar.gz" -C "$tmp" \
       && [[ -x "${tmp}/${top_dir}/proton" ]]; then
        mkdir -p "$PROTON_INSTALL_DIR"
        rm -rf "${PROTON_INSTALL_DIR:?}/${top_dir}"
        if cp -rp "${tmp}/${top_dir}" "${PROTON_INSTALL_DIR}/${top_dir}"; then
            rm -rf "$tmp"
            print_success "${tag} installé : ${PROTON_INSTALL_DIR}/${top_dir}"
            return 0
        fi
    fi
    print_error "Extraction échouée (archive corrompue ?)"
    rm -rf "$tmp"
    return 1
}

check_proton() {
    echo ""
    print_info "Vérification de Proton et umu (jeux PC récents)..."
    echo ""

    local found_umu found_proton
    found_umu=$(command -v umu-run 2>/dev/null) || true
    found_proton=$(find_proton) || true
    [[ -n "$found_umu"    ]] && print_success "umu-run trouvé : $found_umu"
    [[ -n "$found_proton" ]] && print_success "UMU-Proton trouvé : $found_proton"
    [[ -n "$found_umu" && -n "$found_proton" ]] && return 0

    echo ""
    print_info "Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes"
    echo "    créés par Proton (Heroic, Steam) se lancent via umu et Proton."
    echo "    Inutile pour les seuls jeux arcade Batocera (wine-tkg)."
    echo ""
    [[ -z "$found_umu"    ]] && echo "    umu-run   manquant  (umu-launcher, < 1 Mo)"
    [[ -z "$found_proton" ]] && echo "    UMU-Proton manquant (Proton de Valve, ~470 Mo — gère la vibration DualSense)"
    echo ""
    read -p "  Installer ce qui manque ? [o/N] " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[OoYy]$ ]]; then
        [[ -z "$found_umu"    ]] && { download_umu       || true; }
        [[ -z "$found_proton" ]] && { download_umu_proton || true; }
    else
        print_info "Sans umu, ces jeux seront lancés par le Wine de Proton seul (écran noir possible)"
    fi
}

install_script() {
    echo ""
    print_info "Installation de wsquashfs-launcher..."

    local src="wsquashfs-launcher"

    # Si le script n'est pas dispo localement, le télécharger
    if [[ ! -f "$src" ]]; then
        print_info "Téléchargement depuis GitHub..."
        local tmp
        tmp=$(mktemp)
        if command -v curl &>/dev/null; then
            curl -fsSL "$REPO_RAW/wsquashfs-launcher" -o "$tmp"
        elif command -v wget &>/dev/null; then
            wget -q "$REPO_RAW/wsquashfs-launcher" -O "$tmp"
        else
            print_error "curl ou wget requis pour le téléchargement"
            return 1
        fi
        src="$tmp"
    fi

    mkdir -p "$INSTALL_DIR"
    cp "$src" "$INSTALL_DIR/wsquashfs-launcher"
    chmod +x "$INSTALL_DIR/wsquashfs-launcher"

    [[ "$src" == /tmp/* ]] && rm -f "$src"
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

    if [[ -L "$INSTALL_DIR/umu-run" && "$(readlink "$INSTALL_DIR/umu-run")" == "$UMU_INSTALL_DIR/umu-run" ]]; then
        rm -f "$INSTALL_DIR/umu-run"
        rm -rf "$UMU_INSTALL_DIR"
        print_success "umu-run supprimé"
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
    echo "UMU-Proton est conservé (partagé avec Steam et Heroic) :"
    echo "  $PROTON_INSTALL_DIR/"
    echo ""
    echo "Pour tout supprimer :"
    echo "  rm -rf ~/.local/share/wsquashfs/ ~/.cache/wsquashfs/"
    echo ""
}

main() {
    print_header
    check_root
    print_info "Répertoire d'installation : $INSTALL_DIR"

    check_dependencies  || exit 1
    check_wine_batocera
    check_proton
    install_script      || exit 1
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
