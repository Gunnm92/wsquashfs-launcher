#!/usr/bin/env bash
#
# Tests de non-régression de wsquashfs-launcher.
#
# Chaque scénario lance le vrai script dans un HOME isolé, avec de vrais
# montages FUSE (squashfuse, fuse-overlayfs) mais un faux Wine, un faux
# Proton et un faux umu-run qui journalisent leurs appels (arguments et
# variables d'environnement) au lieu de lancer quoi que ce soit. La trace
# complète (sorties standard et d'erreur, codes de retour, appels Wine,
# arborescence des sauvegardes et du cache) est comparée à
# tests/expected.out.
#
# Usage :
#   tests/run-tests.sh            exécute les tests, affiche les différences
#   tests/run-tests.sh --update   réécrit tests/expected.out (après un
#                                 changement de comportement voulu : relire
#                                 le diff avant de commiter)
#   tests/run-tests.sh --keep     garde le dossier de travail
#
# Codes de retour : 0 réussite, 1 différences, 77 prérequis absents.

set -u

TESTS_DIR=$(dirname "$(realpath "$0")")
LAUNCHER=$(realpath "${WSQUASHFS_LAUNCHER:-${TESTS_DIR}/../wsquashfs-launcher}")
EXPECTED="${TESTS_DIR}/expected.out"
UPDATE=false
KEEP=false
for arg in "$@"; do
    case "$arg" in
        --update) UPDATE=true ;;
        --keep)   KEEP=true ;;
        *) echo "Usage : $0 [--update] [--keep]" >&2; exit 2 ;;
    esac
done

# ── Prérequis ──────────────────────────────────────────────────────────

missing=()
for tool in squashfuse fuse-overlayfs mksquashfs unsquashfs getfattr setfattr flock fusermount3 tar zstd; do
    command -v "$tool" &>/dev/null || missing+=("$tool")
