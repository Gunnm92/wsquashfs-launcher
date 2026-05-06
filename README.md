# WSquashFS Launcher

Launcher standalone pour fichiers `.wsquashfs` de Batocera sur n'importe quel Linux — machine physique ou conteneur Docker.

Compatible avec Pegasus Frontend, EmulationStation et autres launchers.

## Description

Ce projet permet de monter et exécuter des fichiers `.wsquashfs` (format Batocera pour les jeux Windows) sur n'importe quelle distribution Linux, avec ou sans FUSE disponible.

Le script détecte automatiquement les capacités de l'environnement et choisit le mode de montage optimal :

| Mode | Conditions | Sauvegardes |
|---|---|---|
| **overlay** | squashfuse + fuse-overlayfs disponibles | Isolées dans `SAVES_DIR` (upperdir) |
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

### Prérequis

```bash
# Debian/Ubuntu — machine physique (overlay complet)
sudo apt install squashfuse fuse-overlayfs wine

# Debian/Ubuntu — Docker sans FUSE (fallback unsquashfs)
sudo apt install squashfs-tools wine

# Arch Linux
sudo pacman -S squashfuse wine
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

```bash
# Lancer un jeu
wsquashfs-launcher /path/to/game.wsquashfs

# Supprimer les copies de travail (libère de l'espace)
wsquashfs-launcher --clean

# Afficher l'aide
wsquashfs-launcher --help
```

### Variables d'environnement

```bash
export WSQUASHFS_SAVES_DIR="$HOME/mes-sauvegardes"   # sauvegardes overlay
export WSQUASHFS_CACHE="$HOME/.cache/mes-jeux"        # cache de travail

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

### Variables Wine

| Variable | Description | Exemples |
|---|---|---|
| `WINE` | Version Wine dans `/opt/wine-<VERSION>/bin/wine` | `9.0`, `lutris-7.2` |
| `PROTON` | Proton via `/usr/local/bin/proton` | `GE-Proton8-25` |
| `RUNNER` | Chemin absolu vers un runner personnalisé | `/opt/wine-custom/bin/wine` |
| `ARCH` | Architecture Wine (défaut : `win64`) | `win32`, `win64` |
| `LANG` | Langue (`LC_ALL`) | `fr_FR.UTF-8` |
| `ENV` | Variables d'environnement additionnelles | `VAR1=val1 VAR2=val2` |

### Variables d'optimisation

| Variable | Description | Valeurs |
|---|---|---|
| `DXVK` | DirectX → Vulkan (via `WINEDLLOVERRIDES`) | `0` / `1` |
| `VKD3D` | DirectX 12 → Vulkan (via `WINEDLLOVERRIDES`) | `0` / `1` |
| `ESYNC` | Event synchronization | `0` / `1` |
| `FSYNC` | Futex synchronization | `0` / `1` |

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
│       ├── game1/          # Mode overlay : upperdir fuse-overlayfs (diff uniquement)
│       └── game2/
└── .cache/wsquashfs/
    ├── mnt/                # Montage squashfuse temporaire (mode overlay)
    ├── wine/               # Overlay ou copie de travail persistante (mode copy)
    │   └── game1/
    │       └── .wsquashfs_cache   # Empreinte source pour détection de changement
    └── work/               # Workdir overlay (mode overlay)
```

## Fonctionnement

### Mode overlay (machine physique avec FUSE)

1. `squashfuse` monte le `.wsquashfs` en lecture seule (`mnt/`)
2. `fuse-overlayfs` crée une vue lecture/écriture (`wine/`) combinant :
   - lowerdir : le squashfs monté (RO)
   - upperdir : `saves/<jeu>/` (RW — seules les modifications)
   - workdir : métadonnées overlay
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
├── pegasus-example/            # Intégration Pegasus Frontend
├── Dockerfile                  # Image Docker
├── docker-compose.yml
├── run-docker.sh
├── test-setup.sh               # Vérification de l'installation
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

Pour activer FUSE dans le conteneur (optionnel) :

```bash
docker run --device /dev/fuse ...
```

### Wine ne trouve pas l'affichage

```
Make sure that your X server is running and that $DISPLAY is set correctly.
```

```bash
# Autoriser les connexions X11 locales
xhost +local:docker

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
