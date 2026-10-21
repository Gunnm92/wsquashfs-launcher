# WSquashFS Launcher

Launcher standalone pour fichiers `.wsquashfs` de Batocera sur n'importe quel Linux — machine physique ou conteneur Docker.

Compatible avec Pegasus Frontend, EmulationStation et autres launchers.

## Description

Ce projet permet de monter et exécuter des fichiers `.wsquashfs` (format Batocera pour les jeux Windows) sur n'importe quelle distribution Linux, avec ou sans FUSE disponible.

Le script détecte automatiquement les capacités de l'environnement et choisit le mode de montage optimal :

| Mode | Conditions | Sauvegardes |
|---|---|---|
| **overlay** | overlay du noyau dans un namespace utilisateur si l'environnement le permet, sinon squashfuse + fuse-overlayfs | Isolées dans `SAVES_DIR` (upperdir) |
| **copy** | squashfuse ou unsquashfs | Intégrées dans le cache de travail |

En mode **copy**, le résultat de l'extraction est mis en cache — les relances suivantes sont instantanées tant que le fichier `.wsquashfs` n'a pas changé.

## Fonctionnalités

- Détection automatique Docker / machine physique
- Sélection automatique du mode de montage (overlay → copy)
- Fallback `unsquashfs` si FUSE est bloqué (conteneurs sans `CAP_SYS_ADMIN`)
- Cache d'extraction persistant (relance rapide)
- Support complet du format `autorun.cmd` de Batocera
- Configuration Wine automatique (DXVK, VKD3D, ESYNC, FSYNC, LANG, ENV)
- Support versions Wine multiples, Proton, runner personnalisé
- Import automatique des fichiers `.reg`
- Nettoyage automatique des montages (trap EXIT/INT/TERM)
- Aucun `eval` — exécution sécurisée par tableaux bash

## Installation

### Installation rapide (curl)

```bash
# Sans sudo (recommandé)
curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/wsquashfs-launcher \
  -o ~/.local/bin/wsquashfs-launcher && chmod +x ~/.local/bin/wsquashfs-launcher
```

```bash
# Avec sudo (système)
curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/wsquashfs-launcher \
  | sudo tee /usr/local/bin/wsquashfs-launcher > /dev/null \
  && sudo chmod +x /usr/local/bin/wsquashfs-launcher
```

```bash
# Via install.sh (vérifie les dépendances, configure PATH, associe .wsquashfs)
curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash
```

`install.sh` détecte la distribution et installe les paquets système :

- **Debian/Ubuntu** (apt) : `wine32:i386`, `libxkbcommon0:i386`, `squashfuse`,
  `squashfs-tools`, `xz-utils`, `fuse-overlayfs`, `attr`, `zstd`, `unzip` et,
  si disponibles, `mangohud` / `mangohud:i386` (facultatifs) ;
- **Arch, CachyOS, Manjaro, EndeavourOS** (pacman) : les mêmes outils, les
  bibliothèques 32 bits de wine-tkg (dépôt `[multilib]`, à activer s'il ne
  l'est pas) avec la version 32 bits des pilotes Vulkan installés (NVIDIA,
  AMD, Intel), et `mangohud` / `lib32-mangohud`. Seuls les paquets manquants
  sont installés ; un paquet absent des dépôts est signalé et ignoré.

Puis il propose, quelle que soit la distribution :