done
[[ -w /dev/fuse ]] || missing+=("/dev/fuse (accès en écriture)")
if ((${#missing[@]})); then
    echo "Tests ignorés, prérequis absents : ${missing[*]}" >&2
    exit 77
fi
# Les runners installés pour tout le système sont aussi vus par le lanceur et
# peuvent changer ses choix.
for d in /usr/wine /usr/share/steam/compatibilitytools.d; do
    [[ -d "$d" ]] && echo "Attention : ${d} présent, les résultats peuvent différer" >&2
done
compgen -G '/opt/*/bin/wine' >/dev/null && echo "Attention : Wine dans /opt, les résultats peuvent différer" >&2

ROOT=$(mktemp -d "${TMPDIR:-/tmp}/wsquashfs-tests.XXXXXX")
OUT="${ROOT}/actual.out"
BG_PIDS=()

# shellcheck disable=SC2317  # appelée par trap
finish() {
    local m pid
    for pid in "${BG_PIDS[@]}"; do kill "$pid" 2>/dev/null; done
    # Montages restés en place (test interrompu)
    mount | grep -F "$ROOT" | sed -E 's/^[^ ]+ on (.*) type .*/\1/' | sort -r \
        | while IFS= read -r m; do fusermount3 -uz "$m" 2>/dev/null; done
    if [[ "$KEEP" == true ]]; then
        echo "Dossier de travail : $ROOT"
    else
        rm -rf "$ROOT"
    fi
}
trap finish EXIT

# ── Environnement isolé ────────────────────────────────────────────────

export HOME="${ROOT}/home"
export WSQUASHFS_CACHE="${ROOT}/cache" WSQUASHFS_SAVES_DIR="${ROOT}/saves"
export FAKELOG="${ROOT}/wine.log"
export USER=wsqtest
export LANG=C
unset LC_ALL WINEDEBUG WINEDLLOVERRIDES WINEPREFIX WINEARCH WINEUSERNAME DISPLAY \
      GAMEID PROTONPATH WSQUASHFS_UMU WSQUASHFS_HIDRAW WSQUASHFS_DXVK WSQUASHFS_D7VK \
      WSQUASHFS_VIRTUAL_DESKTOP WSQUASHFS_BATOCERA_SAVES SUDO_USER
FAKEBIN="${ROOT}/bin"
mkdir -p "$HOME" "$FAKEBIN"
# Faux sysfs hidraw vide par défaut : une manette Sony branchée sur la
# machine de test changeait la trace (17/10). Les scénarios hidraw
# désignent le leur (HIDRAW_SYSFS="$ROOT/sysfs").
export HIDRAW_SYSFS="${ROOT}/no-sysfs"

# mkwine <racine> : faux runner Wine (classique, avec i386-unix) dont bin/wine
# journalise ses appels ; wineboot crée un prefix minimal.
mkwine() {
    mkdir -p "$1/bin" "$1/lib/wine/i386-unix" "$1/lib/wine/x86_64-windows" \
             "$1/lib/wine/i386-windows" "$1/share/wine"
    cat > "$1/bin/wine" <<'EOF'
#!/bin/bash
{
  echo "WINE $(basename "$(dirname "$(dirname "$0")")") cwd=${PWD} args:$(printf ' [%s]' "$@")"
  for v in WINEPREFIX WINEARCH WINEDLLOVERRIDES WINEDEBUG WINEUSERNAME WINEESYNC WINEFSYNC LC_ALL \
           PROTON_USE_WINED3D PROTON_DLL_COPY PROTON_SONY_HIDRAW_XINPUT GAMEID PROTONPATH FOO; do
    [[ -n "${!v+x}" ]] && echo "  $v=${!v}"
  done
} >> "$FAKELOG"
case "$1" in
  --version) echo "wine-9.0 (fake)" ;;
  wineboot)  mkdir -p "$WINEPREFIX/drive_c/windows/system32" "$WINEPREFIX/drive_c/users/$USER"
             echo "WINE REGISTRY Version 2" > "$WINEPREFIX/system.reg"
             touch "$WINEPREFIX/drive_c/windows/system32/explorer.exe" ;;
esac
exit 0
EOF
    chmod +x "$1/bin/wine"
    printf 'MZ fake builtin\n' > "$1/lib/wine/x86_64-windows/kernel32.dll"
    printf 'MZ fake builtin\n' > "$1/lib/wine/x86_64-windows/explorer.exe"
    printf 'MZ fake builtin\n' > "$1/lib/wine/i386-windows/kernel32.dll"
    printf 'MZ fake builtin\n' > "$1/lib/wine/i386-windows/ddraw.dll"
    echo "[Version]" > "$1/share/wine/wine.inf"
}
TKG="$HOME/.local/share/wsquashfs/wine/wine-tkg-fake"
mkwine "$TKG"
PROTON="$HOME/.local/share/Steam/compatibilitytools.d/GE-Proton9-1"
mkwine "${PROTON}/files"
printf '#!/bin/bash\nexit 0\n' > "${PROTON}/proton"
chmod +x "${PROTON}/proton"
echo "1700000000 GE-Proton9-1" > "${PROTON}/version"

cat > "${FAKEBIN}/wineserver" <<'EOF'
#!/bin/bash
echo "WINESERVER $*" >> "$FAKELOG"
exit 0
EOF
cat > "${FAKEBIN}/umu-run" <<'EOF'
#!/bin/bash
{
  echo "UMU args:$(printf ' [%s]' "$@")"
  echo "  PROTONPATH=${PROTONPATH:-} GAMEID=${GAMEID:-} WINEPREFIX=${WINEPREFIX:-} PROTON_DLL_COPY=${PROTON_DLL_COPY-unset} PROTON_USE_WINED3D=${PROTON_USE_WINED3D:-}"
} >> "$FAKELOG"
exit 0
EOF
# fuse-overlayfs qui échoue sur demande (FAIL_OVERLAY=1) : mode copy.
cat > "${FAKEBIN}/fuse-overlayfs" <<EOF
#!/bin/bash
[[ -n "\${FAIL_OVERLAY:-}" ]] && exit 1
exec $(command -v fuse-overlayfs) "\$@"
EOF
chmod +x "${FAKEBIN}"/*
export PATH="${FAKEBIN}:${PATH}"

# DXVK / VKD3D / d7vk factices
mkdir -p "${ROOT}/dxvk/x64" "${ROOT}/dxvk/x32" "${ROOT}/vkd3d/x64" "${ROOT}/vkd3d/x86" "${ROOT}/d7vk/x32"
for d in d3d9 d3d11 dxgi; do touch "${ROOT}/dxvk/x64/$d.dll" "${ROOT}/dxvk/x32/$d.dll"; done
touch "${ROOT}/vkd3d/x64/d3d12.dll" "${ROOT}/vkd3d/x86/d3d12.dll" "${ROOT}/d7vk/x32/ddraw.dll"
export WSQUASHFS_DXVK_DIR="${ROOT}/dxvk" WSQUASHFS_VKD3D_DIR="${ROOT}/vkd3d" \
       WSQUASHFS_D7VK_DLL="${ROOT}/d7vk/x32/ddraw.dll"

# ── Jeux de test ───────────────────────────────────────────────────────

ROMS="${ROOT}/userdata/roms/windows"
mkdir -p "$ROMS"

# Prefix Batocera packagé : DLL native, DLL Wine, lien DXVK cassé, profil
# lié à /userdata, regs/, SAVEDIR, autorun en fin de ligne Windows.
P="${ROOT}/src/Arcade Game.wine"
mkdir -p "$P/drive_c/windows/system32" "$P/drive_c/windows/syswow64" \
         "$P/drive_c/users/steamuser/Documents" "$P/drive_c/Game Dir/Bin" "$P/regs" \
         "$P/drive_c/Game Dir/Saves"
printf '#arch=win64\nWINE REGISTRY Version 2\n' > "$P/system.reg"
printf 'MZ native\n' > "$P/drive_c/windows/system32/d3dx9_42.dll"
printf 'Wine builtin DLL\n' > "$P/drive_c/windows/system32/kernel32.dll"
touch "$P/drive_c/windows/system32/explorer.exe"
ln -s /userdata/system/wine/dxvk/x64/dxgi.dll "$P/drive_c/windows/system32/dxgi.dll"
ln -s /userdata/system/Music "$P/drive_c/users/steamuser/Music"
printf 'MZ' > "$P/drive_c/Game Dir/Bin/Game.EXE"
printf 'MZ' > "$P/drive_c/Game Dir/Bin/Config.exe"
echo "save1" > "$P/drive_c/Game Dir/Saves/slot1"
printf 'REGEDIT4\n' > "$P/regs/a.reg"
printf 'DIR="drive_c/game dir/bin"\r\nCMD=game.exe -fullscreen -w 1\r\nGAME_VERSION=1.2.3\r\nESYNC=1\r\nENV=FOO=bar BAD-VAR=1\r\nSAVEDIR=drive_c\\Game Dir\\Saves\r\nLANG=fr_FR.UTF-8\r\n' > "$P/autorun.cmd"

# Jeu seul (sans prefix), exécutable avec espace, bureau virtuel
S="${ROOT}/src/Solo Game"
mkdir -p "$S/game"
printf 'MZ' > "$S/game/run me.exe"
printf 'CMD="game/run me.exe" --arg\nVIRTUAL_DESKTOP=1280x720\n' > "$S/autorun.cmd"

# Prefix win32 avec un .bat
W="${ROOT}/src/Old32.wine"
mkdir -p "$W/drive_c/windows/system32" "$W/drive_c/game"
printf '#arch=win32\n' > "$W/system.reg"
touch "$W/drive_c/windows/system32/explorer.exe"
printf '@echo off\n' > "$W/drive_c/game/start.bat"
printf 'DIR=drive_c/game\nCMD=start.bat\nDXVK=0\nHIDRAW=1\n' > "$W/autorun.cmd"

# Prefix créé par Proton (config_info)
X="${ROOT}/src/Proton Game.wine"
mkdir -p "$X/drive_c/windows/system32" "$X/drive_c/pg"
printf 'GE-Proton9-1\n/x/share/fonts/\n/x/lib/\n' > "$X/config_info"
printf '#arch=win64\n' > "$X/system.reg"
printf 'MZ' > "$X/drive_c/pg/pg.exe"
printf 'DIR=drive_c/pg\nCMD=pg.exe\nDXVK=0\n' > "$X/autorun.cmd"

# Faux sysfs hidraw : une DualSense Bluetooth dont le nœud est illisible
mkdir -p "${ROOT}/sysfs/hidraw99/device"
echo "HID_ID=0005:0000054C:00000CE6" > "${ROOT}/sysfs/hidraw99/device/uevent"

# ── Outils de scénario ─────────────────────────────────────────────────

# Rend la trace indépendante de la machine : chemins, tailles (du),
# horodatage des archives, environnement (Docker ou non), emplacement des
# outils système (--doctor).
normalize() {
    sed -e "s#${ROOT}#ROOT#g" -e "s#${LAUNCHER}#LAUNCHER#g" \
        -e 's/-[0-9]\{8\}-[0-9]\{6\}\(-[0-9]*\)\{0,1\}\.tar\./-DATE.tar./g' \
        -e 's#^\(  ✓ [a-z0-9-]*\) (/[^)]*)#\1 (PATH)#' \
        -e 's#^\(  ✓ dualsense-usb-bridge\) (.*)#\1 (PATH)#' \
        -e 's/^\(Environnement : \).*/\1ENV/' \
        -e 's/^\(Taille *: \).*/\1SIZE/' \
        -e 's/ (\([0-9][0-9.,]*[KMGT]\{0,1\}\))$/ (SIZE)/' \
        -e 's/gardé [0-9][0-9.,]*[KMGT]\{0,1\} /gardé SIZE /'
}

