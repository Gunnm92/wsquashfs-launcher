# Configuration Pegasus Frontend pour WSquashFS Launcher

Ce dossier contient un exemple de configuration pour utiliser WSquashFS Launcher avec Pegasus Frontend.

## 🚀 Installation

### 1. Installer wsquashfs-run

```bash
sudo cp ../wsquashfs-run /usr/local/bin/
sudo chmod +x /usr/local/bin/wsquashfs-run
```

### 2. Créer votre dossier de jeux

```bash
mkdir -p ~/Games/wsquashfs
```

### 3. Copier vos fichiers .wsquashfs

Placez vos fichiers `.wsquashfs` dans le dossier :

```bash
cp /path/to/your/*.wsquashfs ~/Games/wsquashfs/
```

### 4. Créer le fichier metadata

Copiez le fichier d'exemple et adaptez-le :

```bash
cp metadata.pegasus.txt ~/Games/wsquashfs/
cd ~/Games/wsquashfs/
nano metadata.pegasus.txt
```

### 5. Configurer Pegasus

Dans Pegasus Frontend, ajoutez le chemin de votre dossier de jeux :
- Ouvrez **Settings** → **Game directories**
- Ajoutez `~/Games/wsquashfs`
- Redémarrez Pegasus

## 📁 Structure du dossier

```
~/Games/wsquashfs/
├── metadata.pegasus.txt        # Configuration Pegasus
├── game1.wsquashfs             # Vos jeux
├── game2.wsquashfs
└── media/                      # Images et médias (optionnel)
    ├── game1-box.png
    ├── game1-screen1.png
    ├── game2-box.png
    └── game2-screen1.png
```

## 🎮 Format du fichier metadata

```
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-run "{file.path}"

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

## 🎨 Assets (médias)

Pegasus supporte plusieurs types d'assets :

### Types d'assets disponibles

| Asset | Description | Taille recommandée |
|-------|-------------|-------------------|
| `assets.boxFront` | Jaquette avant | 300x400 px |
| `assets.boxBack` | Jaquette arrière | 300x400 px |
| `assets.boxSpine` | Tranche de la boîte | 50x400 px |
| `assets.cartridge` | Cartouche/Disque | 400x400 px |
| `assets.logo` | Logo du jeu | 400x150 px |
| `assets.marquee` | Marquee/Banner | 800x300 px |
| `assets.screenshot` | Capture d'écran | 1280x720 px |
| `assets.video` | Vidéo de gameplay | MP4, WebM |
| `assets.background` | Image de fond | 1920x1080 px |

### Exemple avec tous les assets

```
game: Super Game
file: supergame.wsquashfs
developer: Studio XYZ
genre: Action
description: Un jeu génial
assets.boxFront: media/supergame/box-front.png
assets.boxBack: media/supergame/box-back.png
assets.logo: media/supergame/logo.png
assets.screenshot: media/supergame/screen1.png
assets.screenshot: media/supergame/screen2.png
assets.video: media/supergame/gameplay.mp4
assets.background: media/supergame/background.jpg
```

## ⚙️ Configuration avancée

### Variables d'environnement

Vous pouvez personnaliser le lancement avec des variables :

```
launch: WSQUASHFS_SAVES_DIR="$HOME/saves" WSQUASHFS_WINEPREFIX="$HOME/.wine-games" wsquashfs-run "{file.path}"
```

### Organiser par genre

Créez plusieurs collections :

```
collection: Action Games (WSquashFS)
shortname: wsquashfs-action
extensions: wsquashfs
launch: wsquashfs-run "{file.path}"
directory: action/

collection: RPG Games (WSquashFS)
shortname: wsquashfs-rpg
extensions: wsquashfs
launch: wsquashfs-run "{file.path}"
directory: rpg/
```

## 🔧 Dépannage

### Les jeux n'apparaissent pas

1. Vérifiez que le fichier `metadata.pegasus.txt` est dans le bon dossier
2. Vérifiez que l'extension est bien `.wsquashfs`
3. Vérifiez que le dossier est ajouté dans Pegasus Settings
4. Redémarrez Pegasus

### Le jeu ne se lance pas

1. Testez en ligne de commande :
   ```bash
   wsquashfs-run ~/Games/wsquashfs/game.wsquashfs
   ```

2. Vérifiez les logs de Pegasus

3. Vérifiez que les dépendances sont installées :
   ```bash
   ./test-setup.sh
   ```

### Sauvegardes

Les sauvegardes sont dans :
```
~/.local/share/wsquashfs/saves/<nom-du-jeu>/
```

Pour les sauvegarder :
```bash
tar -czf saves-backup.tar.gz ~/.local/share/wsquashfs/saves/
```

## 📚 Ressources

- [Documentation Pegasus](https://pegasus-frontend.org/)
- [Format metadata.txt](https://pegasus-frontend.org/docs/user-guide/meta-files/)
- [Assets Pegasus](https://pegasus-frontend.org/docs/user-guide/meta-assets/)

## 💡 Astuces

### Scraper automatique

Utilisez [Skraper](https://www.skraper.net/) pour récupérer automatiquement les images et métadonnées de vos jeux.

### Thèmes Pegasus

Téléchargez des thèmes sur [le site officiel](https://pegasus-frontend.org/tools/themes/) pour personnaliser l'apparence.

### Performance

Pour de meilleures performances, activez DXVK dans vos fichiers `autorun.cmd` :
```cmd
DXVK=1
ESYNC=1
```
