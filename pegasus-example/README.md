# Configuration Pegasus Frontend pour WSquashFS Launcher

Ce dossier contient un exemple de configuration pour utiliser WSquashFS Launcher avec Pegasus Frontend.

## Installation

### 1. Installer wsquashfs-launcher

```bash
sudo cp ../wsquashfs-launcher /usr/local/bin/
```

### 2. Créer votre dossier de jeux

```bash
mkdir -p ~/Games/wsquashfs
```

### 3. Copier vos fichiers .wsquashfs

```bash
cp /path/to/your/*.wsquashfs ~/Games/wsquashfs/
```

### 4. Créer le fichier metadata

```bash
cp metadata.pegasus.txt ~/Games/wsquashfs/
nano ~/Games/wsquashfs/metadata.pegasus.txt
```

### 5. Configurer Pegasus

Dans Pegasus Frontend : **Settings → Game directories** → ajouter `~/Games/wsquashfs`.

## Structure du dossier

```
~/Games/wsquashfs/
├── metadata.pegasus.txt
├── game1.wsquashfs
├── game2.wsquashfs
└── media/
    ├── game1-box.png
    └── game1-screen.png
```

## Format du fichier metadata

```
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-launcher "{file.path}"

game: Nom du Jeu
file: nomdujeu.wsquashfs
developer: Développeur
publisher: Éditeur
genre: Genre
players: 1-4
description: Description du jeu
rating: 85%
release: 2020-01-15
assets.boxFront: media/jeu-box.png
assets.screenshot: media/jeu-screen.png
```

## Assets disponibles

| Asset | Description | Taille recommandée |
|---|---|---|
| `assets.boxFront` | Jaquette avant | 300×400 px |
| `assets.boxBack` | Jaquette arrière | 300×400 px |
| `assets.logo` | Logo du jeu | 400×150 px |
| `assets.marquee` | Banner | 800×300 px |
| `assets.screenshot` | Capture d'écran | 1280×720 px |
| `assets.video` | Vidéo de gameplay | MP4, WebM |
| `assets.background` | Image de fond | 1920×1080 px |

## Configuration avancée

### Variables d'environnement

```
launch: WSQUASHFS_SAVES_DIR="$HOME/saves" wsquashfs-launcher "{file.path}"
```

### Organiser par genre

```
collection: Action Games (WSquashFS)
shortname: wsquashfs-action
extensions: wsquashfs
launch: wsquashfs-launcher "{file.path}"
directory: action/

collection: RPG Games (WSquashFS)
shortname: wsquashfs-rpg
extensions: wsquashfs
launch: wsquashfs-launcher "{file.path}"
directory: rpg/
```

## Dépannage

### Les jeux n'apparaissent pas

1. Vérifiez que `metadata.pegasus.txt` est dans le bon dossier
2. Vérifiez l'extension `.wsquashfs`
3. Vérifiez que le dossier est ajouté dans Pegasus Settings

### Le jeu ne se lance pas

Testez en ligne de commande :

```bash
wsquashfs-launcher ~/Games/wsquashfs/game.wsquashfs
```

### Sauvegardes

Les sauvegardes sont dans `~/.local/share/wsquashfs/saves/<nom-du-jeu>/` (mode overlay) ou dans `~/.cache/wsquashfs/wine/<nom-du-jeu>/` (mode copy).

```bash
# Sauvegarder
tar -czf saves-backup.tar.gz ~/.local/share/wsquashfs/saves/

# Libérer de l'espace (copies de travail)
wsquashfs-launcher --clean
```

## Ressources

- [Documentation Pegasus](https://pegasus-frontend.org/)
- [Format metadata.txt](https://pegasus-frontend.org/docs/user-guide/meta-files/)
- [Assets Pegasus](https://pegasus-frontend.org/docs/user-guide/meta-assets/)
- [Skraper](https://www.skraper.net/) — récupération automatique des médias