# step <titre> <commande...> : exécute, puis journalise sorties, code de
# retour et appels au faux Wine.
step() {
    local title="$1" rc
    shift
    : > "$FAKELOG"
    ( cd "$ROOT" && "$@" ) < /dev/null > "${ROOT}/stdout" 2> "${ROOT}/stderr"
    rc=$?
    {
        echo "################ $title"
        normalize < "${ROOT}/stdout"
        if [[ -s "${ROOT}/stderr" ]]; then
            echo "-- stderr"
            normalize < "${ROOT}/stderr"
        fi
        echo "== rc=$rc"
        normalize < "$FAKELOG"
    } >> "$OUT"
}

# snapshot <titre> : arborescence des sauvegardes et du cache, liens compris.
snapshot() {
    {
        echo "################ TREE $1"
        ( cd "$ROOT" && find saves cache userdata src -printf '%y %p -> %l\n' 2>/dev/null \
            | grep -v '/work/\|/\.work/' | sort ) | normalize
    } >> "$OUT"
}

# note <texte> : ligne libre dans la trace (état observé).
note() {
    echo "## $*" >> "$OUT"
}

# unit <titre> <code bash> : exécute du code avec le lanceur chargé par
# « source » (fonctions seules, main n'est pas appelé).
unit() {
    local title="$1"
    shift
    {
        echo "################ unit $title"
        bash -c "source \"\$1\"; $*" _ "$LAUNCHER" 2>&1 | normalize
    } >> "$OUT"
}

