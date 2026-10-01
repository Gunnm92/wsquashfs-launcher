# 🚀 Démarrage Rapide

## Installation en 3 étapes

### 1. Installer les dépendances

```bash
# Debian/Ubuntu
sudo apt install squashfuse wine dos2unix

# Arch Linux
sudo pacman -S squashfuse wine dos2unix

# Fedora
sudo dnf install squashfuse wine dos2unix
```

### 2. Installer wsquashfs-run

**Méthode automatique (recommandée)** :

```bash
./install.sh
```

**Méthode manuelle** :

```bash
sudo cp wsquashfs-run /usr/local/bin/
sudo chmod +x /usr/local/bin/wsquashfs-run
```

**Avec Make** :

```bash
make install
```

### 3. Lancer un jeu

```bash
wsquashfs-run /path/to/your/game.wsquashfs
```

## ✅ Vérifier l'installation

```bash
./test-setup.sh
```

## 🎮 Utilisation avec Pegasus Frontend

### Configuration rapide

1. Créez un dossier pour vos jeux :
```bash
mkdir -p ~/Games/wsquashfs
```

2. Copiez vos fichiers `.wsquashfs` :
```bash
cp /path/to/*.wsquashfs ~/Games/wsquashfs/
```

3. Créez le fichier metadata :
```bash
cat > ~/Games/wsquashfs/metadata.pegasus.txt << 'EOF'
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-run "{file.path}"

game: Mon Jeu
file: monjeu.wsquashfs
developer: Developer
genre: Action
description: Description de mon jeu
EOF
```

4. Ajoutez le dossier dans Pegasus :
   - Ouvrez Pegasus → Settings → Game directories
   - Ajoutez `~/Games/wsquashfs`
   - Redémarrez Pegasus

## 📁 Où sont mes sauvegardes ?

Vos sauvegardes et modifications de jeu sont stockées dans :

```
~/.local/share/wsquashfs/saves/<nom-du-jeu>/
```

Pour sauvegarder :

```bash
# Sauvegarder toutes les sauvegardes
tar -czf mes-sauvegardes.tar.gz ~/.local/share/wsquashfs/saves/

# Restaurer
tar -xzf mes-sauvegardes.tar.gz -C ~/
```

## ⚙️ Personnalisation

### Changer l'emplacement des sauvegardes

```bash
export WSQUASHFS_SAVES_DIR="/mnt/nas/saves"
wsquashfs-run game.wsquashfs
```

### Changer l'emplacement des préfixes Wine

```bash
export WSQUASHFS_WINEPREFIX="/mnt/ssd/wine-prefixes"
wsquashfs-run game.wsquashfs
```

## 🔧 Optimisations

Dans votre fichier `autorun.cmd` du jeu, ajoutez :

```cmd
# Pour de meilleures performances 3D
DXVK=1
ESYNC=1

# Pour DirectX 12
VKD3D=1
```

## 🐛 Problèmes courants

### Le jeu ne se lance pas

1. Testez manuellement :
```bash
wsquashfs-run /path/to/game.wsquashfs
```

2. Vérifiez le fichier autorun.cmd :
```bash
squashfuse game.wsquashfs /tmp/test
cat /tmp/test/autorun.cmd
umount /tmp/test
```

### Erreur de montage

Vérifiez que FUSE fonctionne :
```bash
ls -l /dev/fuse
groups | grep fuse
```

Si nécessaire :
```bash
sudo usermod -aG fuse $USER
# Puis déconnectez-vous et reconnectez-vous
```

### Wine ne fonctionne pas

```bash
# Vérifier Wine
wine --version

# Réinitialiser Wine (ATTENTION : supprime tous les préfixes)
rm -rf ~/.local/share/wsquashfs/prefix/
```

## 📚 Documentation complète

- [README.md](README.md) - Documentation complète
- [autorun.cmd.example](autorun.cmd.example) - Exemples de configuration
- [CONTRIBUTING.md](CONTRIBUTING.md) - Guide de contribution

## 💡 Astuce Pro

Créez un alias dans votre `.bashrc` :

```bash
alias wsq='wsquashfs-run'
```

Puis lancez simplement :

```bash
wsq game.wsquashfs
```

## 🎉 C'est tout !

Vous êtes prêt à jouer à vos jeux WSquashFS de Batocera sur n'importe quel Linux !
