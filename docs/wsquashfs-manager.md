# wsquashfs-manager — spécification

Application graphique de gestion des images `.wsquashfs` : traiter des
dossiers en masse, éditer une image (autorun, montée de version), sans
scripts faits à la main. Le lanceur `wsquashfs-launcher` reste tel quel ;
le manager s'appuie sur lui et sur les mêmes conventions.

## 1. Objectifs

1. **Voir la ludothèque** : toutes les images d'un ou plusieurs dossiers
   `roms/`, avec leurs informations utiles, filtrables et triables.
2. **Éditer l'`autorun.cmd`** d'une image : formulaire des clés connues et
   éditeur texte brut.
3. **Monter de version** un jeu : remplacer ou ajouter des fichiers dans
   l'image depuis un dossier (patch, nouveau build), mettre à jour
   `GAME_VERSION`.
4. **Générer l'autorun automatiquement** : analyser les fichiers du jeu
   (exécutables, DLL importées, moteur, bibliothèques présentes) et proposer
   un autorun complet et justifié, à valider (§ 2.5).
5. **Traiter en masse** : appliquer la même opération à une sélection
   d'images ou de dossiers (poser une clé, en retirer une, empaqueter des
   dossiers `.wine`, reconstruire).
6. **Ne jamais abîmer un original** : nouvelle image construite à côté,
   vérifiée, puis échangée ; ancienne gardée jusqu'à validation.

Hors périmètre (v1) : lancer les jeux (c'est le rôle du lanceur et d'ES),
gérer les jaquettes/gamelists (RomM, ES), éditer le registre Wine.

## 2. Écrans

### 2.1 Bibliothèque

Tableau, une ligne par image :

| Colonne | Source |
|---|---|
| Nom, système (dossier `roms/<système>`) | chemin |
| Taille | fichier |
| Version | `GAME_VERSION=` de l'autorun (lu sans monter : `unsquashfs -cat`) |
| Type | prefix Wine (Batocera), prefix Proton (`config_info` + version), jeu seul |
| Runner | `WINE=` / `PROTON=` / `RUNNER=`, sinon « défaut » |
| Manettes | `HIDRAW=1` ou XInput |
| Commande | `CMD=` |
| Extras | `.keys` présent, relais `fakeping`, `DllOverrides` particuliers |
| Sauvegardes | taille de `saves/<jeu>`, type du prefix qui s'y trouve |
| État | en cours d'utilisation (monté / lancé), modifié, sauvegarde `.old` présente |

Filtres : système, type, runner, HIDRAW, « sans version », « a une
sauvegarde .old ». Sélection multiple → menu d'actions en masse (§ 2.4).
Le scan est mis en cache (taille + date de chaque image) : seules les images
modifiées sont relues.

### 2.2 Fiche d'une image

- **Informations** (comme `wsquashfs-launcher --info`) et arborescence de
  l'image (lecture seule, `unsquashfs -l`), avec recherche de fichier.
- **Autorun** :
  - formulaire des clés connues, avec aide et valeurs possibles : `CMD`,
    `DIR`, `GAME_VERSION`, `WINE` (dont `tkg`, `system`), `PROTON`,
    `RUNNER`, `ARCH`, `HIDRAW`, `DXVK`, `VKD3D`, `D7VK`, `ESYNC`, `FSYNC`,
    `VIRTUAL_DESKTOP`, `LANG`, `ENV`, `SAVEDIR`, `SAVEFILES` ;
  - éditeur texte brut (onglet), synchronisé avec le formulaire ;
  - validations : `DIR` existe dans l'image ; l'exécutable de `CMD` existe
    (casse ignorée, comme le lanceur) ; clé inconnue signalée (sans
    bloquer : Batocera en ignore aussi) ; avertissement si `ENV=` contient
    `WINEDLLOVERRIDES` (il écraserait les réglages DLL du lanceur).
- **Montée de version** (§ 3.3).
- **Sauvegardes** : taille, type du prefix, bouton « réinitialiser »
  (renomme en `.avant-<date>`, ne supprime pas).
- **Historique** des opérations sur cette image.

### 2.3 Création

Depuis un dossier (prefix Wine `*.wine`, jeu seul) : détection des
exécutables candidats, `DIR`/`CMD` proposés, type détecté (prefix
`system.reg`, `config_info`), `GAME_VERSION` à saisir, puis empaquetage
(équivalent de `--pack`, zstd).

### 2.4 Actions en masse

Sur une sélection d'images :