# Archives de sauvegarde d'un jeu, et contenu (trié) de la dernière.
backups_of() {
    local dir="$HOME/.local/share/wsquashfs/backups" last
    {
        echo "## archives de $1 : $(compgen -G "${dir}/${1}-*.tar.zst" | wc -l)"
        # La plus récente : le suffixe -N dépend de l'heure (même seconde).
        last=$(find "$dir" -maxdepth 1 -name "${1}-*.tar.zst" -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2-)
        [[ -n "$last" ]] && tar -tf "$last" | sort | sed 's/^/##   /'
    } >> "$OUT"
}

mounted() {
    if mountpoint -q "$1"; then echo "monté"; else echo "non monté"; fi
}

lock_state() {
    if flock -n "${ROOT}/cache/locks/${1}.lock" true; then echo "libre"; else echo "tenu"; fi
}

# background <commande...> : processus de fond, arrêté par stop_background.
background() {
    "$@" &
    BG_PIDS+=($!)
    sleep 0.3
}

stop_background() {
    local pid
    for pid in "${BG_PIDS[@]}"; do kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; done
    BG_PIDS=()
}

# ── Scénarios ──────────────────────────────────────────────────────────

L="$LAUNCHER"
: > "$OUT"

# Fonctions isolées
# shellcheck disable=SC2016  # code exécuté par unit, développé là-bas
unit runner_name 'for b in "" /usr/local/bin/proton /usr/bin/wine64 /x/wine-tkg/bin/wine /x/GE-Proton9-1/files/bin/wine; do echo "[$b] → $(runner_name "$b")"; done'
# shellcheck disable=SC2016
unit split_command_line 'for l in "-a -b" "-path \"C:\\Mon Jeu\\x\" -n" "\"\" fin" "  x   y  " "a\"b c\"d"; do printf "%s →" "$l"; split_command_line "$l" | while IFS= read -r -d "" a; do printf " [%s]" "$a"; done; echo; done'
# shellcheck disable=SC2016
unit backup_suffix 'date() { echo fige; }; SAVES_DIR="$HOME/unit-saves"; WSQUASHFS_BACKUP_DIR="$HOME/unit-backups"; mkdir -p "$SAVES_DIR/g"; backup_saves g; backup_saves g; backup_saves absent && echo "absent : rien à archiver"; ls "$WSQUASHFS_BACKUP_DIR"; rm -rf "$SAVES_DIR" "$WSQUASHFS_BACKUP_DIR"'