- **wine-tkg / wine-ge** (jeux arcade Batocera) dans `~/.local/share/wsquashfs/wine`
- **umu-run** (paquet `umu-launcher` sous Arch/CachyOS, sinon zipapp avec un
  lien dans le dossier d'installation) et un **Proton** : `proton-cachyos-slr`
  ou `proton-cachyos` sous CachyOS, sinon **UMU-Proton** (Proton de Valve,
  empreinte SHA-512 vérifiée) dans `~/.local/share/Steam/compatibilitytools.d`
  — nécessaires aux jeux PC récents (images sans prefix, prefixes créés par
  Proton), inutiles pour l'arcade seule ;
- **DXVK, VKD3D-Proton et d7vk** (dernières versions GitHub) dans
  `~/.local/share/wsquashfs/{dxvk,vkd3d-proton,d7vk}` : rendu Vulkan des jeux
  lancés par Wine.

Il termine par `wsquashfs-launcher --doctor`. Options : `--dry-run` (montre ce
qui serait fait sans rien modifier), `--yes` (sans questions), `--uninstall`.
Les questions sont posées au terminal, y compris avec `curl … | bash`.

### Prérequis

```bash
# Debian/Ubuntu — machine physique (overlay complet)
sudo apt install squashfuse fuse-overlayfs wine

# Debian/Ubuntu — Docker sans FUSE (fallback unsquashfs)
sudo apt install squashfs-tools wine

# Arch Linux
sudo pacman -S squashfuse wine
# jeux PC récents (images sans prefix, prefixes Proton) : umu + un Proton
sudo pacman -S umu-launcher        # + proton-cachyos-slr (CachyOS), sinon UMU-Proton ou GE-Proton
# wine-tkg (Kron4ek, non WoW64) : jeux 32 bits
sudo pacman -S lib32-libxkbcommon
# MangoHud (facultatif : mangohud wsquashfs-launcher jeu.wsquashfs)
sudo pacman -S mangohud lib32-mangohud
```

### Installation du script

```bash
git clone https://github.com/Gunnm92/wsquashfs-launcher.git
cd wsquashfs-launcher
sudo cp wsquashfs-launcher /usr/local/bin/
# ou sans sudo
cp wsquashfs-launcher ~/.local/bin/
```

#### Avec Make

```bash
make install      # installe dans /usr/local/bin
make uninstall
```

### Vérifier l'installation

```bash
wsquashfs-launcher --help
./test-setup.sh
```

## Utilisation

Aucun privilège requis : l'image est montée en utilisateur (overlay du
noyau dans un namespace utilisateur, ou squashfuse + fuse-overlayfs comme une
AppImage), y compris les paquets
Batocera dont les fichiers appartiennent à root. Les sauvegardes et le
prefix appartiennent à l'utilisateur qui lance le jeu. `sudo` reste pris en
charge (ancien usage de certains frontends), mais n'apporte rien.

