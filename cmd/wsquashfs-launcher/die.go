package main

// die.go : sorties d'erreur de la commande. Le paquet ui affiche, il ne
// quitte jamais ; la sortie avec cleanup (exitNow) reste ici.

import "wsquashfs-launcher/internal/ui"

// die : erreur, puis sortie (code 1, cleanup compris).
func die(msg string, details ...string) {
	ui.Error(msg, details...)
	exitNow(1)
}

// usageError : rappel de syntaxe, puis code 1.
func usageError(syntax string) {
	ui.Usage(syntax)
	exitNow(1)
}
