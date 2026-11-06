package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// options : résultat de parse_args (script bash).
type options struct {
	action   string
	gameArg  string
	wineOver string
	setArgs  []string
	headless bool
	execCmd  []string
	cmdArgs  []string
}

var setRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// parseSet : --set CLÉ=valeur (parse_set_arg).
func parseSet(v string) (string, error) {
	if !setRe.MatchString(v) {
		return "", fmt.Errorf("Erreur : --set attend CLÉ=valeur (ex. --set NVAPI=0)")
	}
	return v, nil
}

// parseArgs : parse_args du script bash, même ordre et mêmes erreurs.
func parseArgs(args []string) (*options, error) {
	o := &options{action: "run"}
	for i := 0; i < len(args); {
		switch a := args[i]; a {
		case "--help", "-h":
			o.action = "help"
			return o, nil
		case "--setup":
			// Installeur : le reste de la ligne lui appartient (--yes, --dry-run...)
			o.action = "setup"
			o.cmdArgs = args[i+1:]
			return o, nil
		case "--agent":
			o.action = "agent"
			o.cmdArgs = args[i+1:]
			return o, nil
		case "--pair":
			o.action = "pair"
			return o, nil
		case "--update":
			o.action = "update"
			return o, nil
		case "--clean", "--clear", "--umount", "--pack", "--install", "--info",
			"--show", "--saves", "--list", "--doctor":
			o.action = strings.TrimPrefix(a, "--")
			o.cmdArgs = args[i+1:]
			return o, nil
		case "--wine":
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, fmt.Errorf("Erreur : --wine attend une version ou un chemin")
			}
			o.wineOver = args[i+1]
			i += 2
		case "--set":
			v, err := parseSet(argAt(args, i+1))
			if err != nil {
				return nil, err
			}
			o.setArgs = append(o.setArgs, v)
			i += 2
		case "--mount":
			o.action = "mount"
			i++
		case "--prepare":
			o.action = "prepare"
			i++
		case "--headless":
			o.headless = true
			i++
		case "--exec":
			o.action = "exec"
			o.gameArg = argAt(args, i+1)
			if i+2 < len(args) {
				o.execCmd = args[i+2:]
			}
			if o.gameArg == "" || len(o.execCmd) == 0 {
				return nil, fmt.Errorf("Usage : %s --exec <jeu.wsquashfs> <programme> [arguments...]", os.Args[0])
			}
			return o, nil
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("Erreur : option inconnue : %s  (voir %s --help)", a, os.Args[0])
			}
			if o.gameArg != "" {
				return nil, fmt.Errorf("Erreur : un seul jeu attendu (voir %s --help)", os.Args[0])
			}
			o.gameArg = a
			i++
		}
	}
	return o, nil
}

// argAt : args[i] ou "" hors de la liste.
func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}
