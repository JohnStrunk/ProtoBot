package guard

import (
	"path/filepath"
	"strings"
)

// Preserve the existing policy for shell writers to ordinary artifacts while
// preventing common direct writers from bypassing the protected control paths.
func shellWritesProtectedPath(command string, state projectState, cwd string, depth int) bool {
	if depth > 8 {
		return false
	}
	for _, words := range shellCommandSegments(command) {
		args := unwrapCommand(words)
		if len(args) == 0 {
			continue
		}
		for _, target := range protectedShellWriterTargets(args, state.root, cwd) {
			if protectedShellTarget(target, state.root, cwd) {
				return true
			}
		}
		nested, safe := nestedShellCommands(words)
		if !safe {
			continue
		}
		for _, child := range nested {
			if shellWritesProtectedPath(child.script, state, cwd, depth+1) {
				return true
			}
		}
	}
	return false
}

func protectedShellWriterTargets(args []string, root, cwd string) []string {
	switch filepath.Base(args[0]) {
	case "tee":
		return teeTargets(args[1:])
	case "cp":
		return copyTargets(args[1:], root, cwd)
	case "sed":
		return sedInPlaceTargets(args[1:])
	default:
		return nil
	}
}

func teeTargets(args []string) []string {
	var targets []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--":
			targets = append(targets, args[i+1:]...)
			return targets
		case "-a", "--append", "-i", "--ignore-interrupts":
			continue
		default:
			if strings.HasPrefix(arg, "-") {
				continue
			}
			targets = append(targets, arg)
		}
	}
	return targets
}

func copyTargets(args []string, root, cwd string) []string {
	var positional []string
	targetDirectory := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		option, value, hasValue := strings.Cut(arg, "=")
		if strings.HasPrefix(arg, "-t") && len(arg) > 2 {
			targetDirectory = arg[2:]
			continue
		}
		if option == "-t" || option == "--target-directory" {
			if hasValue {
				targetDirectory = value
			} else if i+1 < len(args) {
				i++
				targetDirectory = args[i]
			}
			continue
		}
		if copyOptionTakesValue(option) {
			if !hasValue && i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		positional = append(positional, arg)
	}
	if targetDirectory != "" {
		if protectedShellTarget(targetDirectory, root, cwd) {
			return []string{targetDirectory}
		}
		if adapterDirectory(targetDirectory, root, cwd) {
			for _, source := range positional {
				base := filepath.Base(source)
				if base == "drafting-table.yaml" || strings.ContainsAny(base, "*?[]$`{") {
					return []string{filepath.Join(targetDirectory, "drafting-table.yaml")}
				}
			}
		}
		return nil
	}
	if len(positional) >= 2 {
		destination := positional[len(positional)-1]
		if adapterDirectory(destination, root, cwd) {
			for _, source := range positional[:len(positional)-1] {
				base := filepath.Base(source)
				if base == "drafting-table.yaml" || strings.ContainsAny(base, "*?[]$`{") {
					return []string{filepath.Join(destination, "drafting-table.yaml")}
				}
			}
		}
		return []string{destination}
	}
	return nil
}

func copyOptionTakesValue(option string) bool {
	return contains([]string{"-S", "--suffix", "--context", "--reflink"}, option)
}

func adapterDirectory(value, root, cwd string) bool {
	if root == "" {
		root = cwd
	}
	if root == "" {
		return false
	}
	path := value
	if !filepath.IsAbs(path) {
		base := cwd
		if base == "" {
			base = root
		}
		path = filepath.Join(base, path)
	}
	path, err := filepath.Abs(filepath.Clean(path))
	return err == nil && relativeTo(root, path) == ".agents"
}

func sedInPlaceTargets(args []string) []string {
	inPlace, expressionOption := false, false
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			files = append(files, args[i+1:]...)
			break
		}
		if arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") ||
			(strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "i")) {
			inPlace = true
			continue
		}
		if arg == "-e" || arg == "--expression" || arg == "-f" || arg == "--file" {
			expressionOption = true
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--expression=") || strings.HasPrefix(arg, "--file=") ||
			(strings.HasPrefix(arg, "-e") && len(arg) > 2) || (strings.HasPrefix(arg, "-f") && len(arg) > 2) {
			expressionOption = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		files = append(files, arg)
	}
	if !inPlace {
		return nil
	}
	if !expressionOption && len(files) > 0 {
		files = files[1:] // The first positional is the sed script.
	}
	return files
}

func protectedShellTarget(target, root, cwd string) bool {
	if isLexicalProtectedPath(root, cwd, target) {
		return true
	}
	resolved, err := resolveProjectPath(root, cwd, target)
	return err == nil && protectedProjectPath(root, resolved)
}