- poser / modifier / retirer une clé d'autorun (ex. retirer `HIDRAW=1`,
  poser `GAME_VERSION`, passer `WINE=system` → défaut) ;
- remplacer entièrement l'autorun par un modèle (variables : nom du jeu…) ;
- ajouter un fichier à un chemin donné (ex. relais `fakeping` +
  `DllOverrides`) ;
- réinitialiser les sauvegardes ;
- vérifier l'intégrité (lecture de la table de l'image, autorun lisible).

Sur une sélection de dossiers : empaqueter (`--pack`), avec autorun généré.

Chaque action affiche d'abord un **aperçu** (ce qui change, image par image,
espace disque nécessaire) puis crée des tâches dans la file (§ 4).

Action supplémentaire : **(re)générer l'autorun** de toute une sélection
(§ 2.5), avec un aperçu des propositions image par image et validation
globale ou au cas par cas.

### 2.5 Génération automatique de l'autorun

Proposer un autorun complet à partir de l'analyse de l'image ou du dossier,
**sans l'imposer** : chaque valeur est affichée avec sa justification et un
niveau de confiance, puis validée ou corrigée. Même moteur pour la création
(§ 2.3), la fiche d'une image et les actions en masse.

#### Analyse

Lecture seule, sans lancer le jeu :

- **Exécutables** : en-têtes PE (32/64 bits, sous-système GUI/console,
  drapeau « large address aware »), **table d'imports** (DLL et fonctions),
  taille, nom, ressources de version (`ProductName`, `FileVersion`).
- **Arborescence** : fichiers et dossiers caractéristiques d'un moteur ou
  d'un outil.
- **Prefix** : `system.reg` (Wine/Batocera, architecture win32/win64),
  `config_info` (Proton et sa version), `DllOverrides` du registre
  (`user.reg`).

#### Choix de l'exécutable (`DIR`, `CMD`)

Candidats classés par score :

- exclus : installeurs et redistribuables (`vcredist*`, `dxsetup`,
  `UE4PrereqSetup*`, `*Setup*.exe`), rapporteurs de plantage
  (`UnityCrashHandler*`, `CrashReportClient`), désinstalleurs ;
- favorisés : nom proche du nom de l'image, plus gros exécutable GUI,
  exécutable à la racine du jeu ;
- **Unreal Engine** (`Engine/`, `<Jeu>/Binaries/Win64/*-Win64-Shipping.exe`)
  : lanceur racine ou exécutable `Shipping`, au choix proposé ;
- **Unity** (`UnityPlayer.dll`, dossier `<Jeu>_Data/`) : l'exécutable à côté
  de `UnityPlayer.dll` ;
- **TeknoParrot** (`drive_c/teknoparrot/TeknoParrotUi.exe`) :
  `CMD="TeknoParrotUi.exe" --profile=<profil> --startMinimized`, profil
  retrouvé dans `UserProfiles/` puis `GameProfiles/` par le chemin de
  l'exécutable du jeu ;
- **lanceurs `.bat`** (`launch.bat`, `game.bat`) : proposés tels quels,
  contenu affiché, commandes suspectes signalées (ex. `taskill` au lieu de
  `taskkill`).

#### Règles « tel fichier → tel réglage »

