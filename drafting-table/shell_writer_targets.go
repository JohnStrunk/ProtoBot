package guard

import (
	"os"
	"path/filepath"
	"strings"
)

type writerOptionSpec struct {
	shortFlags        string
	shortValues       string
	longFlags         []string
	longValues        []string
	shortTargetDir    bool
	longTargetDir     bool
	trackedShortFlags string
	trackedLongFlags  []string
}

type writerArgv struct {
	operands   []string
	targetDirs []string
	tracked    map[string]bool
}

func protectedShellWriterTargets(args []string, state projectState, cwd string) ([]string, bool) {
	if len(args) == 0 {
		return nil, true
	}
	// shortcut: this inventory is not a general shell sandbox; add a writer
	// when review identifies a protected-path bypass.
	if isInformationalCommand(args) && filepath.Base(args[0]) != "ed" {
		return nil, true
	}
	switch filepath.Base(args[0]) {
	case "tee":
		return teeTargets(args[1:])
	case "cp":
		return copyLikeTargets(args[1:], state, cwd, cpWriterOptions, false)
	case "mv":
		return copyLikeTargets(args[1:], state, cwd, mvWriterOptions, true)
	case "install":
		return copyLikeTargets(args[1:], state, cwd, installWriterOptions, false)
	case "ln":
		return copyLikeTargets(args[1:], state, cwd, lnWriterOptions, false)
	case "dd":
		return ddTargets(args[1:])
	case "truncate":
		return truncateTargets(args[1:])
	case "rm":
		return rmTargets(args[1:], state, cwd)
	case "rsync":
		return rsyncTargets(args[1:], state, cwd)
	case "find":
		return findFileTargets(args[1:])
	case "git":
		return gitConfigWriteTargets(args, state, cwd)
	case "sed":
		return sedInPlaceTargets(args[1:]), true
	case "patch", "ed":
		return nil, false
	default:
		return nil, true
	}
}

func teeTargets(args []string) ([]string, bool) {
	var targets []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--":
			return append(targets, args[i+1:]...), true
		case "-a", "--append", "-i", "--ignore-interrupts":
			continue
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return nil, false
			}
			targets = append(targets, arg)
		}
	}
	return targets, true
}

var cpWriterOptions = writerOptionSpec{
	shortFlags:     "adfHilLnpPRrsTuvx",
	shortValues:    "S",
	longFlags:      []string{"--archive", "--attributes-only", "--backup", "--dereference", "--force", "--hard-dereference", "--interactive", "--link", "--no-clobber", "--no-dereference", "--parents", "--preserve", "--recursive", "--remove-destination", "--symbolic-link", "--update", "--verbose", "--one-file-system"},
	longValues:     []string{"--context", "--reflink", "--sparse", "--suffix"},
	shortTargetDir: true,
	longTargetDir:  true,
}

var mvWriterOptions = writerOptionSpec{
	shortFlags:     "finuvbT",
	shortValues:    "S",
	longFlags:      []string{"--backup", "--exchange", "--force", "--interactive", "--no-clobber", "--no-target-directory", "--verbose", "--update", "--whiteout"},
	longValues:     []string{"--suffix"},
	shortTargetDir: true,
	longTargetDir:  true,
}

var installWriterOptions = writerOptionSpec{
	shortFlags:        "bCDdpZsvT",
	shortValues:       "moguS",
	longFlags:         []string{"--backup", "--compare", "--directory", "--preserve-timestamps", "--strip", "--verbose", "--no-target-directory"},
	longValues:        []string{"--group", "--mode", "--owner", "--preserve-context", "--suffix"},
	shortTargetDir:    true,
	longTargetDir:     true,
	trackedShortFlags: "dD",
	trackedLongFlags:  []string{"--directory"},
}

var lnWriterOptions = writerOptionSpec{
	shortFlags:     "bfnPrsvTL",
	shortValues:    "S",
	longFlags:      []string{"--backup", "--force", "--interactive", "--logical", "--no-dereference", "--no-target-directory", "--physical", "--relative", "--symbolic", "--verbose"},
	longValues:     []string{"--suffix"},
	shortTargetDir: true,
	longTargetDir:  true,
}

