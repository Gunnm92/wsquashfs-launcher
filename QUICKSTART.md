# 🚀 Démarrage rapide

## 1. Installer

```bash
curl -fsSL https://raw.githubusercontent.com/Gunnm92/wsquashfs-launcher/main/install.sh | bash
```

ou, depuis le dépôt cloné : `./install.sh` (ou `make install`).

L'installeur détecte la distribution — **Debian/Ubuntu** (apt) ou **Arch,
CachyOS, Manjaro, EndeavourOS** (pacman) — installe les paquets système
(squashfuse, fuse-overlayfs, bibliothèques 32 bits…), puis propose wine-tkg,
umu et un Proton, DXVK, VKD3D-Proton et d7vk. Il pose ses questions au
terminal ; `--yes` pour tout accepter, `--dry-run` pour voir ce qu'il ferait
sans rien modifier.

## 2. Vérifier

```bash
wsquashfs-launcher --doctor      # ou ./test-setup.sh depuis le dépôt
```

Chaque point est marqué ✓ (en place), ✗ (manquant, avec la conséquence) ou –
(facultatif).

## 3. Lancer un jeu

```bash
wsquashfs-launcher /chemin/vers/MonJeu.wsquashfs
```

Commandes utiles :

```bash
wsquashfs-launcher --list                  # jeux connus, état, sauvegardes
wsquashfs-launcher --info MonJeu.wsquashfs # version, type, commande
wsquashfs-launcher --exec MonJeu.wsquashfs winecfg
wsquashfs-launcher --backup                # archiver les sauvegardes
wsquashfs-launcher --help
```

## 🎮 Frontend (Pegasus, EmulationStation…)

Commande de lancement : `wsquashfs-launcher "{file.path}"`.

Exemple Pegasus (`metadata.pegasus.txt` dans le dossier des jeux) :

```
collection: Windows Games (WSquashFS)
shortname: wsquashfs
extensions: wsquashfs
launch: wsquashfs-launcher "{file.path}"
```

## 📁 Où sont mes sauvegardes ?

Ce que le jeu écrit (sauvegardes, réglages) va dans
`~/.local/share/wsquashfs/saves/<Nom du jeu>/` ; le `.wsquashfs` n'est jamais
modifié.

```bash
wsquashfs-launcher --backup                # archives dans ~/.local/share/wsquashfs/backups
wsquashfs-launcher --backup "MonJeu"       # un seul jeu
```

Autre emplacement : `export WSQUASHFS_SAVES_DIR=/mnt/nas/saves`.

## ⚙️ Réglages par jeu

Dans l'`autorun.cmd` du jeu (format Batocera), ou sans reconstruire l'image
dans un fichier `MonJeu.wsquashfs.autorun` à côté :

```
DXVK=0             # rendu OpenGL de Wine au lieu de DXVK (activé par défaut)
ESYNC=1
VIRTUAL_DESKTOP=1920x1080
HIDRAW=1           # DualSense gérée par le jeu lui-même
```

Toutes les clés : [README.md](README.md) et [autorun.cmd.example](autorun.cmd.example).

## 🐳 Docker

```bash
make build-docker                          # ou : docker compose build
./run-docker.sh /chemin/vers/MonJeu.wsquashfs
./run-docker.sh --doctor
```

L'image contient wine-tkg, le Wine de Debian, DXVK, VKD3D-Proton et d7vk ; pas
de Proton. Sauvegardes et cache sont dans des volumes Docker.

## 🐛 Problèmes courants

- **Le jeu ne se lance pas** : relancer depuis un terminal pour lire le
  récapitulatif et les erreurs Wine ; vérifier la commande avec `--info`.
- **Erreur de montage** : `wsquashfs-launcher --doctor` vérifie `/dev/fuse`,
  squashfuse et fuse-overlayfs. Sans FUSE (certains conteneurs), le lanceur
  passe en mode copy (unsquashfs).
- **Un jeu reste monté** : `wsquashfs-launcher --umount` (tous les jeux).
- **Repartir de zéro pour un jeu** : `wsquashfs-launcher --clear "MonJeu"` ne
  garde que `drive_c/users`, après une archive automatique.

## 📚 Documentation

- [README.md](README.md) — documentation complète
- [autorun.cmd.example](autorun.cmd.example) — exemples de configuration
- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution et tests
