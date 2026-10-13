#!/usr/bin/env bash
#
# install.sh — installe wsquashfs-launcher et ce dont il a besoin.
#
# Usage local  : bash install.sh [options]
# Usage distant: curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash
#
# Options :
#   --yes, -y        répond oui à toutes les questions (installation complète)
#   --dry-run, -n    montre ce qui serait fait, sans rien installer ni modifier
#   --uninstall, -u  désinstalle le lanceur (sauvegardes et runners gardés)
#   --help, -h
#
# Distributions prises en charge pour les paquets système :
#   - Debian, Ubuntu et dérivés (apt)
#   - Arch, CachyOS, Manjaro, EndeavourOS et dérivés (pacman)
# Ailleurs, la liste des paquets à installer à la main est affichée ; le
# reste (runners Wine/Proton, DXVK, VKD3D, d7vk, lanceur) est téléchargé et
# ne dépend pas de la distribution.
#
# Organisation du fichier :
#    1. Configuration
#    2. Affichage et questions
#    3. Exécution (simulation, sudo) et téléchargements
#    4. Paquets système : Debian (apt), Arch (pacman)
#    5. Runners Wine
#    6. Proton et umu
#    7. Rendu : DXVK, VKD3D-Proton, d7vk
#    8. Lanceur, pont DualSense, type MIME, PATH
#    9. Désinstallation
#   10. Point d'entrée

# ════════════════════════════════════════════════════════════════════════
# 1. CONFIGURATION
# ════════════════════════════════════════════════════════════════════════

REPO_RAW="https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main"

# Emplacements où le lanceur cherche chaque composant.
WSQ_DATA="$HOME/.local/share/wsquashfs"
WINE_INSTALL_DIR="${WSQ_DATA}/wine"
UMU_INSTALL_DIR="${WSQ_DATA}/umu"
DXVK_DIR="${WSQ_DATA}/dxvk"
VKD3D_DIR="${WSQ_DATA}/vkd3d-proton"
D7VK_DIR="${WSQ_DATA}/d7vk"
# Emplacement standard des Proton de Steam : le lanceur, Steam et Heroic
# les y trouvent tous.
PROTON_INSTALL_DIR="$HOME/.local/share/Steam/compatibilitytools.d"

# Dossier de ce script, pour une installation depuis le dépôt cloné (vide
# avec curl | bash : tout est alors téléchargé).
SCRIPT_DIR=""
if [[ -n "${BASH_SOURCE[0]:-}" && -f "${BASH_SOURCE[0]}" ]]; then
    SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
fi

# ── État (main, check_root, detect_distro) ─────────────────────────────
DRY_RUN=false
ASSUME_YES=false
INSTALL_DIR=""          # /usr/local/bin en root, ~/.local/bin sinon
SUDO=()                 # préfixe des commandes système (sudo hors root)
DISTRO=""               # debian | arch | autre
DISTRO_NAME=""
OS_RELEASE="${OS_RELEASE:-/etc/os-release}"   # surchargeable pour les tests

# ════════════════════════════════════════════════════════════════════════
# 2. AFFICHAGE ET QUESTIONS
# ════════════════════════════════════════════════════════════════════════

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

section()       { echo ""; echo -e "${BLUE}── $1${NC}"; }
print_success() { echo -e "${GREEN}✓${NC} $1"; }
print_error()   { echo -e "${RED}✗${NC} $1" >&2; }
print_info()    { echo -e "${YELLOW}→${NC} $1"; }

# Les questions sont lues sur le terminal (/dev/tty), jamais sur l'entrée
# standard : avec curl | bash, celle-ci est le script lui-même.
has_tty() {
    [[ -r /dev/tty && -w /dev/tty ]] && { : < /dev/tty; } 2>/dev/null
}

# ask_yes_no <question> <défaut o|n> : vrai pour oui. --yes : oui ; sans
# terminal : la réponse par défaut.
ask_yes_no() {
    local question="$1" default="$2" reply hint="[o/N]"
    [[ "$default" == o ]] && hint="[O/n]"
    if [[ "$ASSUME_YES" == true ]]; then
        echo "  ${question} ${hint} o (--yes)"
        return 0
    fi
    if ! has_tty; then
        echo "  ${question} ${hint} ${default} (pas de terminal)"
        [[ "$default" == o ]]
        return
    fi
    read -r -p "  ${question} ${hint} " reply < /dev/tty
    reply="${reply:-$default}"
    [[ "${reply,,}" == o* || "${reply,,}" == y* ]]
}

# ask_choice <question> <défaut> : un caractère ; --yes ou sans terminal :
# le défaut.
ask_choice() {
    local question="$1" default="$2" reply
    if [[ "$ASSUME_YES" == true ]] || ! has_tty; then
        echo "  ${question} ${default}"
        REPLY="$default"
        return
    fi
    read -r -p "  ${question} " -n 1 reply < /dev/tty
    echo ""
    REPLY="${reply:-$default}"
}

# ════════════════════════════════════════════════════════════════════════
# 3. EXÉCUTION ET TÉLÉCHARGEMENTS
# ════════════════════════════════════════════════════════════════════════