func parseWriterArgv(args []string, spec writerOptionSpec) (writerArgv, bool) {
	parsed := writerArgv{tracked: make(map[string]bool)}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			parsed.operands = append(parsed.operands, args[i+1:]...)
			return parsed, true
		}
		if strings.HasPrefix(arg, "--") {
			option, value, hasValue := strings.Cut(arg, "=")
			switch {
			case spec.longTargetDir && option == "--target-directory":
				if !hasValue {
					if i+1 >= len(args) {
						return writerArgv{}, false
					}
					i++
					value = args[i]
				}
				parsed.targetDirs = append(parsed.targetDirs, value)
			case contains(spec.longValues, option):
				if !hasValue {
					if i+1 >= len(args) {
						return writerArgv{}, false
					}
					i++
				}
			case contains(spec.longFlags, option):
				if hasValue {
					return writerArgv{}, false
				}
				if contains(spec.trackedLongFlags, option) {
					parsed.tracked[option] = true
				}
			default:
				return writerArgv{}, false
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags := arg[1:]
			for index := 0; index < len(flags); index++ {
				flag := flags[index]
				if spec.shortTargetDir && flag == 't' {
					value := flags[index+1:]
					if value == "" {
						if i+1 >= len(args) {
							return writerArgv{}, false
						}
						i++
						value = args[i]
					}
					parsed.targetDirs = append(parsed.targetDirs, value)
					break
				}
				if strings.ContainsRune(spec.shortValues, rune(flag)) {
					if index+1 == len(flags) {
						if i+1 >= len(args) {
							return writerArgv{}, false
						}
						i++
					}
					break
				}
				if !strings.ContainsRune(spec.shortFlags, rune(flag)) {
					return writerArgv{}, false
				}
				if strings.ContainsRune(spec.trackedShortFlags, rune(flag)) {
					parsed.tracked[string(flag)] = true
				}
			}
			continue
		}
		parsed.operands = append(parsed.operands, arg)
	}
	return parsed, true
}

func copyLikeTargets(args []string, state projectState, cwd string, spec writerOptionSpec, includeSources bool) ([]string, bool) {
	parsed, safe := parseWriterArgv(args, spec)
	if !safe {
		return nil, false
	}
	if parsed.tracked["d"] || parsed.tracked["D"] || parsed.tracked["--directory"] {
		// install -d creates every operand as a directory.
		targets := append([]string(nil), parsed.operands...)
		targets = append(targets, parsed.targetDirs...)
		return targets, true
	}
	var targets []string
	for _, targetDir := range parsed.targetDirs {
		targets = append(targets, targetDir)
		if adapterManifestTarget(state, cwd, targetDir) != "" {
			if anyManifestSource(parsed.operands) {
				targets = append(targets, adapterManifestTarget(state, cwd, targetDir))
			}
		}
	}
	if len(parsed.targetDirs) == 0 && len(parsed.operands) >= 2 {
		target := parsed.operands[len(parsed.operands)-1]
		sources := parsed.operands[:len(parsed.operands)-1]
		targets = append(targets, target)
		if adapterManifestTarget(state, cwd, target) != "" && (includeSources || anyManifestSource(sources)) {
			targets = append(targets, adapterManifestTarget(state, cwd, target))
		}
	}
	if includeSources {
		sources := parsed.operands
		if len(parsed.targetDirs) == 0 && len(sources) >= 2 {
			sources = sources[:len(sources)-1]
		}
		for _, source := range sources {
			if directorySourceContainsProtectedMetadata(state, cwd, source) {
				targets = append(targets, state.manifestPath, state.gitCommonDir)
			}
			targets = append(targets, source)
		}
	}
	return targets, true
}

func anyManifestSource(sources []string) bool {
	for _, source := range sources {
		base := filepath.Base(source)
		if base == "drafting-table.yaml" || strings.ContainsAny(base, "*?[]") {
			return true
		}
	}
	return false
}

func adapterManifestTarget(state projectState, cwd, directory string) string {
	if state.root != "" {
		lexical, err := lexicalProjectPath(state.root, cwd, directory)
		if err == nil && filepath.Clean(lexical) == filepath.Join(state.root, ".agents") {
			return filepath.Join(state.root, ".agents", "drafting-table.yaml")
		}
	}
	resolved, err := resolveProjectPath(state.root, cwd, directory)
	if err != nil {
		return ""
	}
	if state.manifestPath != "" && filepath.Clean(resolved) == filepath.Clean(filepath.Dir(state.manifestPath)) {
		return state.manifestPath
	}
	projectAgents := filepath.Join(state.root, ".agents")
	if state.root != "" && filepath.Clean(resolved) == filepath.Clean(projectAgents) {
		return filepath.Join(projectAgents, "drafting-table.yaml")
	}
	return ""
}

func directorySourceContainsProtectedMetadata(state projectState, cwd, source string) bool {
	resolved, err := resolveProjectPath(state.root, cwd, source)
	if err != nil {
		return true
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return false
	}
	projectGitDir := filepath.Join(state.root, ".git")
	return withinOrEqual(resolved, projectGitDir) || withinOrEqual(resolved, state.gitDir) || withinOrEqual(resolved, state.gitCommonDir) ||
		(state.manifestPath != "" && withinOrEqual(resolved, state.manifestPath))
}