# Ligne de commande
step help          "$L" --help
step doctor        env HIDRAW_SYSFS="$ROOT/sysfs" "$L" --doctor
step list-empty    "$L" --list
step backup-empty  "$L" --backup
step noarg         "$L"
step badopt        "$L" --nope
step badext        "$L" "$ROOT/src/Solo Game/autorun.cmd"
step two-games     "$L" a.wsquashfs b.wsquashfs
step wine-noarg    "$L" --wine
step exec-usage    "$L" --exec "$ROMS/x.wsquashfs"

# --pack / --info
step pack1         "$L" --pack "$P" "$ROMS/Arcade Game.wsquashfs"
step pack-exist    "$L" --pack "$P" "$ROMS/Arcade Game.wsquashfs"
step pack2         "$L" --pack "$S" "$ROMS/Solo Game.wsquashfs"
step pack3         "$L" --pack "$W"
mv "${ROOT}/src/Old32.wsquashfs" "$ROMS/"
step pack4         "$L" --pack "$X" "$ROMS/Proton Game.wsquashfs"
step pack-usage    "$L" --pack
step info1         "$L" --info "$ROMS/Arcade Game.wsquashfs"
step info2         "$L" --info "$ROMS/Solo Game.wsquashfs"
step info3         "$L" --info "$X"
step info-usage    "$L" --info

# Lancement d'un prefix Batocera (overlay)
step run-arcade    env WSQUASHFS_UMU=0 "$L" "$ROMS/Arcade Game.wsquashfs"
snapshot after-arcade
note "verrou Arcade Game après la partie : $(lock_state "Arcade Game")"
step run-arcade2   env WSQUASHFS_UMU=0 "$L" "$ROMS/Arcade Game.wsquashfs"
printf 'CMD=Config.exe\nDXVK=\nGAME_VERSION="9.9"\n' > "$ROMS/Arcade Game.wsquashfs.autorun"
step run-override  env WSQUASHFS_UMU=0 "$L" "$ROMS/Arcade Game.wsquashfs"
step info-override "$L" --info "$ROMS/Arcade Game.wsquashfs"
rm "$ROMS/Arcade Game.wsquashfs.autorun"
step exec-tool     env WSQUASHFS_UMU=0 "$L" --exec "$ROMS/Arcade Game.wsquashfs" winecfg
step exec-rel      env WSQUASHFS_UMU=0 "$L" --exec "$ROMS/Arcade Game.wsquashfs" config.exe /x
step wine-missing  env WSQUASHFS_UMU=0 "$L" --wine nonexistent "$ROMS/Arcade Game.wsquashfs"
step wine-proton   env WSQUASHFS_UMU=0 "$L" --wine GE-Proton9 "$ROMS/Arcade Game.wsquashfs"

