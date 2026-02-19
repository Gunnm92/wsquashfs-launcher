# WSquashFS Launcher

**Launcher standalone pour fichiers WSquashFS de Batocera sur n'importe quel Linux**

Compatible avec Pegasus Frontend, EmulationStation et autres launchers.

## 📋 Description

Ce projet permet de monter et exécuter des fichiers `.wsquashfs` (format utilisé par Batocera pour empaqueter des jeux Windows) sur **n'importe quelle distribution Linux**.

Le script utilise la même technologie que Batocera :
- **SquashFS** pour le montage en lecture seule du jeu
- **OverlayFS** pour permettre les écritures (sauvegardes, configurations)
- **Wine** pour l'exécution des jeux Windows

Les modifications du jeu (sauvegardes, configs) sont stockées séparément dans `~/.local/share/wsquashfs/saves/`, permettant de garder le fichier `.wsquashfs` intact.

## 🎯 Fonctionnalités

- ✅ **Montage OverlayFS** comme Batocera (lecture seule + couche modifiable)
- ✅ **Sauvegardes persistantes** séparées du fichier .wsquashfs
- ✅ **100% compatible** avec le format Batocera
- ✅ Support complet des variables `autorun.cmd` de Batocera
- ✅ Configuration automatique Wine (DXVK, VKD3D, ESYNC, FSYNC)
- ✅ Support versions Wine multiples et Proton
- ✅ **Compatible avec les launchers** (Pegasus, EmulationStation, etc.)
- ✅ **Standalone** - pas besoin de Docker
- ✅ Nettoyage automatique des montages
- ✅ Fonctionne sur toutes les distributions Linux

## 🚀 Installation

### Prérequis

```bash
# Debian/Ubuntu
sudo apt install squashfuse fuse-overlayfs wine dos2unix

# Arch Linux
sudo pacman -S squashfuse wine dos2unix

# Fedora
sudo dnf install squashfuse wine dos2unix
```

### Installation du script

#### Installation automatique (recommandé)

```bash
git clone https://github.com/votre-repo/wsquashfs-launcher.git
cd wsquashfs-launcher
./install.sh
```

Le script d'installation :
- ✅ Vérifie les dépendances
- ✅ Installe `wsquashfs-run` dans `/usr/local/bin` (avec sudo) ou `~/.local/bin`
- ✅ Propose de créer une entrée desktop pour les GUI
- ✅ Propose d'associer les fichiers `.wsquashfs`
- ✅ Configure le PATH automatiquement

#### Installation manuelle

```bash
chmod +x wsquashfs-run
sudo cp wsquashfs-run /usr/local/bin/
```

#### Avec Make

```bash
make install
```

#### Désinstallation

```bash
./install.sh --uninstall
# ou
make uninstall
```

### Vérifier l'installation

```bash
./test-setup.sh
# ou après installation
wsquashfs-run --help
```

## 📖 Utilisation

### Utilisation de base

```bash
# Lancer un jeu
wsquashfs-run /path/to/game.wsquashfs

# Exemple avec un chemin complet
wsquashfs-run ~/Games/Batocera/MyGame.wsquashfs

# Nettoyer les fichiers temporaires (depuis n'importe où)
wsquashfs-run --clean

# Afficher l'aide
wsquashfs-run --help
```

### Variables d'environnement

Personnalisez les emplacements :

```bash
# Changer le répertoire des sauvegardes
export WSQUASHFS_SAVES_DIR="$HOME/mes-sauvegardes"

# Changer le répertoire des préfixes Wine
export WSQUASHFS_WINEPREFIX="$HOME/.wine-games"

# Changer le répertoire de cache
export WSQUASHFS_CACHE="$HOME/.cache/mes-jeux"

# Lancer le jeu
wsquashfs-run game.wsquashfs
```

### Intégration avec Pegasus Frontend

Créez un fichier `metadata.pegasus.txt` dans votre dossier de jeux :

```
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-run "{file.path}"

game: My Game
file: MyGame.wsquashfs
developer: Developer Name
genre: Action
description: Description du jeu
```

### Intégration avec EmulationStation

Éditez `/etc/emulationstation/es_systems.cfg` :

```xml
<system>
  <name>wsquashfs</name>
  <fullname>Windows (WSquashFS)</fullname>
  <path>~/roms/wsquashfs</path>
  <extension>.wsquashfs</extension>
  <command>wsquashfs-run %ROM%</command>
  <platform>pc</platform>
  <theme>windows</theme>
</system>
```

## 📁 Structure du fichier autorun.cmd

Le script s'attend à trouver un fichier `autorun.cmd` à la racine du wsquashfs. Ce fichier utilise le format Batocera et supporte de nombreuses variables pour configurer l'exécution.

### Variables de base (obligatoires)

```cmd
DIR=chemin/vers/dossier
CMD=executable.exe arguments
```

### Variables Wine/Proton (optionnelles)

| Variable | Description | Exemples |
|----------|-------------|----------|
| `WINE` | Version de Wine à utiliser | `9.0`, `lutris-7.2` |
| `PROTON` | Version de Proton (Steam) | `9.0`, `GE-Proton8-25` |
| `RUNNER` | Chemin vers un runner personnalisé | `/opt/wine-custom/bin/wine` |
| `ARCH` | Architecture Wine | `win32`, `win64` (défaut) |

### Variables d'optimisation (optionnelles)

| Variable | Description | Valeurs |
|----------|-------------|---------|
| `DXVK` | DirectX vers Vulkan | `0` (off), `1` (on) |
| `VKD3D` | DirectX 12 vers Vulkan | `0` (off), `1` (on) |
| `ESYNC` | Event synchronization | `0` (off), `1` (on) |
| `FSYNC` | Futex synchronization | `0` (off), `1` (on) |