func ddTargets(args []string) ([]string, bool) {
	var targets []string
	for _, arg := range args {
		if !strings.Contains(arg, "=") {
			if arg == "--help" || arg == "--version" {
				return nil, true
			}
			return nil, false
		}
		key, value, _ := strings.Cut(arg, "=")
		switch key {
		case "of":
			if value == "" {
				return nil, false
			}
			targets = append(targets, value)
		case "if", "bs", "ibs", "obs", "count", "skip", "seek", "status", "conv", "iflag", "oflag", "cbs", "fullblock", "count_bytes", "skip_bytes", "seek_bytes":
		default:
			return nil, false
		}
	}
	return targets, true
}

func truncateTargets(args []string) ([]string, bool) {
	parsed, safe := parseWriterArgv(args, writerOptionSpec{
		shortFlags:  "c",
		shortValues: "sr",
		longFlags:   []string{"--no-create"},
		longValues:  []string{"--reference", "--size"},
	})
	return parsed.operands, safe
}

func rmTargets(args []string, state projectState, cwd string) ([]string, bool) {
	parsed, safe := parseWriterArgv(args, writerOptionSpec{
		shortFlags:        "dfiIRrv",
		longFlags:         []string{"--dir", "--force", "--interactive", "--no-preserve-root", "--one-file-system", "--preserve-root", "--recursive", "--verbose"},
		trackedShortFlags: "rR",
		trackedLongFlags:  []string{"--recursive"},
	})
	if !safe {
		return nil, false
	}
	targets := append([]string(nil), parsed.operands...)
	if parsed.tracked["r"] || parsed.tracked["R"] || parsed.tracked["--recursive"] {
		for _, source := range parsed.operands {
			if directorySourceContainsProtectedMetadata(state, cwd, source) {
				targets = append(targets, state.manifestPath, state.gitCommonDir)
			}
		}
	}
	return targets, true
}

func rsyncTargets(args []string, state projectState, cwd string) ([]string, bool) {
	parsed, safe := parseWriterArgv(args, rsyncWriterOptions)
	if !safe || len(parsed.operands) < 2 {
		return nil, safe
	}
	for _, path := range parsed.operands {
		if strings.Contains(path, ":") || strings.HasPrefix(path, "rsync://") {
			return nil, false
		}
	}
	destination := parsed.operands[len(parsed.operands)-1]
	targets := []string{destination}
	if adapterManifestTarget(state, cwd, destination) != "" || directorySourceContainsProtectedMetadata(state, cwd, destination) {
		targets = append(targets, adapterManifestTarget(state, cwd, destination), state.manifestPath, state.gitCommonDir)
	}
	return targets, true
}

var rsyncWriterOptions = writerOptionSpec{
	shortFlags:  "avzrlptgoDAXHhnqviIcLPS",
	shortValues: "eFM",
	longFlags:   []string{"--archive", "--backup", "--checksum", "--compress", "--copy-links", "--copy-unsafe-links", "--delete", "--delete-after", "--delete-before", "--delete-during", "--delete-excluded", "--dirs", "--executability", "--existing", "--group", "--hard-links", "--human-readable", "--ignore-existing", "--ignore-missing-args", "--ignore-errors", "--inplace", "--itemize-changes", "--links", "--mkpath", "--no-implied-dirs", "--omit-dir-times", "--owner", "--perms", "--recursive", "--relative", "--remove-source-files", "--safe-links", "--size-only", "--specials", "--stats", "--times", "--update", "--verbose", "--whole-file"},
	longValues:  []string{"--backup-dir", "--chmod", "--compare-dest", "--copy-dest", "--delete-missing-args", "--exclude", "--exclude-from", "--files-from", "--filter", "--include", "--include-from", "--link-dest", "--max-size", "--min-size", "--modify-window", "--out-format", "--partial-dir", "--password-file", "--rsync-path", "--suffix", "--temp-dir", "--timeout", "--write-batch"},
}

func findFileTargets(args []string) ([]string, bool) {
	var targets []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-delete":
			return nil, false
		case "-fprint", "-fprint0", "-fls", "-fprintf":
			if i+1 >= len(args) {
				return nil, false
			}
			targets = append(targets, args[i+1])
			i++
			if args[i-1] == "-fprintf" {
				if i+1 >= len(args) {
					return nil, false
				}
				i++
			}
		}
	}
	return targets, true
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