# Jeu seul, prefix Proton, umu
step solo-noumu    env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
snapshot after-solo
step solo-again    env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
step proton-umu    "$L" "$ROMS/Proton Game.wsquashfs"
step proton-nodxvk env WSQUASHFS_DXVK_DIR=/nope "$L" "$ROMS/Proton Game.wsquashfs"
step win32-hidraw  env WSQUASHFS_UMU=0 HIDRAW_SYSFS="$ROOT/sysfs" "$L" "$ROMS/Old32.wsquashfs"

# --mount / --umount : les démons FUSE ne doivent pas garder le verrou
step mount         "$L" --mount "$ROMS/Solo Game.wsquashfs"
note "verrou Solo Game pendant --mount : $(lock_state "Solo Game")"
step list-mounted  "$L" --list
step backup-busy   "$L" --backup "Solo Game"
step mount-run     env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
step umount        "$L" --umount "$ROMS/Solo Game.wsquashfs"

# Jeu installé (--install)
step install       "$L" --install "$ROMS/Arcade Game.wsquashfs" "$ROOT/installed"
step install-exist "$L" --install "$ROMS/Arcade Game.wsquashfs" "$ROOT/installed"
step run-dir       env WSQUASHFS_UMU=0 "$L" "$ROOT/installed/Arcade Game"
step mount-dir     "$L" --mount "$ROOT/installed/Arcade Game"

# --backup : attributs étendus (DOS de Wine, dossier opaque) conservés
setfattr -n user.DOSATTRIB -v archive-ok "$ROOT/saves/Arcade Game/system.reg"
step backup-one    "$L" --backup "Arcade Game.wsquashfs"
backups_of "Arcade Game"
mkdir -p "$ROOT/restore"
tar -C "$ROOT/restore" --xattrs --xattrs-include='user.*' -xpf \
    "$(compgen -G "$HOME/.local/share/wsquashfs/backups/Arcade Game-*.tar.zst" | head -1)"
note "attribut après restauration : $(getfattr --only-values -n user.DOSATTRIB "$ROOT/restore/Arcade Game/system.reg" 2>/dev/null || echo absent)"
rm -rf "$ROOT/restore"
step backup-unknown "$L" --backup "Unknown"

# --clean / --clear
snapshot before-clean
step clean         "$L" --clean
snapshot after-clean
step clear-one     "$L" --clear "Solo Game"
step clear-none    "$L" --clear "Unknown"
step clear-all     "$L" --clear
snapshot after-clear
backups_of "Solo Game"
step list-after-clear "$L" --list
step umount-all    "$L" --umount

# Mode copy (overlay en échec), puis cache valide
step copy1         env FAIL_OVERLAY=1 WSQUASHFS_UMU=0 "$L" "$ROMS/Arcade Game.wsquashfs"
snapshot after-copy
step copy2         env FAIL_OVERLAY=1 WSQUASHFS_UMU=0 "$L" "$ROMS/Arcade Game.wsquashfs"

# Jeu en cours, reconnu par son WINEPREFIX : chemin par défaut
# (…/wsquashfs/wine) et chemin du CACHE_DIR personnalisé.
background env "WINEPREFIX=/x/wsquashfs/wine/Old32" sleep 30
step in-use-default env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
stop_background
step mount-old32   "$L" --mount "$ROMS/Old32.wsquashfs"
background env "WINEPREFIX=${ROOT}/cache/wine/Old32/pfx" sleep 30
step in-use-cache  env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
step list-running  "$L" --list
step other-game    env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
note "Old32 (en cours) après le lancement d'un autre jeu : $(mounted "${ROOT}/cache/wine/Old32")"
step clean-running "$L" --clean
note "Old32 (en cours) après --clean : $(mounted "${ROOT}/cache/wine/Old32")"
stop_background

# Verrou tenu par un autre lancement (Wine pas encore démarré)
background flock -o "${ROOT}/cache/locks/Old32.lock" sleep 30
step locked        env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
step list-locked   "$L" --list
step locked-other  env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
note "Old32 (verrouillé) après le lancement d'un autre jeu : $(mounted "${ROOT}/cache/wine/Old32")"
step clean-locked  "$L" --clean
note "Old32 (verrouillé) après --clean : $(mounted "${ROOT}/cache/wine/Old32")"
stop_background
step unlocked      env WSQUASHFS_UMU=0 "$L" "$ROMS/Solo Game.wsquashfs"
note "Old32 (libéré) après le lancement d'un autre jeu : $(mounted "${ROOT}/cache/wine/Old32")"