# run_root <commande...> : commande système (sudo hors root) ; en
# simulation, seulement affichée.
run_root() {
    if [[ "$DRY_RUN" == true ]]; then
        echo "    [simulation] ${SUDO[*]}${SUDO[*]:+ }$*"
        return 0
    fi
    "${SUDO[@]}" "$@"
}

check_root() {
    if [[ $EUID -eq 0 ]]; then
        INSTALL_DIR="/usr/local/bin"
        SUDO=()
    else
        INSTALL_DIR="$HOME/.local/bin"
        SUDO=(sudo)
    fi
}

# Télécharge url vers dest (fichier, ou - pour la sortie standard).
download() {
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

# Vrai si l'URL répond (simulation : vérifie les liens sans télécharger).
url_exists() {
    if command -v curl &>/dev/null; then
        curl -fsIL -o /dev/null "$1"
    else
        wget -q --spider "$1"
    fi
}

# Dernière version publiée d'un dépôt GitHub ($2 si l'API ne répond pas).
github_latest_tag() {
    local repo="$1" fallback="$2" tag
    tag=$(download "https://api.github.com/repos/${repo}/releases/latest" - 2>/dev/null \
        | grep '"tag_name"' | head -1 \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    echo "${tag:-$fallback}"
}

# fetch <url> <fichier> <libellé> : téléchargement, ou vérification du lien
# en simulation (faux si absent). Le contenu n'est alors pas utilisable.
fetch() {
    local url="$1" dest="$2" label="$3"
    if [[ "$DRY_RUN" == true ]]; then
        if url_exists "$url"; then
            echo "    [simulation] téléchargement de ${label} : ${url}"
            return 0
        fi
        print_error "${label} introuvable : ${url}"
        return 1
    fi
    print_info "Téléchargement de ${label}..."
    download "$url" "$dest" || { print_error "Téléchargement échoué : ${url}"; return 1; }
}

# extract <archive> <dossier> : tar (gz, xz, zst) ou zip.
extract() {
    local archive="$1" dest="$2"
    case "$archive" in
        *.zip)
            if command -v unzip &>/dev/null; then
                unzip -q "$archive" -d "$dest"
            elif command -v bsdtar &>/dev/null; then
                bsdtar -xf "$archive" -C "$dest"
            else
                python3 -I -m zipfile -e "$archive" "$dest"
            fi
            ;;
        *.tar.zst) tar --zstd -xf "$archive" -C "$dest" ;;
        *)         tar -xf "$archive" -C "$dest" ;;
    esac
}

# Dossier racine d'une archive extraite dans un dossier temporaire.
top_dir_of() {
    find "$1" -mindepth 1 -maxdepth 1 -type d | head -1
}

# install_tree <source> <destination> : remplace la destination. Copie
# depuis un dossier local (/tmp), sans liens durs : les systèmes de fichiers
# réseau (shfs Unraid, CIFS…) ne les prennent pas en charge.
install_tree() {
    rm -rf "${2:?}"
    mkdir -p "$(dirname "$2")"
    cp -rp "$1" "$2"
}

# ════════════════════════════════════════════════════════════════════════
# 4. PAQUETS SYSTÈME
# ════════════════════════════════════════════════════════════════════════

detect_distro() {
    local id="" like=""
    if [[ -r "$OS_RELEASE" ]]; then
        # shellcheck disable=SC1090  # fichier de la distribution
        id=$(. "$OS_RELEASE" && echo "${ID:-}")
        # shellcheck disable=SC1090
        like=$(. "$OS_RELEASE" && echo "${ID_LIKE:-}")
        # shellcheck disable=SC1090
        DISTRO_NAME=$(. "$OS_RELEASE" && echo "${PRETTY_NAME:-$id}")
    fi
    case " ${id} ${like} " in
        *" debian "*|*" ubuntu "*) DISTRO=debian ;;
        *" arch "*|*" cachyos "*)  DISTRO=arch ;;
        *)
            if command -v pacman &>/dev/null; then
                DISTRO=arch
            elif command -v apt-get &>/dev/null; then
                DISTRO=debian
            else
                DISTRO=autre
            fi
            ;;
    esac
    DISTRO_NAME="${DISTRO_NAME:-inconnue}"
}

# « installé(s) », ou « à installer » en simulation.
done_word() {
    if [[ "$DRY_RUN" == true ]]; then echo "à installer (simulation)"; else echo "installé(s)"; fi
}