### Exemples complets

**Jeu simple :**
```cmd
DIR=Game
CMD=game.exe -fullscreen
```

**Jeu 3D moderne avec optimisations :**
```cmd
DIR=Game
CMD=game.exe -fullscreen
WINE=9.0
ARCH=win64
DXVK=1
ESYNC=1
```

**Jeu DirectX 12 avec VKD3D :**
```cmd
DIR=bin
CMD=launcher.exe
WINE=9.0
VKD3D=1
FSYNC=1
```

**Avec Proton (Steam) :**
```cmd
DIR=Game
CMD=game.exe
PROTON=GE-Proton8-25
DXVK=1
ESYNC=1
```

Voir [autorun.cmd.example](autorun.cmd.example) pour plus d'exemples.

## 🔧 Configuration

### Variables d'environnement (Docker)

- `DISPLAY` : Affichage X11 (pour l'interface graphique)
- `WINEPREFIX` : Préfixe Wine personnalisé (optionnel)

### Bottles (Desktop uniquement)

Par défaut, le script utilise une bouteille Bottles nommée "Soda". Pour changer :

Éditez [wsquashfs-launcher:37](wsquashfs-launcher#L37) :
```bash
BOTTLE_NAME="VotreNomDeBouteille"
```

## 🐛 Résolution de problèmes

### Le montage échoue
```bash
# Vérifier que squashfuse est installé
which squashfuse

# Vérifier les permissions
ls -l /path/to/game.wsquashfs
```

### Wine ne démarre pas
```bash
# Tester Wine
wine --version

# Réinitialiser le préfixe Wine
rm -rf ~/.wine
wineboot
```

### Problèmes Docker avec l'affichage
```bash
# Autoriser les connexions X11 locales
xhost +local:docker

# Vérifier DISPLAY
echo $DISPLAY
```

## 🏗️ Architecture

### Structure du projet

```
wsquashfs-launcher/
├── wsquashfs-run               # ⭐ Script principal standalone
├── autorun.cmd.example         # Exemple de configuration
├── pegasus-example/            # Configuration Pegasus Frontend
│   ├── metadata.pegasus.txt    # Exemple metadata
│   └── README.md               # Guide Pegasus
├── test-setup.sh               # Script de vérification
├── Makefile                    # Installation facilitée
├── CONTRIBUTING.md             # Guide de contribution
├── LICENSE                     # Licence MIT
└── README.md                   # Cette documentation

# Fichiers optionnels (Docker - non nécessaires)
├── wsquashfs-launcher          # Version avec support Bottles
├── wsquashfs-launcher-docker   # Version Docker
├── Dockerfile                  # Image Docker
├── docker-compose.yml          # Docker Compose
└── run-docker.sh               # Helper Docker
```

### Architecture des données

```
$HOME/
├── .local/share/wsquashfs/
│   ├── saves/                  # Sauvegardes des jeux (overlay upperdir)
│   │   ├── game1/              # Modifications pour game1.wsquashfs
│   │   └── game2/              # Modifications pour game2.wsquashfs
│   └── prefix/                 # Préfixes Wine par jeu
│       ├── game1/              # Préfixe Wine pour game1
│       └── game2/              # Préfixe Wine pour game2
└── .cache/wsquashfs/
    ├── mnt/                    # Points de montage squashfs (temporaire)
    ├── wine/                   # Points de montage overlay (temporaire)
    └── work/                   # Workdir pour overlay (temporaire)
```

### Comment ça fonctionne

1. **Montage SquashFS** : Le fichier `.wsquashfs` est monté en lecture seule dans `~/.cache/wsquashfs/mnt/`
2. **Montage Overlay** : Un overlay est créé combinant :
   - **lowerdir** (lecture seule) : le squashfs monté
   - **upperdir** (lecture/écriture) : `~/.local/share/wsquashfs/saves/<jeu>/`
   - **workdir** : répertoire de travail pour overlay
3. **Exécution** : Wine lance le jeu depuis le point de montage overlay
4. **Sauvegarde** : Toutes les modifications sont écrites dans upperdir
5. **Nettoyage** : Les montages sont démontés automatiquement

## 🤝 Contribution

Les contributions sont les bienvenues ! N'hésitez pas à :
- Signaler des bugs
- Proposer des nouvelles fonctionnalités
- Soumettre des pull requests

## 📝 Licence

Ce projet est sous licence MIT. Voir le fichier [LICENSE](LICENSE) pour plus de détails.

## ⚠️ Statut

**Travail en cours** - Le projet fonctionne partiellement. Contributions et tests bienvenus !

### Notes de compatibilité

- ✅ Variables `DIR` et `CMD` : Pleinement supportées
- ✅ Variables `WINE`, `ARCH`, `ESYNC`, `FSYNC` : Supportées
- ⚠️ Variables `PROTON` : Support basique (nécessite installation manuelle de Proton)
- ⚠️ Variables `DXVK`, `VKD3D` : Détectées mais nécessitent installation dans le préfixe Wine
- ⚠️ Variable `RUNNER` : Support basique

### Différences entre versions

| Fonctionnalité | Desktop | Docker |
|----------------|---------|--------|
| Wine standard | ✅ | ✅ |
| Bottles | ✅ | ❌ |
| Versions Wine multiples | ⚠️ | ⚠️ |
| Proton | ⚠️ | ⚠️ |
| DXVK/VKD3D | ⚠️ | ⚠️ |
| ESYNC/FSYNC | ✅ | ✅ |

## 🔗 Ressources

- [Batocera](https://batocera.org/)
- [Wine](https://www.winehq.org/)
- [SquashFS](https://github.com/vasi/squashfuse) 
