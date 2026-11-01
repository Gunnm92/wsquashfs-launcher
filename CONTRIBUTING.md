# Guide de contribution

Merci de votre intérêt pour contribuer à WSquashFS Launcher !

## 🚀 Comment contribuer

### Signaler un bug

1. Vérifiez que le bug n'a pas déjà été signalé dans les [Issues](../../issues)
2. Créez une nouvelle issue en incluant :
   - Une description claire du problème
   - Les étapes pour reproduire le bug
   - Le comportement attendu vs observé
   - Votre environnement (OS, version de Wine, Docker, etc.)
   - Les logs pertinents

### Proposer une fonctionnalité

1. Ouvrez une issue pour discuter de la fonctionnalité
2. Expliquez le cas d'usage et les bénéfices
3. Attendez les retours avant de commencer à coder

### Soumettre une Pull Request

1. Forkez le projet
2. Créez une branche pour votre fonctionnalité (`git checkout -b feature/ma-fonctionnalite`)
3. Committez vos changements (`git commit -m 'Ajout de ma fonctionnalité'`)
4. Pushez vers la branche (`git push origin feature/ma-fonctionnalite`)
5. Ouvrez une Pull Request

## 📝 Standards de code

### Scripts Bash

- Utilisez `#!/bin/bash` en première ligne
- Indentez avec 4 espaces
- Ajoutez des commentaires pour les sections complexes
- Vérifiez les erreurs avec `set -e` quand approprié
- Utilisez des variables en majuscules pour les constantes

### Structure

```bash
#!/bin/bash

# Description du script

# Fonction helper
ma_fonction() {
    local param=$1
    echo "$param"
}

# Variables
MA_CONSTANTE="valeur"

# Code principal
```

## 🧪 Tests

Le lanceur a une suite de tests de non-régression (`tests/run-tests.sh`) :
elle lance le vrai script sur des dizaines de scénarios, avec de vrais
montages FUSE mais un faux Wine/Proton/umu qui journalise ses appels, et
compare la trace obtenue à `tests/expected.out`.

```bash
make test          # doit afficher « ✓ N scénarios conformes »
make lint          # shellcheck
make test-update   # après un changement de comportement VOULU : relire
                   # le diff de tests/expected.out avant de commiter
```

Prérequis : squashfuse, fuse-overlayfs, squashfs-tools, attr (getfattr),
util-linux (flock) et l'accès à `/dev/fuse`. Un nouveau comportement
s'accompagne d'un scénario dans `tests/run-tests.sh`.

La trace ne dépend pas de la machine : les runners du système (`/opt`,
`/usr/wine`, `/usr/share/steam/compatibilitytools.d`…) sont cherchés sous
une racine vide (`WSQUASHFS_SYSROOT`), le PATH des tests n'a ni Wine, ni
Proton, ni umu du système, et les valeurs propres à la machine (tailles,
nombre de processeurs) sont normalisées. Une différence sur une autre
machine est donc un vrai écart, à corriger dans le lanceur ou le harnais,
jamais à absorber par `make test-update`.

Après `make test-update`, relire le diff **scénario par scénario** : une
ligne qui disparaît doit correspondre à un changement voulu. Un faux outil
qui s'éloigne du vrai (faux `wineboot` qui perdait l'architecture du
prefix) ou un scénario qui ne vérifie plus rien (partie jamais démarrée,
notée « arrêtée ») passent sinon inaperçus.

Les scénarios de l'overlay noyau ont leur propre trace,
`tests/expected-kernel.out`. Elle n'est produite et comparée que si la
machine sait monter un overlay noyau en namespace utilisateur. Le harnais
le vérifie lui-même, sans le code du lanceur : une régression du lanceur
fait donc échouer ces scénarios au lieu de les faire ignorer. Sur une
machine sans overlay noyau, ils sont ignorés avec un message, et
`make test-update` laisse cette trace inchangée.

Avant de soumettre une PR :

1. Testez votre code sur votre système
2. Vérifiez que les scripts existants fonctionnent toujours
3. Testez avec différents fichiers wsquashfs si possible
4. Vérifiez la compatibilité Docker si vous modifiez cette partie

## 🎯 Priorités actuelles

- [ ] Support complet de Proton
- [ ] Installation automatique de DXVK/VKD3D
- [ ] Support de multiples versions de Wine dans Docker
- [x] Tests automatisés (`make test`)
- [ ] CI/CD avec GitHub Actions
- [ ] Documentation améliorée

## 💡 Idées de contribution

- Améliorer la gestion des erreurs
- Ajouter plus d'exemples d'utilisation
- Améliorer la documentation
- Supporter d'autres formats de compression
- Ajouter une GUI (optionnel)
- Optimiser les performances

## 📚 Ressources utiles

- [Documentation Batocera](https://wiki.batocera.org/)
- [Documentation Wine](https://wiki.winehq.org/)
- [Documentation Docker](https://docs.docker.com/)
- [SquashFS Tools](https://github.com/vasi/squashfuse)

## ❓ Questions

Si vous avez des questions, n'hésitez pas à :
- Ouvrir une issue de type "Question"
- Consulter les discussions existantes
- Contacter les mainteneurs

Merci de contribuer ! 🎉