need_sudo() {
    [[ ${#SUDO[@]} -eq 0 ]] && return 0
    if ! command -v sudo &>/dev/null; then
        print_error "sudo absent — relancez en root : sudo bash $0"
        return 1
    fi
    print_info "Non-root : sudo utilisé pour les paquets système"
}

# ── Debian / Ubuntu (apt) ──────────────────────────────────────────────

dpkg_installed() {
    dpkg -l "$1" 2>/dev/null | grep -q '^ii'
}

# apt_ensure <libellé> <paquet...> : installe ceux qui manquent.
apt_ensure() {
    local label="$1" missing=() p
    shift
    for p in "$@"; do
        dpkg_installed "$p" || missing+=("$p")
    done
    if ((${#missing[@]} == 0)); then
        print_success "${label} : déjà installé (${*})"
        return 0
    fi
    print_info "${label} : installation de ${missing[*]}..."
    if run_root apt-get install -y "${missing[@]}"; then
        print_success "${label} : ${missing[*]} $(done_word)"
        return 0
    fi
    print_error "${label} : échec de l'installation de ${missing[*]}"
    return 1
}

deps_debian() {
    local has_error=false
    # wine32:i386 : Wine système et bibliothèques 32 bits. libxkbcommon0:i386 :
    # wine-tkg (Kron4ek, non WoW64) fait tourner les jeux 32 bits avec de
    # vraies bibliothèques 32 bits ; sans elle, winewayland.drv 32 bits ne se
    # charge pas et un jeu 32 bits n'a aucun pilote d'affichage ("no driver
    # could be loaded", constaté sur Virtua Tennis 4).
    if ! dpkg_installed wine32:i386; then
        if ! { run_root dpkg --add-architecture i386 && run_root apt-get update; }; then
            has_error=true
        fi
    fi
    apt_ensure "Wine 32 bits" wine32:i386 libxkbcommon0:i386 || has_error=true
    apt_ensure "Images squashfs" squashfuse squashfs-tools xz-utils || has_error=true
    apt_ensure "Mode overlay" fuse-overlayfs \
        || print_info "fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)"
    # getfattr : --clean ; zstd : --backup et VKD3D-Proton ; unzip : d7vk
    apt_ensure "Outils (--clean, --backup, archives)" attr zstd unzip || true
    # MangoHud (optionnel : mangohud wsquashfs-launcher jeu.wsquashfs)
    apt_ensure "MangoHud (facultatif)" mangohud mangohud:i386 \
        || apt_ensure "MangoHud (facultatif)" mangohud \
        || print_info "mangohud indisponible — overlay MangoHud non disponible"
    [[ "$has_error" == false ]]
}

# ── Arch, CachyOS, Manjaro… (pacman) ───────────────────────────────────

pacman_installed() {
    pacman -Q "$1" &>/dev/null
}

pacman_available() {
    pacman -Si "$1" &>/dev/null
}

# Bases de paquets synchronisées : sans elles, pacman -Si ne trouve rien,
# pas même pacman lui-même.
pacman_synced() {
    pacman -Si pacman &>/dev/null
}

multilib_enabled() {
    if command -v pacman-conf &>/dev/null; then
        pacman-conf --repo-list 2>/dev/null | grep -qx multilib
    else
        grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null
    fi
}

# pacman_ensure <libellé> <paquet...> : installe ceux qui manquent et
# existent dans les dépôts ; les autres sont signalés. Faux si l'installation
# échoue.
pacman_ensure() {
    local label="$1" missing=() unknown=() p opts=(-S --needed)
    shift
    for p in "$@"; do
        pacman_installed "$p" && continue
        if pacman_available "$p"; then
            missing+=("$p")
        else
            unknown+=("$p")
        fi
    done
    ((${#unknown[@]})) && print_info "${label} : absents des dépôts, ignorés : ${unknown[*]}"
    if ((${#missing[@]} == 0)); then
        ((${#unknown[@]})) || print_success "${label} : déjà installé"
        return 0
    fi
    print_info "${label} : installation de ${missing[*]}..."
    # Confirmation de pacman au terminal ; sans terminal ou avec --yes, aucune.
    local rc
    if [[ "$ASSUME_YES" == true ]] || ! has_tty; then
        opts+=(--noconfirm)
        run_root pacman "${opts[@]}" "${missing[@]}"
        rc=$?
    else
        run_root pacman "${opts[@]}" "${missing[@]}" < /dev/tty
        rc=$?
    fi
    if ((rc == 0)); then
        print_success "${label} : ${missing[*]} $(done_word)"
        return 0
    fi
    print_error "${label} : échec de l'installation de ${missing[*]}"
    return 1
}

# Bibliothèques 32 bits (multilib) de wine-tkg (Kron4ek, non WoW64), qui
# lance les prefixes 32 bits des jeux arcade Batocera : l'équivalent de
# wine32:i386 sous Debian. Plus la version 32 bits des pilotes Vulkan/OpenGL
# installés (NVIDIA, AMD, Intel) : sans elle, un jeu 32 bits n'a ni Vulkan
# (DXVK) ni accélération.
arch_lib32_packages() {
    local pkgs=(
        lib32-glibc lib32-gcc-libs
        lib32-libx11 lib32-libxext lib32-libxrender lib32-libxrandr lib32-libxi
        lib32-libxcursor lib32-libxcomposite lib32-libxinerama lib32-libxfixes
        lib32-libxkbcommon lib32-wayland
        lib32-freetype2 lib32-fontconfig lib32-gnutls
        lib32-libpulse lib32-alsa-lib lib32-alsa-plugins
        lib32-mesa lib32-vulkan-icd-loader lib32-sdl2
        lib32-gst-plugins-base-libs
    )
    local driver
    for driver in nvidia-utils vulkan-radeon vulkan-intel; do
        pacman_installed "$driver" && pkgs+=("lib32-${driver}")
    done
    printf '%s\n' "${pkgs[@]}"
}

deps_arch() {
    local has_error=false lib32=()
    if ! pacman_synced; then
        print_error "Bases pacman non synchronisées : lancez d'abord « sudo pacman -Syu »"
        return 1
    fi
    pacman_ensure "Images squashfs" squashfuse squashfs-tools xz || has_error=true
    pacman_ensure "Mode overlay" fuse-overlayfs \
        || print_info "fuse-overlayfs indisponible — mode copy utilisé (fonctionnel)"
    pacman_ensure "Outils (--clean, --backup, archives)" attr zstd unzip || true
    if multilib_enabled; then
        mapfile -t lib32 < <(arch_lib32_packages)
        pacman_ensure "Bibliothèques 32 bits (wine-tkg, jeux 32 bits)" "${lib32[@]}" || has_error=true
    else
        print_error "Dépôt [multilib] désactivé : les jeux 32 bits (arcade Batocera) ne se lanceront pas."
        echo "    Activez-le dans /etc/pacman.conf (section [multilib]), puis relancez l'installation."
        has_error=true
    fi
    # MangoHud (optionnel : mangohud wsquashfs-launcher jeu.wsquashfs)
    if multilib_enabled; then
        pacman_ensure "MangoHud (facultatif)" mangohud lib32-mangohud || true
    else
        pacman_ensure "MangoHud (facultatif)" mangohud || true
    fi
    [[ "$has_error" == false ]]
}

deps_other() {
    print_error "Distribution non prise en charge pour les paquets système (${DISTRO_NAME})."
    echo "    Installez à la main : squashfuse, squashfs-tools, fuse-overlayfs, attr,"
    echo "    zstd, unzip, et les bibliothèques 32 bits de Wine (jeux arcade 32 bits)."
    ask_yes_no "Continuer (runners, DXVK, lanceur) ?" n
}

check_dependencies() {
    section "Paquets système (${DISTRO_NAME})"
    need_sudo || return 1
    case "$DISTRO" in
        debian) deps_debian ;;
        arch)   deps_arch ;;
        *)      deps_other ;;
    esac
}

# ════════════════════════════════════════════════════════════════════════
# 5. RUNNERS WINE
# ════════════════════════════════════════════════════════════════════════

# install_wine_archive <url> <fichier> <libellé> [nom du dossier] : télécharge
# et installe un runner Wine dans WINE_INSTALL_DIR.
install_wine_archive() {
    local url="$1" file="$2" label="$3" name="${4:-}" tmp top wine
    tmp=$(mktemp -d)
    if fetch "$url" "${tmp}/${file}" "$label" && [[ "$DRY_RUN" == true ]]; then
        rm -rf "$tmp"
        return 0
    fi
    if [[ -f "${tmp}/${file}" ]] && mkdir -p "${tmp}/x" && extract "${tmp}/${file}" "${tmp}/x" \
       && top=$(top_dir_of "${tmp}/x") && [[ -n "$top" ]]; then
        name="${name:-$(basename "$top")}"
        install_tree "$top" "${WINE_INSTALL_DIR}/${name}"
        wine=$(find "${WINE_INSTALL_DIR}/${name}" -maxdepth 2 -name wine -path '*/bin/wine' | head -1)
        if [[ -x "$wine" ]]; then
            rm -rf "$tmp"
            print_success "${label} installé : ${wine}"
            return 0
        fi
    fi
    rm -rf "$tmp"
    print_error "${label} : installation échouée"
    return 1
}

# wine-tkg (Kron4ek, staging-tkg, non WoW64) : le Wine par défaut du lanceur
# pour les jeux arcade Batocera, prefixes 32 bits compris.
download_wine_tkg() {
    local version file
    version=$(github_latest_tag "Kron4ek/Wine-Builds" "11.19")
    file="wine-${version}-staging-tkg-amd64.tar.xz"
    install_wine_archive "https://github.com/Kron4ek/Wine-Builds/releases/download/${version}/${file}" \
        "$file" "wine-tkg ${version} (~100 Mo)"
}

download_wine_ge() {
    local tag file
    tag=$(github_latest_tag "GloriousEggroll/wine-ge-custom" "GE-Proton8-26")
    file="wine-lutris-${tag}-x86_64.tar.xz"
    install_wine_archive "https://github.com/GloriousEggroll/wine-ge-custom/releases/download/${tag}/${file}" \
        "$file" "wine-ge ${tag} (~234 Mo)" "wine-ge-${tag}"
}

check_wine_runners() {
    local found_tkg found_ge need_tkg=false need_ge=false
    section "Wine pour les jeux arcade Batocera"
    found_tkg=$(find "$WINE_INSTALL_DIR" /opt /usr/wine/wine-tkg -maxdepth 3 -name wine \
                    -path '*/bin/wine' 2>/dev/null | grep -i tkg | head -1)
    found_ge=$(find "$WINE_INSTALL_DIR" /usr/wine/ge-custom /usr/wine/wine-proton -maxdepth 3 -name wine \
                   -path '*/bin/wine' 2>/dev/null | grep -iE 'ge-|lutris|wine-proton' | head -1)
    if [[ -n "$found_tkg" ]]; then print_success "wine-tkg : ${found_tkg}"; else need_tkg=true; fi
    if [[ -n "$found_ge" ]];  then print_success "wine-ge  : ${found_ge}";  else need_ge=true; fi
    [[ "$need_tkg" == false && "$need_ge" == false ]] && return 0

    echo "    Les prefixes .wsquashfs Batocera sont conçus pour wine-tkg ou wine-ge ;"
    echo "    le Wine système peut provoquer des incompatibilités de DLL."
    [[ "$need_tkg" == true ]] && echo "    wine-tkg  manquant  (Kron4ek staging, ~100 Mo) — runner par défaut"
    [[ "$need_ge"  == true ]] && echo "    wine-ge   manquant  (GloriousEggroll, ~234 Mo) — alternative"
    echo "    1) Installer ce qui manque   2) wine-tkg seulement   3) wine-ge seulement   4) Ignorer"
    ask_choice "Choix [1-4] :" 2
    case "$REPLY" in
        1) [[ "$need_tkg" == true ]] && download_wine_tkg
           [[ "$need_ge"  == true ]] && download_wine_ge ;;
        2) [[ "$need_tkg" == true ]] && download_wine_tkg ;;
        3) [[ "$need_ge"  == true ]] && download_wine_ge ;;
        *) print_info "Ignoré : le Wine système sera utilisé (certaines DLL peuvent manquer)" ;;
    esac
    return 0
}

# ════════════════════════════════════════════════════════════════════════
# 6. PROTON ET UMU
# ════════════════════════════════════════════════════════════════════════
# Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes créés
# par Proton (Heroic, Steam) sont lancés via umu-run et un Proton, comme
# Heroic : le Wine de Proton lancé seul donnait écran noir ou plantage.

# Un Proton que le lanceur choisit pour ces jeux (proton-cachyos, puis
# UMU-Proton : Proton de Valve, leur SDL gère la DualSense en XInput avec la
# vibration ; GE-Proton la force en hidraw, sans vibration en XInput).
find_proton() {
    local base d
    for base in "$PROTON_INSTALL_DIR" "$HOME/.steam/root/compatibilitytools.d" \
                "$HOME/.config/heroic/tools/proton" /usr/share/steam/compatibilitytools.d; do
        for d in "$base"/proton-cachyos* "$base"/UMU-Proton*; do
            [[ -x "${d}/proton" && -x "${d}/files/bin/wine" ]] && { echo "$d"; return 0; }
        done
    done 2>/dev/null
    return 1
}

# umu-run en zipapp (sans paquet de distribution) : ~/.local/share/wsquashfs/umu.
download_umu() {
    local tag file tmp
    command -v python3 &>/dev/null || { print_error "python3 requis pour umu-run"; return 1; }
    tag=$(github_latest_tag "Open-Wine-Components/umu-launcher" "1.4.4")
    file="umu-launcher-${tag}-zipapp.tar"
    tmp=$(mktemp -d)
    if fetch "https://github.com/Open-Wine-Components/umu-launcher/releases/download/${tag}/${file}" \
             "${tmp}/${file}" "umu-launcher ${tag} (< 1 Mo)" && [[ "$DRY_RUN" == true ]]; then
        rm -rf "$tmp"
        return 0
    fi
    if [[ -f "${tmp}/${file}" ]] && tar -xf "${tmp}/${file}" -C "$tmp" && [[ -f "${tmp}/umu/umu-run" ]]; then
        rm -rf "$UMU_INSTALL_DIR"
        mkdir -p "$UMU_INSTALL_DIR" "$INSTALL_DIR"
        cp "${tmp}/umu/umu-run" "${UMU_INSTALL_DIR}/umu-run"
        chmod +x "${UMU_INSTALL_DIR}/umu-run"
        ln -sfn "${UMU_INSTALL_DIR}/umu-run" "${INSTALL_DIR}/umu-run"
        rm -rf "$tmp"
        print_success "umu-run ${tag} installé : ${INSTALL_DIR}/umu-run"
        return 0
    fi
    rm -rf "$tmp"
    print_error "umu-launcher : installation échouée"
    return 1
}

install_umu() {
    # Paquet de la distribution s'il existe (Arch, CachyOS : umu-launcher).
    if [[ "$DISTRO" == arch ]] && pacman_synced && pacman_available umu-launcher; then
        pacman_ensure "umu-launcher" umu-launcher
    else
        download_umu
    fi
}

download_umu_proton() {
    local tag base tmp top
    tag=$(github_latest_tag "Open-Wine-Components/umu-proton" "UMU-Proton-10.0-4")
    base="https://github.com/Open-Wine-Components/umu-proton/releases/download/${tag}"
    tmp=$(mktemp -d)
    if fetch "${base}/${tag}.tar.gz" "${tmp}/${tag}.tar.gz" "${tag} (~470 Mo)" && [[ "$DRY_RUN" == true ]]; then
        rm -rf "$tmp"
        return 0
    fi
    if [[ ! -f "${tmp}/${tag}.tar.gz" ]] || ! download "${base}/${tag}.sha512sum" "${tmp}/${tag}.sha512sum"; then
        rm -rf "$tmp"
        return 1
    fi
    if ! (cd "$tmp" && sha512sum -c --quiet "${tag}.sha512sum" >/dev/null 2>&1); then
        print_error "Somme de contrôle SHA-512 incorrecte, archive ignorée"
        rm -rf "$tmp"
        return 1
    fi
    if mkdir -p "${tmp}/x" && extract "${tmp}/${tag}.tar.gz" "${tmp}/x" \
       && top=$(top_dir_of "${tmp}/x") && [[ -x "${top}/proton" ]]; then
        install_tree "$top" "${PROTON_INSTALL_DIR}/$(basename "$top")"
        rm -rf "$tmp"
        print_success "${tag} installé : ${PROTON_INSTALL_DIR}/$(basename "$top")"
        return 0
    fi
    rm -rf "$tmp"
    print_error "${tag} : extraction échouée (archive corrompue ?)"
    return 1
}

install_proton() {
    local pkg
    # CachyOS : proton-cachyos en paquet, le Proton préféré du lanceur ; la
    # variante -slr (Steam Linux Runtime, celui qu'utilise umu) d'abord.
    if [[ "$DISTRO" == arch ]] && pacman_synced; then
        for pkg in proton-cachyos-slr proton-cachyos; do
            if pacman_available "$pkg"; then
                pacman_ensure "$pkg" "$pkg"
                return
            fi
        done
    fi
    download_umu_proton
}

check_proton() {
    local found_umu found_proton
    section "Proton et umu (jeux PC récents)"
    found_umu=$(command -v umu-run 2>/dev/null)
    found_proton=$(find_proton)
    [[ -n "$found_umu"    ]] && print_success "umu-run : ${found_umu}"
    [[ -n "$found_proton" ]] && print_success "Proton  : ${found_proton}"
    [[ -n "$found_umu" && -n "$found_proton" ]] && return 0

    echo "    Les .wsquashfs sans prefix (jeux PC emballés seuls) et les prefixes"
    echo "    créés par Proton (Heroic, Steam) se lancent via umu et Proton."
    echo "    Inutile pour les seuls jeux arcade Batocera (wine-tkg)."
    [[ -z "$found_umu"    ]] && echo "    umu-run manquant"
    [[ -z "$found_proton" ]] && echo "    Proton manquant (proton-cachyos ou UMU-Proton)"
    if ask_yes_no "Installer ce qui manque ?" o; then
        [[ -z "$found_umu"    ]] && install_umu
        [[ -z "$found_proton" ]] && install_proton
    else
        print_info "Sans umu, ces jeux seront lancés par le Wine de Proton seul (écran noir possible)"
    fi
    return 0
}

# ════════════════════════════════════════════════════════════════════════
# 7. RENDU : DXVK, VKD3D-PROTON, D7VK
# ════════════════════════════════════════════════════════════════════════
# Activés par défaut par le lanceur (sinon rendu OpenGL de Wine, llvmpipe
# sous Xwayland + NVIDIA) et cherchés dans /usr/share/<nom> ou
# ~/.local/share/wsquashfs/<nom>. Version Windows, celle des releases GitHub ;
# leur structure (x64/, x32/ ou x86/) est celle qu'attend le lanceur. Sans
# effet sur les jeux lancés par Proton, qui a les siens.

# install_dll_pack <libellé> <dépôt> <version de repli> <nom de l'archive,
# {tag} et {ver} remplacés> <destination> <fichier témoin>
install_dll_pack() {
    local label="$1" repo="$2" fallback="$3" pattern="$4" dest="$5" probe="$6"
    local tag ver file tmp top
    tag=$(github_latest_tag "$repo" "$fallback")
    ver="${tag#v}"
    file="${pattern//\{tag\}/$tag}"
    file="${file//\{ver\}/$ver}"
    tmp=$(mktemp -d)
    if fetch "https://github.com/${repo}/releases/download/${tag}/${file}" "${tmp}/${file}" \
             "${label} ${tag}" && [[ "$DRY_RUN" == true ]]; then
        rm -rf "$tmp"
        return 0
    fi
    if [[ -f "${tmp}/${file}" ]] && mkdir -p "${tmp}/x" && extract "${tmp}/${file}" "${tmp}/x" \
       && top=$(top_dir_of "${tmp}/x") && [[ -f "${top}/${probe}" ]]; then
        install_tree "$top" "$dest"
        echo "$tag" > "${dest}/VERSION"
        rm -rf "$tmp"
        print_success "${label} ${tag} installé : ${dest}"
        return 0
    fi
    rm -rf "$tmp"
    print_error "${label} : installation échouée"
    return 1
}

# Chemin déjà connu du lanceur pour un composant, vide sinon.
installed_component() {
    local probe="$1" d
    shift
    for d in "$@"; do
        [[ -f "${d}/${probe}" ]] && { echo "$d"; return 0; }
    done
    return 1
}

check_rendering() {
    local dxvk vkd3d d7vk
    section "Rendu : DXVK, VKD3D-Proton, d7vk"
    dxvk=$(installed_component x64/dxgi.dll /usr/share/dxvk "$DXVK_DIR")
    vkd3d=$(installed_component x64/d3d12.dll /usr/share/vkd3d-proton "$VKD3D_DIR")
    d7vk=$(installed_component x32/ddraw.dll /usr/share/d7vk "$D7VK_DIR")
    [[ -n "$dxvk"  ]] && print_success "DXVK : ${dxvk}"
    [[ -n "$vkd3d" ]] && print_success "VKD3D-Proton : ${vkd3d}"
    [[ -n "$d7vk"  ]] && print_success "d7vk : ${d7vk}"
    [[ -n "$dxvk" && -n "$vkd3d" && -n "$d7vk" ]] && return 0

    echo "    Direct3D 8-11 (DXVK), 12 (VKD3D-Proton) et DirectDraw (d7vk) traduits"
    echo "    en Vulkan pour les jeux lancés par Wine ; sans eux, rendu OpenGL de Wine."
    if ! ask_yes_no "Installer ce qui manque (~25 Mo) ?" o; then
        print_info "Ignoré : rendu via wined3d (OpenGL) pour les jeux arcade"
        return 0
    fi
    [[ -n "$dxvk" ]] || install_dll_pack "DXVK" doitsujin/dxvk v3.1.1 \
        "dxvk-{ver}.tar.gz" "$DXVK_DIR" x64/dxgi.dll
    if [[ -z "$vkd3d" ]]; then
        if command -v zstd &>/dev/null || [[ "$DRY_RUN" == true ]]; then
            install_dll_pack "VKD3D-Proton" HansKristian-Work/vkd3d-proton v3.0.1 \
                "vkd3d-proton-{ver}.tar.zst" "$VKD3D_DIR" x64/d3d12.dll
        else
            print_error "VKD3D-Proton : zstd requis pour l'archive (paquet zstd)"
        fi
    fi
    [[ -n "$d7vk" ]] || install_dll_pack "d7vk" WinterSnowfall/d7vk v2.3 \
        "d7vk-{tag}.zip" "$D7VK_DIR" x32/ddraw.dll
    return 0
}

# ════════════════════════════════════════════════════════════════════════
# 8. LANCEUR, PONT DUALSENSE, TYPE MIME, PATH
# ════════════════════════════════════════════════════════════════════════

# install_repo_file <chemin dans le dépôt> <destination> : copie depuis le
# dépôt cloné, sinon téléchargement depuis GitHub.
install_repo_file() {
    local rel="$1" dest="$2" tmp=""
    if [[ "$DRY_RUN" == true ]]; then
        echo "    [simulation] ${rel} → ${dest}"
        return 0
    fi
    mkdir -p "$(dirname "$dest")"
    # Copie à côté puis renommage : un lanceur en cours d'exécution (partie
    # en cours) garde son fichier, jamais réécrit sous lui.
    tmp="${dest}.new.$$"
    if [[ -n "$SCRIPT_DIR" && -f "${SCRIPT_DIR}/${rel}" ]]; then
        cp "${SCRIPT_DIR}/${rel}" "$tmp"
    else
        download "${REPO_RAW}/${rel}" - > "$tmp"
    fi || {
        rm -f "$tmp"
        print_error "Copie ou téléchargement de ${rel} impossible"
        return 1
    }
    chmod +x "$tmp" && mv -f "$tmp" "$dest"
}

install_launcher() {
    section "Lanceur"
    install_repo_file wsquashfs-launcher "${INSTALL_DIR}/wsquashfs-launcher" || return 1
    print_success "Lanceur : ${INSTALL_DIR}/wsquashfs-launcher"
    # Pont DualSense Bluetooth → USB, démarré par le lanceur en HIDRAW=1
    # (F1 22 : les libScePad de 2022 refusent la DualSense Bluetooth).
    install_repo_file tools/dualsense-usb-bridge/dualsense-usb-bridge "${INSTALL_DIR}/dualsense-usb-bridge" \
        && print_success "Pont DualSense : ${INSTALL_DIR}/dualsense-usb-bridge"
    return 0
}

create_mime_type() {
    local mime_dir="$HOME/.local/share/mime/packages"
    ask_yes_no "Associer les fichiers .wsquashfs au lanceur (type MIME) ?" n || return 0
    if [[ "$DRY_RUN" == true ]]; then
        echo "    [simulation] ${mime_dir}/wsquashfs.xml"
        return 0
    fi
    mkdir -p "$mime_dir"
    cat > "${mime_dir}/wsquashfs.xml" << 'EOF'
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
    command -v update-mime-database &>/dev/null \
        && update-mime-database "$HOME/.local/share/mime" 2>/dev/null
    print_success "Type MIME créé"
}

# Fichier de configuration du shell de connexion de l'utilisateur.
shell_rc_file() {
    case "$(basename "${SHELL:-}")" in
        zsh)  echo "$HOME/.zshrc" ;;
        fish) echo "$HOME/.config/fish/config.fish" ;;
        bash) echo "$HOME/.bashrc" ;;
    esac
}

check_path() {
    local rc line
    if [[ ":$PATH:" == *":${INSTALL_DIR}:"* ]]; then
        print_success "${INSTALL_DIR} est dans le PATH"
        return 0
    fi
    print_info "${INSTALL_DIR} n'est pas dans le PATH"
    rc=$(shell_rc_file)
    if [[ "$(basename "${SHELL:-}")" == fish ]]; then
        line="fish_add_path ${INSTALL_DIR}"
    else
        line="export PATH=\"\$PATH:${INSTALL_DIR}\""
    fi
    if [[ -n "$rc" ]] && ask_yes_no "Ajouter à ${rc} ?" n; then
        if [[ "$DRY_RUN" == true ]]; then
            echo "    [simulation] ${line} >> ${rc}"
            return 0
        fi
        mkdir -p "$(dirname "$rc")"
        printf '\n# WSquashFS Launcher\n%s\n' "$line" >> "$rc"
        print_success "PATH mis à jour dans ${rc} (nouveau terminal pour en profiter)"
    else
        echo "    À ajouter à votre shell : ${line}"
    fi
}

show_usage() {
    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║       Installation terminée !          ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
    echo ""
    echo "  wsquashfs-launcher /chemin/jeu.wsquashfs"
    echo "  wsquashfs-launcher --doctor        (vérifier l'installation)"
    echo "  wsquashfs-launcher --list          (jeux connus et sauvegardes)"
    echo "  wsquashfs-launcher --help"
    echo ""
}

# ════════════════════════════════════════════════════════════════════════
# 9. DÉSINSTALLATION
# ════════════════════════════════════════════════════════════════════════

# remove <chemin...> : suppression, seulement affichée en simulation.
remove() {
    if [[ "$DRY_RUN" == true ]]; then
        echo "    [simulation] suppression de $*"
        return 0
    fi
    rm -rf -- "$@"
}

uninstall() {
    print_header
    print_info "Désinstallation de WSquashFS Launcher..."
    [[ "$DRY_RUN" == true ]] && print_info "Simulation : rien ne sera supprimé"
    echo ""
    if [[ -f "${INSTALL_DIR}/wsquashfs-launcher" ]]; then
        remove "${INSTALL_DIR}/wsquashfs-launcher" && print_success "Lanceur supprimé"
    else
        print_info "Lanceur absent de ${INSTALL_DIR}"
    fi
    [[ -e "${INSTALL_DIR}/dualsense-usb-bridge" ]] && remove "${INSTALL_DIR}/dualsense-usb-bridge"
    if [[ -L "${INSTALL_DIR}/umu-run" && "$(readlink "${INSTALL_DIR}/umu-run")" == "${UMU_INSTALL_DIR}/umu-run" ]]; then
        remove "${INSTALL_DIR}/umu-run" "$UMU_INSTALL_DIR" && print_success "umu-run supprimé"
    fi
    if [[ -f "$HOME/.local/share/mime/packages/wsquashfs.xml" ]]; then
        remove "$HOME/.local/share/mime/packages/wsquashfs.xml"
        [[ "$DRY_RUN" == false ]] && command -v update-mime-database &>/dev/null \
            && update-mime-database "$HOME/.local/share/mime" 2>/dev/null
        print_success "Type MIME supprimé"
    fi
    echo ""
    print_info "Conservés (sauvegardes, cache, runners, DXVK/VKD3D/d7vk, archives) :"
    echo "  ${WSQ_DATA}/"
    echo "  ~/.cache/wsquashfs/"
    echo "Conservé (partagé avec Steam et Heroic) : ${PROTON_INSTALL_DIR}/"
    echo "Paquets système conservés."
    echo ""
    echo "Pour tout supprimer : rm -rf ${WSQ_DATA}/ ~/.cache/wsquashfs/"
    echo ""
    return 0
}

# ════════════════════════════════════════════════════════════════════════
# 10. POINT D'ENTRÉE
# ════════════════════════════════════════════════════════════════════════

usage() {
    if [[ -n "$SCRIPT_DIR" ]]; then
        sed -n '3,19p' "${SCRIPT_DIR}/$(basename "${BASH_SOURCE[0]}")" | sed 's/^# \{0,1\}//'
    else
        echo "Usage : install.sh [--yes] [--dry-run] [--uninstall] [--help]"
    fi
}

install_all() {
    print_header
    check_root
    detect_distro
    print_info "Distribution : ${DISTRO_NAME} (${DISTRO})"
    print_info "Lanceur installé dans : ${INSTALL_DIR}"
    [[ "$DRY_RUN" == true ]] && print_info "Simulation : rien ne sera installé ni modifié"

    if ! check_dependencies; then
        print_error "Paquets système incomplets : voir ci-dessus"
        ask_yes_no "Continuer malgré tout ?" n || exit 1
    fi
    check_wine_runners
    check_proton
    check_rendering
    install_launcher || exit 1
    create_mime_type
    check_path
    show_usage
    # Bilan réel de ce qui est en place
    if [[ "$DRY_RUN" == false && -x "${INSTALL_DIR}/wsquashfs-launcher" ]]; then
        section "Vérification (wsquashfs-launcher --doctor)"
        "${INSTALL_DIR}/wsquashfs-launcher" --doctor
    fi
}

main() {
    local action=install arg
    for arg in "$@"; do
        case "$arg" in
            --yes|-y)       ASSUME_YES=true ;;
            --dry-run|-n)   DRY_RUN=true ;;
            --uninstall|-u) action=uninstall ;;
            --help|-h)      action=help ;;
            *) print_error "Option inconnue : ${arg}"; usage; exit 1 ;;
        esac
    done
    case "$action" in
        help)      usage ;;
        uninstall) check_root; uninstall ;;
        install)   install_all ;;
    esac
}

# Exécuté (et non chargé par « source », pour tester ses fonctions) : on lance.
# exit dans le même bloc : bash lit un script au fur et à mesure, et ne doit
# jamais relire ce fichier après main — s'il a été modifié ou remplacé
# pendant l'exécution, il lirait du texte décalé (« erreur de syntaxe près
# de ;; », 13/10).
if [[ "${BASH_SOURCE[0]:-}" == "$0" || -z "${BASH_SOURCE[0]:-}" ]]; then
    main "$@"
    exit
fi