```bash
# Lancer un jeu (commande de l'autorun.cmd)
wsquashfs-launcher /path/to/game.wsquashfs

# Lancer un autre programme dans le prefix du jeu : exécutable du jeu
# (chemin relatif au prefix ou au DIR de l'autorun.cmd, casse ignorée)
# ou outil Wine (winecfg, regedit, cmd, explorer, control joy.cpl...)
wsquashfs-launcher --exec game.wsquashfs JConfig.exe
wsquashfs-launcher --exec game.wsquashfs drive_c/teknoparrot/TeknoParrotUi.exe
wsquashfs-launcher --exec game.wsquashfs winecfg

# Forcer un runner Wine/Proton (nom ou partie du nom d'un runner installé
# dans /opt, /usr/wine, ~/.local/share/wsquashfs/wine ou un dossier Proton
# de Heroic/Steam, chemin d'un binaire wine, ou "system") — combinable
# avec --exec. Un Proton sur un jeu à prefix Proton ou sans prefix est lancé
# via umu, comme PROTON= dans l'autorun.cmd ; sur un prefix Wine (Batocera),
# son Wine est lancé directement.
wsquashfs-launcher --wine GE-Proton9 game.wsquashfs

# Outils extérieurs (MangoHud, gamemode...) : placés devant la commande,
# leurs variables d'environnement sont transmises au jeu (32 bits :
# lib32-mangohud)
mangohud wsquashfs-launcher game.wsquashfs

# Monter le jeu et le laisser monté pour le modifier à la main (les
# modifications vont dans le dossier de sauvegardes, jamais dans le paquet)
wsquashfs-launcher --mount game.wsquashfs
wsquashfs-launcher --umount game.wsquashfs     # ou --umount seul : tous les jeux

# Installer un jeu (l'inverse de --pack) : extrait l'image dans un dossier,
# pour jouer sans FUSE. Défaut : à côté de l'image (MonJeu.wsquashfs →
# MonJeu/). L'image est conservée ; ses sauvegardes ne sont pas reprises ;
# un dossier existant n'est jamais écrasé.
wsquashfs-launcher --install MonJeu.wsquashfs [dossier]
# Lancer le jeu installé, sur place (même choix de Wine/Proton, sans
# montage : le jeu écrit directement dans son dossier) — hors
# EmulationStation, qui n'affiche que les .wsquashfs
wsquashfs-launcher /chemin/MonJeu

# Version du jeu (GAME_VERSION= de l'autorun.cmd), type de prefix, commande
# et taille — lus dans l'image sans la monter (aussi sur un dossier installé)
wsquashfs-launcher --info MonJeu.wsquashfs

# Créer un .wsquashfs (zstd) à partir d'un prefix Wine
wsquashfs-launcher --pack MonJeu.wine          # → MonJeu.wsquashfs
# Image existante : confirmation au terminal (et proposition de réinitialiser
# ses sauvegardes), refus sans terminal. L'ancienne image n'est remplacée
# qu'une fois la nouvelle construite.

# Supprimer les copies de travail et les doublons des sauvegardes : fichiers
# du jeu recopiés sans modification (jeu qui ouvre ses données en écriture :
# 2,7 Go pour Streets of Rage 4), DLL que Proton recopie à chaque lancement.
# Tout ce que le jeu a écrit est gardé ; jeux en cours exceptés.
wsquashfs-launcher --clean

# Radical : ne garder que drive_c/users (sauvegardes des jeux PC récents) de
# tous les jeux, ou d'un seul. Prefix recréé au lancement suivant ; scores et
# réglages écrits ailleurs (jeux arcade, dossier du jeu, registre) perdus.
# Chaque jeu est archivé (--backup) avant ; sans archive, il n'est pas touché.
wsquashfs-launcher --clear ["Nom du jeu"]

# Archiver les sauvegardes (tous les jeux, ou un seul) dans
# ~/.local/share/wsquashfs/backups/<jeu>-AAAAMMJJ-HHMMSS.tar.zst ; jeux en
# cours exceptés. Restauration, jeu arrêté :
#   tar -C ~/.local/share/wsquashfs/saves --xattrs --xattrs-include='user.*' -xpf <archive>
wsquashfs-launcher --backup ["Nom du jeu"]

# Jeux connus : état (en cours, verrouillé, monté), version, taille des
# sauvegardes et image d'origine
wsquashfs-launcher --list

# Vérifier l'installation : FUSE, Wine/Proton et runners, umu, DXVK, VKD3D,
# d7vk, manettes (hidraw, dualsense-usb-bridge), dossiers. Code 1 si aucun
# jeu ne peut être lancé.
wsquashfs-launcher --doctor

# Afficher l'aide
wsquashfs-launcher --help
```

### Variables d'environnement

```bash
export WSQUASHFS_SAVES_DIR="$HOME/mes-sauvegardes"   # sauvegardes overlay
export WSQUASHFS_CACHE="$HOME/.cache/mes-jeux"        # cache de travail
export WSQUASHFS_BACKUP_DIR="$HOME/archives-jeux"     # archives --backup / --clear
export WSQUASHFS_DXVK=0        # désactive DXVK (activé par défaut)
export WSQUASHFS_HIDRAW=1      # manettes Sony par hidraw ("DisableHidraw"=0)
                               # si lisibles ; défaut XInput (Xbox), comme
                               # Batocera — jeux gérant la DualSense seulement.
                               # DualSense Bluetooth présentée en USB
                               # (dualsense-usb-bridge) ; =bt : en direct
export WSQUASHFS_D7VK=0        # désactive d7vk (DirectDraw → Vulkan, activé par
                               # défaut si /usr/share/d7vk/x32/ddraw.dll existe)
export WSQUASHFS_BATOCERA_SAVES=/chemin/saves/<système>   # dossier des
                               # sauvegardes SAVEDIR/SAVEFILES (défaut : saves/
                               # à côté de roms/, comme Batocera)

wsquashfs-launcher game.wsquashfs
```

### Intégration Pegasus Frontend

```
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-launcher "{file.path}"

game: My Game
file: MyGame.wsquashfs
```

### Intégration EmulationStation