Règles déclaratives (fichier YAML éditable, versionné avec l'application),
évaluées sur l'analyse. Chaque règle produit une clé d'autorun, un fichier à
ajouter, ou un simple avertissement. Règles de départ, issues des cas réels :

| Constat | Proposition | Origine |
|---|---|---|
| import `d3d9`, `d3d10*`, `d3d11`, `dxgi` | `DXVK=1` (défaut) | rendu Vulkan, wined3d = rendu logiciel sous NVIDIA |
| import `d3d12` | `VKD3D=1` (défaut) | Pinball FX |
| import `ddraw` | `D7VK=1` (défaut) | jeux DirectDraw |
| import `opengl32` seul | rien (DXVK sans effet) | — |
| import `xinput1_*` | mode XInput (pas de `HIDRAW`) | défaut |
| `libScePad*.dll` ou chaînes Sony/DualSense dans l'exécutable, sans `steam_api` | proposer `HIDRAW=1` (confiance moyenne) | Until Dawn |
| `steam_api*.dll` présent | ne **pas** proposer `HIDRAW=1` : support DualSense probablement via Steam Input | The Devil in Me, Street Fighter V |
| exécutable 32 bits sans « large address aware » | rien de spécial depuis la correction de la pile ; noter la limite de 2 Go | GTI Club |
| `libavs-win32.dll` (Konami e-amusement) | ajouter le relais `fakeping` (`dinput8.dll` + `DllOverrides`) si l'exécutable importe `DINPUT8` ; sinon avertir | Yu-Gi-Oh! DT6, GTI Club |
| TeknoParrot + `d3dx9_43` natif (Microsoft) dans le prefix | `"*d3dx9_43"="builtin"` dans le registre | Yu-Gi-Oh! DT6 |
| TeknoParrot ancien (`TeknoParrot.dll` < version de référence) | avertir, proposer la mise à jour de TeknoParrot | anti-débogage (Yu-Gi-Oh!) |
| `JVSEmu*.dll` / `JConfig*.exe` | avertir : manette obligatoire au lancement | GTI Club |
| `DemulShooter.exe` | avertir : pistolet, vérifier `config.ini` (périphérique d'origine) | Castlevania |
| prefix `config_info` | Proton de même version (défaut du lanceur) | — |
| prefix Batocera (`system.reg`) | wine-tkg (défaut) ; `ARCH=win32` si `#arch=win32` | — |
| jeu seul (pas de prefix) | Proton par défaut du lanceur (proton-cachyos) | — |
| sauvegardes connues (`Saved/SaveGames`, `Documents/My Games/<Jeu>`, `AppData/…/<Jeu>`) sur prefix Batocera | proposer `SAVEDIR` | — |
| `ENV=` contenant `WINEDLLOVERRIDES` | refuser, proposer l'équivalent registre | écraserait les réglages DLL du lanceur |

Toujours ajouté : `GAME_VERSION` pré-rempli depuis les ressources de version
de l'exécutable (`ProductVersion`/`FileVersion`), à confirmer.

#### Base de connaissances par jeu

Les cas qui ne se déduisent pas des fichiers (ex. un jeu qui exige
`WINE=system`) sont enregistrés par jeu, repérés par l'empreinte de
l'exécutable principal (et à défaut par son nom) : règles propres au jeu,
appliquées en priorité, avec la note expliquant pourquoi. Chaque correction
validée dans l'application peut être ajoutée à cette base en un clic.

#### Contrôle après génération

Mêmes validations que l'édition manuelle (§ 2.2). Option : **lancement
d'essai** chronométré (comme les tests de compatibilité faits à la main :
le jeu tient-il 60 s, erreurs connues dans le journal — `SOCK_RAW`,
`install_bpf`, `out of memory`, `page fault`), résultat joint à la
proposition.

## 3. Opérations sur une image

### 3.1 Principe commun : reconstruire sans extraire

Une image squashfs ne se modifie pas en place : il faut la reconstruire.
Pour éviter d'extraire 10 Go sur disque à chaque fois :

1. monter l'image en lecture seule (`squashfuse`) ;
2. superposer une couche contenant uniquement les changements
   (`fuse-overlayfs`, `upperdir` = dossier temporaire : nouvel autorun,
   fichiers du patch, fichiers supprimés en « whiteout ») ;
3. `mksquashfs` depuis la vue fusionnée vers `<jeu>.wsquashfs.part`
   (zstd, mêmes options que `--pack`) ;
4. vérifier la nouvelle image (§ 3.4) ;
5. échange atomique : original → `<jeu>.wsquashfs.old`, `.part` →
   `<jeu>.wsquashfs` ;
6. démonter, nettoyer.

Espace disque nécessaire : la taille de la nouvelle image (≈ l'ancienne),
plus les fichiers du patch. Vérifié avant de démarrer.

Repli si FUSE est indisponible : extraction complète (`unsquashfs`) puis
`mksquashfs`, avec l'espace disque correspondant (≈ 2 × taille décompressée).

### 3.2 Édition d'autorun

- Réécriture **ligne à ligne** : les commentaires (`REM`, `#`), l'ordre des
  lignes et les fins de ligne (CRLF ou LF, détectées) sont conservés ; une
  clé modifiée garde sa place, une clé ajoutée va en fin de fichier.
- Même lecture que le lanceur : clé en début de ligne, première occurrence
  retenue, `\r` final retiré.
- Autorun seul modifié → reconstruction rapide (§ 3.1).

### 3.3 Montée de version

1. Choisir l'image et un **dossier source** (nouveau build ou patch).
2. Choisir le **point d'ancrage** dans l'image (ex.
   `drive_c/game/<Jeu>/`), proposé d'après `DIR=`.
3. **Aperçu du diff** : fichiers ajoutés, remplacés (taille/date), et en
   option « supprimés » (présents dans l'image mais absents de la source, si
   la source est un build complet).
4. Saisir la nouvelle `GAME_VERSION` (pré-remplie).
5. Reconstruction (§ 3.1) ; avertissement sur les sauvegardes existantes :
   des fichiers du jeu modifiés dans `saves/` masqueraient les nouveaux →
   proposer de les retirer de la couche de sauvegarde (en gardant les
   vraies sauvegardes de partie).

### 3.4 Vérifications après reconstruction

- la table de l'image se lit (`unsquashfs -s`, `unsquashfs -l`) ;
- l'autorun se lit et contient les valeurs attendues ;
- nombre de fichiers et taille cohérents avec l'aperçu ;
- optionnel : empreinte des fichiers modifiés comparée à la source.

L'`.old` n'est supprimé que sur action explicite (« valider ») ou après un
délai configurable ; bouton « revenir à l'ancienne ».

## 4. File de tâches

- Les reconstructions sont longues (5 Go ≈ 1 à 2 min) : exécutées en
  arrière-plan, dans une file, avec progression (sortie de `mksquashfs`),
  journal par tâche, annulation (nettoyage du `.part`).
- Concurrence limitée (1 par défaut : c'est surtout du disque), priorité
  basse (`nice`/`ionice`).
- Verrous : jamais deux tâches sur la même image ; refus si le jeu est en
  cours (même test que le lanceur : montage actif, `WINEPREFIX` dans
  `/proc`).
- La file survit à un redémarrage du service (état sur disque) ; une tâche
  interrompue est remise en attente, son `.part` supprimé.

## 5. Architecture proposée

- **Application web** servie depuis SteamBox (ou un petit conteneur à côté
  qui monte les mêmes dossiers) : utilisable depuis n'importe quel PC du
  réseau, sans passer par la session Moonlight ; les outils nécessaires
  (`squashfs-tools`, `squashfuse`, `fuse-overlayfs`) sont déjà dans l'image.
- **Backend** Python (FastAPI) : scan, lecture/écriture d'autorun,
  planification des tâches ; exécution des outils en sous-processus, sous
  l'utilisateur `arcade` (propriétaire des images et sauvegardes).
- **Frontend** léger (HTMX ou Vue) : tableau, fiche, formulaires, file.
- **Configuration** : dossiers `roms/` à scanner, dossier des sauvegardes
  (`WSQUASHFS_SAVES_DIR`), dossier temporaire, concurrence, rétention des
  `.old`.
- **Accès** : réseau local uniquement, authentification simple (mot de
  passe) — l'outil modifie et supprime des fichiers.
- Réutilise le lanceur quand c'est possible (`--info`, `--pack`) ; la
  logique d'autorun est partagée ou réimplémentée à l'identique, avec des
  tests qui comparent les deux.

## 6. Découpage

| Étape | Contenu | Effort estimé |
|---|---|---|
| 1. Socle | service, config, scan + cache, tableau bibliothèque, fiche en lecture | 1 session |
| 2. Autorun | formulaire + texte brut, validations, reconstruction par overlay, vérification, échange, `.old` | 1 à 2 sessions |
| 3. Masse | sélection, actions en masse sur l'autorun, file de tâches, aperçu, espace disque | 1 à 2 sessions |
| 4. Version | dossier source, diff, montée de version, gestion des sauvegardes qui masquent | 1 à 2 sessions |
| 5. Génération | analyse PE et arborescence, choix de l'exécutable, moteur de règles YAML + règles de départ, `GAME_VERSION` depuis les ressources | 2 sessions |
| 6. Création | empaquetage de dossiers avec autorun généré, ajout de fichiers en masse (fakeping) | 1 session |
| 7. Connaissances | base par jeu, lancement d'essai chronométré | 1 à 2 sessions |

Les étapes 1 à 3 couvrent déjà l'essentiel : remplacer l'autorun et
traiter des dossiers en masse. La génération (étape 5) peut démarrer dès
l'étape 2, sur la fiche d'une image.

## 7. Points à décider

1. **Web ou bureau** : application web (recommandé, accessible depuis ton
   PC) ou application de bureau dans la session XFCE de SteamBox.
2. **Où elle tourne** : dans le conteneur SteamBox (simple, outils déjà là)
   ou dans un conteneur séparé (indépendant des redémarrages de SteamBox).
3. **Rétention des `.old`** : manuelle, ou suppression après N jours.
4. **Surcharge sans reconstruction** (plus tard) : un fichier à côté de
   l'image (`<jeu>.wsquashfs.autorun`) lu par le lanceur, pour tester un
   réglage instantanément avant de l'inscrire dans l'image — ignoré par
   Batocera.