# Runner imposé par l'autorun (fichier de surcharge)
O="$ROMS/Old32.wsquashfs.autorun"
printf 'RUNNER=/nope/wine\n' > "$O"
step runner-bad    env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'RUNNER=%s\n' "$TKG/bin/wine" > "$O"
step runner-ok     env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'WINE=foo\n' > "$O"
step wine-foo      env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'WINE=tkg-fake\n' > "$O"
step wine-tkg      env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'WINE=system\n' > "$O"
step wine-system   env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'PROTON=GE-Proton9-1\n' > "$O"
step proton-on-wine "$L" "$ROMS/Old32.wsquashfs"
printf 'CMD=\n' > "$O"
step no-cmd        env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'CMD=start.bat -path "C:\\Mon Jeu\\x" -n "" fin\n' > "$O"
step cmd-quotes    env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'CMD=missing.exe\n' > "$O"
step no-exe        env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
printf 'SAVEDIR=\nSAVEFILES=drive_c\\game\\a.sav;drive_c/game/b.sav\nHIDRAW=bt\nFSYNC=1\nVKD3D=0\nD7VK=0\n' > "$O"
step savefiles     env WSQUASHFS_UMU=0 WSQUASHFS_HIDRAW=0 "$L" "$ROMS/Old32.wsquashfs"
snapshot after-savefiles
rm -f "$O"
step proton-option "$L" --wine GE-Proton9-1 "$ROMS/Solo Game.wsquashfs"
step solo-umu      "$L" "$ROMS/Solo Game.wsquashfs"

# Prefix win32 avec un Wine WoW64 seul (sans i386-unix)
mv "$TKG/lib/wine/i386-unix" "${ROOT}/i386-unix.bak"
rm -rf "$ROOT/saves/Old32"
step win32-nowine  env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
rm -rf "$ROOT/saves/Old32"
step win32-option  env WSQUASHFS_UMU=0 "$L" --wine tkg-fake "$ROMS/Old32.wsquashfs"
# … et un Wine classique installé à côté : il lance le jeu ET reg add
mkwine "$HOME/.local/share/wsquashfs/wine/wine-classic"
rm -rf "$ROOT/saves/Old32"
step win32-swap    env WSQUASHFS_UMU=0 "$L" "$ROMS/Old32.wsquashfs"
rm -rf "$HOME/.local/share/wsquashfs/wine/wine-classic"
mv "${ROOT}/i386-unix.bak" "$TKG/lib/wine/i386-unix"

# --clean avec un vrai doublon (copie identique d'un fichier de l'image)
mkdir -p "$ROOT/saves/Arcade Game/drive_c/Game Dir/Bin"
cp "$P/drive_c/Game Dir/Bin/Config.exe" "$ROOT/saves/Arcade Game/drive_c/Game Dir/Bin/Config.exe"
echo modified > "$ROOT/saves/Arcade Game/drive_c/Game Dir/Bin/Game.EXE"
step clean-dup     "$L" --clean
snapshot after-clean-dup
step umount-final  "$L" --umount
note "montages restants : $(mount | grep -cF "$ROOT")"

# ── Résultat ───────────────────────────────────────────────────────────

if [[ "$UPDATE" == true ]]; then
    cp "$OUT" "$EXPECTED"
    echo "✓ ${EXPECTED} mis à jour ($(grep -c '^################ ' "$EXPECTED") scénarios)"
    exit 0
fi
if [[ ! -f "$EXPECTED" ]]; then
    echo "Erreur : ${EXPECTED} absent (tests/run-tests.sh --update pour le créer)" >&2
    exit 1
fi
if diff -u --label attendu --label obtenu "$EXPECTED" "$OUT"; then
    echo "✓ $(grep -c '^################ ' "$OUT") scénarios conformes"
    exit 0
fi
echo "✗ La trace diffère de tests/expected.out (voir ci-dessus)" >&2
exit 1