```xml
<system>
  <name>wsquashfs</name>
  <fullname>Windows (WSquashFS)</fullname>
  <path>~/roms/wsquashfs</path>
  <extension>.wsquashfs</extension>
  <command>wsquashfs-launcher %ROM%</command>
  <platform>pc</platform>
  <theme>windows</theme>
</system>
```

## Format autorun.cmd

Le fichier `autorun.cmd` à la racine du `.wsquashfs` configure l'exécution. Utilise le format Batocera (paires `CLE=valeur`, fins de ligne DOS tolérées).

### Variables de base

| Variable | Obligatoire | Description |
|---|---|---|
| `CMD` | ✅ | Exécutable et arguments (`game.exe -fullscreen` ou `"My Game.exe" -fullscreen`) |
| `DIR` | — | Chemin vers le dossier de l'exécutable (relatif à la racine du wsquashfs) |
| `GAME_VERSION` | — | Version du jeu packagé (`1.04`, `v2.1.0 GOG`…), affichée au lancement, par `--info`, `--pack` et `--install`. Ignorée par Batocera, qui ne lit que les clés qu'il connaît : l'image reste compatible |
| `PRE` | — | Commande lancée une fois **avant** le jeu, dans son dossier (DIR), avec l'utilisateur et l'environnement du jeu (`WINEPREFIX`…). Un échec arrête le lancement. Sert à préparer des fichiers que l'image ne peut pas contenir tels quels. Ligne de commande comme `CMD` : ses guillemets sont gardés | `sh pre.sh` |
| `ATTACH` | — | Commande lancée **pendant** le jeu, une fois son processus trouvé dans le prefix du jeu (sans délai maximal) ; `GAME_PID` contient son PID. Elle accompagne le jeu jusqu'à la fin de tous ses processus Wine (lanceur intermédiaire compris), a ensuite 5 s pour finir seule, puis est arrêtée avec tout ce qu'elle a lancé | `sh attach.sh` |
| `ATTACH_PROCESS` | — | Nom du processus attendu par `ATTACH` (15 caractères au plus, comme le noyau). Défaut : l'exécutable de `CMD` ; à renseigner quand `CMD` est un lanceur qui démarre le vrai jeu | `DOA5A.exe` |

`PRE` et `ATTACH` exécutent des commandes Linux sur la machine, hors de Wine :
n'utiliser que des images de confiance. Ni l'une ni l'autre n'est lancée par
`--exec`. Batocera les ignore, comme `GAME_VERSION` : un jeu qui en dépend n'y
fonctionnera pas tel quel.

### Variables Wine

| Variable | Description | Exemples |
|---|---|---|
| `WINE` | Version Wine dans `/opt/wine-<VERSION>/bin/wine`, ou `system` pour le Wine du système | `9.0`, `tkg`, `system` |
| `PROTON` | Runner Proton de Heroic/Steam (nom du dossier ou version), à défaut proton-cachyos, sinon l'UMU-Proton le plus récent | `proton-cachyos-slr`, `UMU-Proton-10.0-4`, `GE-Proton8-25` |
| `RUNNER` | Chemin absolu vers un runner personnalisé | `/opt/wine-custom/bin/wine` |
| `ARCH` | Architecture Wine (défaut : `win64`) | `win32`, `win64` |
| `LANG` | Langue (`LC_ALL`) | `fr_FR.UTF-8` |
| `ENV` | Variables d'environnement additionnelles | `VAR1=val1 VAR2=val2` |
| `HIDRAW` | `1` : manettes Sony par hidraw pour ce jeu s'il gère la DualSense **lui-même**, sans Steam Input (repli XInput si illisible) ; défaut XInput. Une DualSense Bluetooth (celle de Sunshine) est présentée au jeu en DualSense USB par [dualsense-usb-bridge](tools/dualsense-usb-bridge/README.md) : les `libScePad` de 2022 (F1 22) refusent la Bluetooth. `bt` : DualSense Bluetooth en direct, pour les jeux dont les haptiques passent en Bluetooth (Until Dawn) — en USB, elles partent en audio, non transmis. Sous GE-Proton, le mode XInput passe aussi par hidraw : GE confie toujours les manettes Sony à hidraw et les convertit en XInput (`PROTON_SONY_HIDRAW_XINPUT=1`, posé par le lanceur) | `1` |

### Choix du runner Wine

Sans `WINE`, `PROTON`, `RUNNER` ni `--wine` :

- **Prefix créé par Proton** (Heroic, Steam : fichier `config_info` à la racine) :
  la même version de Proton, cherchée dans `~/.config/heroic/tools/proton`,
  `~/.local/share/Steam/compatibilitytools.d`, `~/.steam/root/compatibilitytools.d`
  et `/usr/share/steam/compatibilitytools.d`. Si elle est absente : proton-cachyos,
  puis l'UMU-Proton le plus récent, puis GE-Proton, puis n'importe quel Proton installé.
  Le jeu est lancé **via umu** (`umu-run` du paquet umu-launcher, à défaut celui
  de Heroic) avec le script `proton`, comme Heroic : Proton gère alors le prefix,
  DXVK, VKD3D et NVAPI. Sans umu, le Wine du Proton est lancé directement (avec
  un avertissement : certains jeux ne s'affichent pas). `WSQUASHFS_UMU=0` pour
  forcer ce mode. Les DLL que Proton copie d'ordinaire dans chaque prefix
  (d3dx9, msvcp, xaudio… ~190 Mo par jeu) y sont des liens vers Proton
  (`PROTON_DLL_COPY` vide) ; `ENV=PROTON_DLL_COPY=<motifs>` dans l'autorun.cmd
  pour un jeu qui doit les remplacer.
- **Image sans prefix** (jeu PC emballé seul) : prefix créé par Proton via umu
  (proton-cachyos, sinon l'UMU-Proton le plus récent, sinon GE-Proton).
  proton-cachyos et UMU-Proton d'abord : leur SDL gère la DualSense en manette
  XInput, vibration comprise ; GE-Proton la force en hidraw, sans vibration en
  mode XInput. Le lanceur ramène la pile à 8 Mo si elle est illimitée (cas de
  Docker sur Unraid) : sans quoi proton-cachyos ne démarre pas, et les jeux
  32 bits sous wine-tkg manquent de mémoire. `WINE=tkg` dans l'autorun.cmd
  pour garder wine-tkg. Un prefix Wine déjà présent dans les sauvegardes est
  gardé sous Wine (Proton ne sait pas le convertir) : supprimer le dossier de
  sauvegardes du jeu pour passer sous Proton.
- **Autres prefixes** (jeux arcade Batocera) : wine-tkg, puis wine-ge/lutris, puis
  wine-proton, puis le Wine du système — les chemins Batocera (`/usr/wine/...`)
  sont pris en charge. Certains jeux 32 bits échouent sous wine-tkg (non WoW64)
  mais passent avec le Wine WoW64 du système : `WINE=system` dans leur
  autorun.cmd (GTI Club – Supermini Festa). Batocera ignore cette valeur.

### Sauvegardes (comme Batocera)

| Variable | Description | Exemples |
|---|---|---|
| `SAVEDIR` | Dossier de sauvegarde du jeu, relié à `saves/<système>/<jeu>` (à côté de `roms/`). Les sauvegardes packagées y sont copiées sans jamais écraser l'existant | `drive_c/game/saves` |
| `SAVEFILES` | Fichiers de sauvegarde (séparés par `;`, relatifs au prefix), reliés au même dossier | `drive_c/game/save.dat;drive_c/game/cfg.ini` |

### Variables d'optimisation

| Variable | Description | Valeurs |
|---|---|---|
| `DXVK` | DirectX 8-11 → Vulkan (activé par défaut) | `0` / `1` |
| `VKD3D` | DirectX 12 → Vulkan (activé par défaut) | `0` / `1` |
| `D7VK` | DirectDraw / DirectX 7 → Vulkan via d7vk (activé par défaut) | `0` / `1` |
| `VIRTUAL_DESKTOP` | Bureau virtuel Wine | `1920x1080` |
| `ESYNC` | Event synchronization | `0` / `1` |
| `FSYNC` | Futex synchronization | `0` / `1` |

### Surcharge sans reconstruction (`<jeu>.wsquashfs.autorun`)

Un fichier `<jeu>.wsquashfs.autorun` posé **à côté de l'image** (même format
que l'`autorun.cmd`) permet d'essayer un réglage tout de suite, sans
reconstruire l'image : une clé qui y figure l'emporte sur celle de l'image,
`CLÉ=` vide la ramène au défaut du lanceur, les autres clés viennent de
l'image. Le lancement et `--info` l'annoncent (« Surcharge : … »).

```
# Le Jeu.wsquashfs.autorun
GAME_VERSION=1.2.0
HIDRAW=1
```

Batocera ne lit pas ce fichier : une fois le réglage validé, l'inscrire dans
l'image (wsquashfs-manager le fait en une action) et supprimer la surcharge.

### Exemples

```cmd
# Jeu simple
DIR=Game
CMD=game.exe -fullscreen
```

```cmd
# Jeu 3D avec optimisations
DIR=Game
CMD=game.exe
WINE=9.0
ARCH=win64
DXVK=1
ESYNC=1
```

```cmd
# Jeu DirectX 12
DIR=bin
CMD=launcher.exe
VKD3D=1
FSYNC=1
```

```cmd
# Chemin avec espaces
DIR=drive_c/Program Files/My Game
CMD="My Game.exe" --lang fr
DXVK=1
```

Voir [autorun.cmd.example](autorun.cmd.example) pour plus d'exemples.

## Architecture des données

```
$HOME/
├── .local/share/wsquashfs/
│   └── saves/
│       ├── game1/          # Mode overlay : upperdir (diff uniquement)
│       ├── game2/
│       └── .work/          # Workdir overlay, même disque que les sauvegardes
└── .cache/wsquashfs/
    ├── mnt/                # Montage squashfuse temporaire (mode overlay)
    ├── wine/               # Overlay ou copie de travail persistante (mode copy)
    │   └── game1/
    │       └── .wsquashfs_cache   # Empreinte source pour détection de changement
    └── ns/                 # Échange avec le lanceur en namespace, le temps d'une partie
```

## Fonctionnement

### Mode overlay noyau (par défaut quand l'environnement le permet)

Depuis Linux 5.11, un utilisateur peut monter l'overlay du noyau dans son
propre namespace utilisateur, sans privilège. Le lanceur se relance dans un
tel namespace (`unshare --map-current-user --mount --keep-caps`), y monte
l'image avec `squashfuse_ll -o uid=,gid=` (tous les fichiers de l'image
appartiennent à l'utilisateur, qu'ils soient à root comme dans les paquets
Batocera ou à un autre UID), puis l'overlay du noyau par-dessus. Wine est
lancé sans les capacités du namespace.

- Une seule couche FUSE au lieu de deux. Lectures aléatoires en parallèle
  (chargement d'un niveau) environ 5 fois plus rapides, mesuré sur Tempest
  Rising. En revanche, la lecture séquentielle pure est plus lente
  (1,5 Go/s contre 3,7 Go/s).
- Les sauvegardes s'écrivent à la vitesse du disque.
- Les montages sont invisibles hors du namespace et disparaissent avec lui :
  rien d'orphelin après un plantage.
- `--umount` arrête la partie (SIGTERM au lanceur du jeu), `--list` la voit
  « en cours ».

Ce mode est essayé à chaque lancement. Tout échec retombe sur fuse-overlayfs :
namespaces refusés, squashfuse antérieur à 0.5 (Debian 12), sauvegardes sur
un système de fichiers sans attributs `user.*` ou sur un overlay, etc.
`--doctor` indique s'il est disponible et pourquoi. `WSQUASHFS_OVERLAY=fuse`
impose fuse-overlayfs, `WSQUASHFS_OVERLAY=kernel` interdit le repli.
`--mount` utilise toujours fuse-overlayfs, parce que le montage doit survivre
au lanceur.

Les deux overlays partagent les mêmes sauvegardes. fuse-overlayfs lit le
format du noyau, et les marqueurs propres à fuse-overlayfs (dossiers
opaques `.wh..wh..opq`, fichiers supprimés `.wh.<nom>`) sont convertis avant
chaque montage noyau.

### Mode overlay FUSE (repli)

1. `squashfuse` monte le `.wsquashfs` en lecture seule (`mnt/`)
2. `fuse-overlayfs` crée une vue lecture/écriture (`wine/`) combinant :
   - lowerdir : le squashfs monté (RO)
   - upperdir : `saves/<jeu>/` (RW — seules les modifications)
   - workdir : métadonnées overlay, dans `saves/.work/<jeu>/` — toujours sur
     le même système de fichiers que les sauvegardes (NAS, autre disque,
     volume Docker), sans quoi Wine ne peut rien y écrire
3. Wine exécute le jeu depuis `wine/`
4. À la sortie : démontage automatique, sauvegardes conservées dans `saves/`

### Mode copy (Docker / FUSE bloqué)

1. `squashfuse` (si disponible) ou `unsquashfs` extrait le contenu dans `wine/<jeu>/`
2. Un fichier `.wsquashfs_cache` (taille + mtime de la source) marque le cache comme valide
3. Wine exécute le jeu depuis `wine/<jeu>/`
4. Les sauvegardes sont dans `wine/<jeu>/` (persistantes entre sessions)
5. `--clean` supprime les copies de travail (libère l'espace)

## Structure du projet

```
wsquashfs-launcher/
├── wsquashfs-launcher          # Script principal
├── autorun.cmd.example         # Exemple de configuration
├── install.sh                  # Installation (Debian/Ubuntu, Arch/CachyOS)
├── Dockerfile                  # Image Docker (wine-tkg, DXVK, VKD3D, d7vk)
├── docker-compose.yml          # FUSE, GPU, X11, son, volumes de sauvegardes
├── run-docker.sh               # Lance un jeu dans le conteneur
├── test-setup.sh               # Vérification (--doctor + Docker)
├── tests/                      # Tests de non-régression (make test)
├── tools/                      # dualsense-usb-bridge, fakeping-dinput8
├── Makefile
├── CONTRIBUTING.md
└── LICENSE
```

## Résolution de problèmes

### FUSE bloqué dans un conteneur

```
fusermount3: mount failed: Operation not permitted
```

Le script bascule automatiquement sur `unsquashfs`. Si celui-ci est absent :

```bash
apt-get install squashfs-tools
```

Pour activer FUSE dans le conteneur (mode overlay, recommandé) :
`docker-compose.yml` passe déjà `/dev/fuse` et `privileged: true` ; en
`docker run` : `--device /dev/fuse --cap-add SYS_ADMIN`.

### Overlay noyau indisponible

`wsquashfs-launcher --doctor` donne la cause, et chaque lancement l'affiche
(`Overlay noyau : indisponible, …`) avant de passer à fuse-overlayfs. Le jeu
fonctionne dans les deux cas. Causes fréquentes :

- **squashfuse trop ancien** : l'option `uid=` de `squashfuse_ll` date de
  squashfuse 0.5 (Debian 13, Arch). C'est le cas de l'image Docker, basée sur
  Debian 12.
- **namespaces refusés** : profil seccomp par défaut de Docker (`privileged:
  true` ou `--security-opt seccomp=unconfined` les autorise), AppArmor
  (Ubuntu 24.04 : `kernel.apparmor_restrict_unprivileged_userns`), ou sysctl
  `user.max_user_namespaces=0`.
- **sauvegardes sur un système de fichiers inadapté** : l'upperdir doit gérer
  les attributs `user.*`, et ne peut pas se trouver sur un overlay. En
  conteneur, les sauvegardes doivent donc être sur un volume, et non dans le
  système de fichiers du conteneur.

### Wine ne trouve pas l'affichage

```
Make sure that your X server is running and that $DISPLAY is set correctly.
```

```bash
# Autoriser l'utilisateur courant (run-docker.sh le fait le temps du jeu)
xhost +SI:localuser:$(id -un)

# Vérifier DISPLAY
echo $DISPLAY
```

### Réinitialiser un jeu (mode copy)

```bash
# Supprimer uniquement le cache d'un jeu
rm -rf ~/.cache/wsquashfs/wine/"Nom du Jeu"

# Supprimer tous les caches
wsquashfs-launcher --clean
```

## Licence

MIT — voir [LICENSE](LICENSE).
