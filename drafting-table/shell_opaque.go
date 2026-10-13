package guard

import (
	"path/filepath"
	"strings"
)

// These checks deliberately recognize common execution boundaries, not
// program contents. Unknown command prefixes also pass through the literal
// governed-write backstop; script-file execution and renamed binaries remain
// documented residual limits rather than a claim of general containment.
func isCodeInterpreter(name string, args []string) bool {
	name = strings.ToLower(name)
	family := ""
	switch {
	case name == "python" || isVersionedInterpreter(name, "python") || name == "pypy" || isVersionedInterpreter(name, "pypy"):
		family = "python"
	case name == "perl" || isVersionedInterpreter(name, "perl"):
		family = "perl"
	case name == "ruby" || isVersionedInterpreter(name, "ruby"):
		family = "ruby"
	case name == "node" || name == "nodejs":
		family = "node"
	case name == "osascript":
		family = "osascript"
	case name == "awk" || name == "gawk" || name == "mawk":
		family = "awk"
	case name == "php":
		family = "php"
	case name == "lua" || strings.HasPrefix(name, "lua5."):
		family = "lua"
	case name == "rscript":
		family = "rscript"
	case name == "gdb":
		family = "gdb"
	case name == "expect":
		family = "expect"
	case name == "pwsh" || name == "powershell":
		family = "powershell"
	case name == "guile":
		family = "guile"
	case name == "clisp":
		family = "clisp"
	case name == "sed":
		family = "sed"
	case contains([]string{"r", "tclsh", "erl", "elixir", "octave", "jshell", "fish", "csh", "tcsh"}, name):
		family = "opaque"
	default:
		return false
	}
	if family == "opaque" {
		return !safeOpaqueInterpreterInfo(name, args[1:])
	}
	if family == "sed" {
		return sedExecutionOpaque(args[1:])
	}
	return interpreterExecutesOpaqueCode(family, args[1:])
}

func safeOpaqueInterpreterInfo(name string, args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch name {
	case "r":
		return contains([]string{"--help", "--version", "-h", "-v", "-V"}, args[0])
	case "erl":
		return contains([]string{"-help", "-version"}, args[0])
	case "elixir", "octave", "jshell", "fish":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, args[0])
	default:
		return false
	}
}

func interpreterExecutesOpaqueCode(family string, args []string) bool {
	if len(args) == 1 && interpreterInfoOption(family, args[0]) {
		return false
	}
	if family == "awk" && len(args) == 2 && args[0] == "-W" && args[1] == "version" {
		return false
	}
	sourceFile := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-" {
			return true
		}
		if arg == "--" {
			if i+1 == len(args) {
				return true
			}
			return args[i+1] == "-" || family == "awk"
		}
		if !strings.HasPrefix(arg, "-") {
			if family == "awk" {
				return true // The first positional awk argument is source code.
			}
			if family == "python" {
				// shortcut: Python script-file contents remain an accepted residual boundary; upgrade to source inspection if that boundary changes.
				return false
			}
			return false // Ordinary script/source-file forms remain available.
		}
		if inlineInterpreterOption(family, arg) {
			return true
		}
		skip, known, scriptFile := interpreterOption(family, arg)
		if !known {
			return true // Unknown flags may select inline or stdin execution.
		}
		if skip && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
			if scriptFile {
				if family == "python" || family == "awk" || args[i] == "-" {
					return true
				}
				sourceFile = true
			}
		}
	}
	// With no file or code option, these interpreters consume code from stdin.
	return !sourceFile
}

func interpreterInfoOption(family, arg string) bool {
	switch family {
	case "python":
		return contains([]string{"--help", "-h", "--version", "-V", "--help-env", "--help-xoptions", "--help-all"}, arg)
	case "perl":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "ruby":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "node":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "osascript":
		return arg == "-h"
	case "awk":
		return contains([]string{"--help", "-h", "--version"}, arg)
	case "php":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "lua":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "rscript":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "gdb":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "expect":
		return contains([]string{"--help", "-h", "--version", "-v", "-V"}, arg)
	case "powershell":
		return contains([]string{"-help", "-?", "-version"}, arg)
	case "guile":
		return contains([]string{"--help", "-h", "--version"}, arg)
	case "clisp":
		return contains([]string{"--help", "-h", "--version"}, arg)
	default:
		return false
	}
}

func inlineInterpreterOption(family, arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch family {
	case "python":
		if strings.HasPrefix(arg, "-m") && len(arg) > 2 {
			return true
		}
		if strings.HasPrefix(arg, "--") {
			return name == "--command"
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		flags := strings.TrimPrefix(arg, "-")
		for _, flag := range flags {
			if flag == 'c' {
				return true
			}
			if flag == 'W' || flag == 'X' {
				return false
			}
		}
	case "perl":
		return perlOptionHasEval(arg)
	case "ruby":
		if name == "--eval" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			for _, flag := range strings.TrimPrefix(arg, "-") {
				if flag == 'e' {
					return true
				}
				if strings.ContainsRune("IErFKC", flag) {
					return false
				}
			}
		}
	case "node":
		return name == "--eval" || name == "--print" || arg == "-e" || arg == "-p" || strings.HasPrefix(arg, "-e") || strings.HasPrefix(arg, "-p")
	case "osascript":
		return strings.HasPrefix(arg, "-e")
	case "awk":
		return strings.HasPrefix(arg, "-e") || arg == "--source" || strings.HasPrefix(arg, "--source=")
	case "php":
		return arg == "-r" || strings.HasPrefix(arg, "-r") || arg == "-R" || strings.HasPrefix(arg, "-R") || arg == "-B" || strings.HasPrefix(arg, "-B")
	case "lua":
		return arg == "-e" || strings.HasPrefix(arg, "-e") || name == "--execute"
	case "rscript":
		return arg == "-e" || strings.HasPrefix(arg, "-e") || name == "--expression"
	case "gdb":
		return strings.HasPrefix(arg, "-ex") || strings.HasPrefix(arg, "--ex=") || arg == "--ex" || arg == "-eval-command" || strings.HasPrefix(arg, "--eval-command")
	case "expect":
		return arg == "-c" || strings.HasPrefix(arg, "-c")
	case "powershell":
		lower := strings.ToLower(arg)
		return lower == "-c" || strings.HasPrefix(lower, "-command") || strings.HasPrefix(lower, "-c:")
	case "guile":
		return strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "-e") || arg == "--command" || arg == "--eval" || strings.HasPrefix(arg, "--command=") || strings.HasPrefix(arg, "--eval=")
	case "clisp":
		return arg == "-x" || strings.HasPrefix(arg, "-x") || arg == "-eval" || arg == "-execute" || strings.HasPrefix(arg, "-eval=") || strings.HasPrefix(arg, "-execute=")
	case "sed":
		return false
	}
	return false
}

// interpreterOption returns (takes following argv, recognized, is script file).
func interpreterOption(family, arg string) (bool, bool, bool) {
	name, _, hasValue := strings.Cut(arg, "=")
	if hasValue {
		return false, true, false
	}
	switch family {
	case "python":
		if contains([]string{"-W", "-X", "--check-hash-based-pycs"}, arg) {
			return true, true, false
		}
		if arg == "-m" {
			return true, true, true
		}
		if strings.HasPrefix(arg, "-m") && len(arg) > 2 {
			return false, true, true
		}
		if strings.HasPrefix(arg, "--") {
			return contains([]string{"--help", "--help-env", "--help-xoptions", "--help-all", "--version", "--check-hash-based-pycs"}, arg), isPythonLongOption(arg), false
		}
		if strings.HasPrefix(arg, "-") {
			flags := strings.TrimPrefix(arg, "-")
			for index, flag := range flags {
				if strings.ContainsRune("WX", flag) {
					return index == len(flags)-1, true, false
				}
				if !strings.ContainsRune("abBdiEhiIOqsu vV", flag) {
					return false, false, false
				}
			}
			return false, true, false
		}
	case "perl":
		if contains([]string{"-I", "-M", "-m", "-F"}, arg) {
			return true, true, false
		}
		return false, perlOptionKnown(arg), false
	case "ruby":
		if contains([]string{"-I", "-r", "-E", "--encoding", "-F", "-K", "-C"}, arg) {
			return true, true, false
		}
		return false, rubyOptionKnown(arg), false
	case "node":
		if nodeValueOption(name) {
			return true, true, false
		}
		return false, nodeOptionKnown(name), false
	case "osascript":
		return arg == "-l", arg == "-l" || arg == "-e", false
	case "awk":
		if contains([]string{"-F", "-v"}, arg) {
			return true, true, false
		}
		if arg == "-f" {
			return true, true, true
		}
		if arg == "-W" {
			return true, true, false
		}
		return false, awkOptionKnown(arg), false
	case "php":
		if contains([]string{"-d", "-z", "-c"}, arg) {
			return true, true, false
		}
		if arg == "-f" {
			return true, true, true
		}
		return false, phpOptionKnown(arg), false
	case "lua":
		if arg == "-l" {
			return true, true, false
		}
		return false, contains([]string{"-i", "-v", "--version"}, arg), false
	case "rscript":
		if arg == "-e" || arg == "--expression" {
			return true, true, false
		}
		if arg == "-f" {
			return true, true, true
		}
		return false, contains([]string{"--vanilla", "--default-packages", "--save", "--no-save", "--no-restore", "--verbose", "--quiet", "--slave", "--interactive"}, arg) || strings.HasPrefix(arg, "--default-packages="), false
	case "gdb":
		if gdbOptionTakesValue(arg) {
			return true, true, strings.Contains(arg, "command") && arg != "-ex" && arg != "--ex" && arg != "-eval-command" && arg != "--eval-command"
		}
		return false, strings.HasPrefix(arg, "-"), false
	case "expect":
		if arg == "-f" {
			return true, true, true
		}
		return false, contains([]string{"-c", "-d", "-i", "-n", "-N", "-v"}, arg), false
	case "powershell":
		if powershellTakesValue(arg) {
			return true, true, strings.EqualFold(arg, "-File")
		}
		return false, strings.HasPrefix(arg, "-"), false
	case "guile":
		if contains([]string{"-l", "--load-path", "-L"}, arg) {
			return true, true, false
		}
		if arg == "-s" {
			return true, true, true
		}
		return false, strings.HasPrefix(arg, "-"), false
	case "clisp":
		if arg == "-i" {
			return true, true, true
		}
		return false, strings.HasPrefix(arg, "-"), false
	}
	return false, false, false
}

func sedExecutionOpaque(args []string) bool {
	if len(args) == 1 && contains([]string{"--help", "-h", "--version"}, args[0]) {
		return false
	}
	var scripts []string
	scriptSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-f" || arg == "--file" || strings.HasPrefix(arg, "-f") && !strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "--file="):
			return true
		case arg == "-e" || arg == "--expression":
			if i+1 >= len(args) {
				return true
			}
			i++
			scripts = append(scripts, args[i])
			scriptSeen = true
		case strings.HasPrefix(arg, "--expression="):
			scripts = append(scripts, strings.TrimPrefix(arg, "--expression="))
			scriptSeen = true
		case strings.HasPrefix(arg, "-e") && len(arg) > 2:
			scripts = append(scripts, arg[2:])
			scriptSeen = true
		case arg == "--in-place" || strings.HasPrefix(arg, "--in-place=") || strings.HasPrefix(arg, "-i") && !strings.HasPrefix(arg, "--"):
		case contains([]string{"-n", "--quiet", "--silent", "-E", "-r", "--regexp-extended", "--posix", "--sandbox", "--follow-symlinks", "--debug", "-s", "--separate", "-u", "--unbuffered", "-z", "--null-data"}, arg):
		case arg == "--":
			if !scriptSeen {
				if i+1 >= len(args) {
					return true
				}
				scripts = append(scripts, args[i+1])
				scriptSeen = true
			}
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			return true
		default:
			if !scriptSeen {
				scripts = append(scripts, arg)
				scriptSeen = true
			}
		}
	}
	if len(scripts) == 0 {
		return true // No script means sed reads its program from stdin.
	}
	for _, script := range scripts {
		if !safeSedScript(script) {
			return true
		}
	}
	return false
}

func safeSedScript(script string) bool {
	if contains([]string{"p", "q", "d"}, script) {
		return true
	}
	if len(script) < 4 || script[0] != 's' {
		return false
	}
	delimiter := script[1]
	if delimiter == '\n' || delimiter == '\\' || delimiter >= '0' && delimiter <= '9' || delimiter == ' ' || delimiter == '\t' {
		return false
	}
	index := 2
	for part := 0; part < 2; part++ {
		for index < len(script) {
			if script[index] == '\\' {
				index += 2
				continue
			}
			if script[index] == delimiter {
				index++
				break
			}
			index++
		}
		if index > len(script) || index == len(script) && (len(script) == 0 || script[len(script)-1] != delimiter) {
			return false
		}
	}
	for _, flag := range script[index:] {
		if !strings.ContainsRune("gIp0123456789", flag) {
			return false
		}
	}
	return true
}

func isPythonLongOption(arg string) bool {
	return contains([]string{"--help", "--help-env", "--help-xoptions", "--help-all", "--version", "--check-hash-based-pycs"}, arg)
}

func perlOptionHasEval(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	flags := arg[1:]
	for i := 0; i < len(flags); i++ {
		flag := flags[i]
		if flag == 'e' || flag == 'E' {
			return true
		}
		if flag == '0' {
			for i+1 < len(flags) && flags[i+1] >= '0' && flags[i+1] <= '7' {
				i++
			}
			continue
		}
		if strings.ContainsRune("IMmFDi x", rune(flag)) {
			return false
		}
		if !strings.ContainsRune("acdl npsStTuUvVwWx", rune(flag)) {
			return false
		}
	}
	return false
}

func perlOptionKnown(arg string) bool {
	if strings.HasPrefix(arg, "-0") {
		return true
	}
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
		return false
	}
	for _, flag := range arg[1:] {
		if !strings.ContainsRune("acdl npsStTuUvVwWx", flag) {
			return false
		}
	}
	return true
}

func rubyOptionKnown(arg string) bool {
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	for _, flag := range arg[1:] {
		if !strings.ContainsRune("acdlnpsSTvwWx", flag) {
			return false
		}
	}
	return true
}

func nodeValueOption(option string) bool {
	return contains([]string{"-r", "--require", "--import", "--loader", "--experimental-loader", "--conditions", "--inspect-port", "--icu-data-dir", "--openssl-config", "--redirect-warnings", "--title", "--max-old-space-size", "--max-semi-space-size", "--stack-size", "--diagnostic-dir", "--heapsnapshot-signal", "--watch-path", "--trace-event-categories", "--trace-event-file-pattern", "--experimental-policy", "--policy-integrity", "--input-type"}, option)
}

func nodeOptionKnown(option string) bool {
	if strings.HasPrefix(option, "--") {
		return contains([]string{"--help", "--version", "--v8-options", "--check", "--preserve-symlinks", "--preserve-symlinks-main", "--no-warnings", "--trace-warnings", "--throw-deprecation", "--no-deprecation", "--pending-deprecation", "--experimental-modules", "--input-type", "--watch", "--test", "--run", "--enable-source-maps", "--experimental-strip-types"}, option) || nodeValueOption(option)
	}
	return contains([]string{"-h", "-v", "-V", "-p", "-i", "-r"}, option)
}

func awkOptionKnown(arg string) bool {
	return contains([]string{"-b", "-c", "-d", "-g", "-h", "-L", "-M", "-o", "-p", "-s", "-t", "-V", "--help", "--version", "--lint", "--posix", "--traditional"}, arg) || strings.HasPrefix(arg, "--lint=")
}

func phpOptionKnown(arg string) bool {
	return contains([]string{"-a", "-c", "-d", "-f", "-h", "-i", "-l", "-m", "-n", "-q", "-s", "-v", "-w", "-z", "--help", "--version"}, arg)
}

func gdbOptionTakesValue(arg string) bool {
	return contains([]string{"-x", "--command", "-ex", "--ex", "-eval-command", "--eval-command", "-ix", "--init-command", "-iex", "--init-eval-command", "--directory", "-d", "--data-directory", "-cd", "--cd", "-p", "--pid", "-b", "--baud"}, arg)
}

func powershellTakesValue(arg string) bool {
	switch strings.ToLower(arg) {
	case "-file", "-executionpolicy", "-inputformat", "-outputformat", "-configurationname", "-workingdirectory", "-windowstyle", "-encodedcommand", "-encodedarguments", "-version":
		return true
	default:
		return false
	}
}

func isVersionedInterpreter(name, base string) bool {
	suffix := strings.TrimPrefix(name, base)
	if suffix == name || suffix == "" {
		return false
	}
	suffix = strings.TrimSuffix(suffix, "t")
	if suffix == "" || suffix[0] < '0' || suffix[0] > '9' {
		return false
	}
	for _, char := range suffix {
		if (char < '0' || char > '9') && char != '.' {
			return false
		}
	}
	return true
}

func isRecipeRunner(name string) bool {
	// This list is defense-in-depth, not an exhaustive command allowlist; the
	// literal governed-command argv backstop handles unknown prefixes.
	switch name {
	case "make", "gmake", "rake", "just", "task", "npm", "npx", "yarn", "pnpm", "bun", "bunx", "gradle", "gradlew", "ant", "mvn", "ninja", "cargo", "uv", "uvx", "tox", "poetry", "pipenv", "pip", "pipx", "cmake", "deno":
		return true
	default:
		return false
	}
}

func isInformationalCommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	for _, arg := range args[1:] {
		if !contains([]string{"--help", "--version", "-h", "-v", "-V"}, arg) {
			return false
		}
	}
	return true
}

func goExecutionOpaque(args []string) bool {
	command, index, ok := goSubcommand(args)
	if !ok {
		return false
	}
	for _, arg := range args[index+1:] {
		option, _, _ := strings.Cut(arg, "=")
		option = strings.TrimLeft(option, "-") // Go accepts one or two leading dashes.
		if (command == "build" && option == "toolexec") || (command == "vet" && option == "vettool") || (command == "env" && option == "w") {
			return true
		}
	}
	return contains([]string{"test", "run", "generate", "tool"}, command)
}

func goSubcommand(args []string) (string, int, bool) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "-C" {
			if i+1 >= len(args) {
				return "", 0, false
			}
			i++
			continue
		}
		if strings.HasPrefix(arg, "-C=") || strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, i, true
	}
	return "", 0, false
}

func isOpaqueExecutionWrapper(name string) bool {
	return contains([]string{"flock", "watch", "script", "tmux", "screen", "parallel", "su", "ssh", "strace", "valgrind", "taskset", "runuser", "doas", "ionice", "ltrace", "chrt", "unshare", "perf", "nsenter", "systemd-run", "docker", "podman", "at", "batch", "chronic", "runcon"}, name)
}

func isOpaqueWrapperInvocation(name string, args []string) bool {
	if isInformationalCommand(args) {
		return false
	}
	switch name {
	case "flock":
		return flockHasCommand(args[1:])
	case "watch", "script", "parallel", "su", "ssh":
		return true
	case "tmux":
		return containsAny(args[1:], "new", "new-session", "neww", "new-window", "respawn-pane", "respawn-window", "run-shell", "pipe-pane", "send-keys")
	case "screen":
		return containsAny(args[1:], "-X", "-c", "-dm", "-dmS", "-D", "-S")
	case "strace":
		return straceCommandPresent(args[1:])
	case "valgrind", "taskset", "runuser", "doas", "ionice", "ltrace", "chrt", "unshare", "perf", "nsenter", "systemd-run", "docker", "podman", "at", "batch", "chronic", "runcon":
		return true
	default:
		return false
	}
}

func flockHasCommand(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-c" || arg == "--command" || strings.HasPrefix(arg, "--command=") {
			return true
		}
		if arg == "--" {
			return i+2 < len(args)
		}
		if strings.HasPrefix(arg, "-") {
			if contains([]string{"-w", "-E", "--wait", "--conflict-exit-code"}, arg) {
				i++
			}
			continue
		}
		return i+1 < len(args) // The first positional is the lock file.
	}
	return false
}

func straceCommandPresent(args []string) bool {
	valueOptions := map[string]bool{"-e": true, "--expr": true, "--expression": true, "-o": true, "--output": true, "-p": true, "--attach": true, "-s": true, "--string-limit": true, "-u": true, "--user": true, "-E": true, "--env": true, "-P": true, "--status": true, "-O": true, "--summary-sort-by": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return i+1 < len(args)
		}
		if !strings.HasPrefix(arg, "-") {
			return true
		}
		option, _, hasValue := strings.Cut(arg, "=")
		if valueOptions[option] && !hasValue {
			i++
		}
	}
	return false
}

func containsAny(values []string, wanted ...string) bool {
	for _, value := range values {
		if contains(wanted, value) {
			return true
		}
	}
	return false
}

func hasGitExecutionEnvironmentOverride(words []string) bool {
	// An assignment of a Git execution key anywhere in the segment makes the
	// child's behavior uninspectable, including when a wrapper such as
	// timeout, stdbuf, or env precedes the assignment.
	for _, word := range words {
		if gitConfigAssignment(word) {
			return true
		}
	}
	return false
}

func gitConfigAssignment(word string) bool {
	key, _, assignment := strings.Cut(word, "=")
	return assignment && gitExecutionEnvironmentKey(key)
}

func gitInvocationOpaque(args []string) bool {
	if len(args) == 2 && contains([]string{"--help", "--version", "-h", "-v", "-V"}, args[1]) {
		return false
	}
	command, index, ok := gitSubcommand(args)
	if !ok {
		return true
	}
	if gitConfiguresExecutableConfig(args, index) {
		return true
	}
	if command == "config" && gitConfigCommandWritesExecutableConfig(args[index+1:]) {
		return true
	}
	if gitSubcommandCanExecuteCommands(command, args[index+1:]) {
		return true
	}
	return !gitBuiltins[command]
}

func gitSubcommandCanExecuteCommands(command string, args []string) bool {
	switch command {
	case "bisect":
		return contains(args, "run")
	case "difftool", "mergetool", "credential":
		return true
	case "branch", "remote", "tag", "worktree", "stash", "reflog", "submodule":
		return !gitReadOnlyMetadataCommand(command, args)
	case "add", "am", "apply", "checkout", "cherry-pick", "clean", "clone", "commit", "fetch", "filter-branch", "init", "merge", "mv", "notes", "pull", "push", "rebase", "reset", "restore", "revert", "rm", "switch", "update-index", "update-ref", "gc", "maintenance", "repack", "prune", "symbolic-ref":
		return true
	default:
		return false
	}
}

func gitReadOnlyMetadataCommand(command string, args []string) bool {
	switch command {
	case "branch":
		return len(args) == 0 || len(args) == 1 && contains([]string{"--list", "-l", "--show-current", "--show-ref"}, args[0])
	case "remote":
		return len(args) == 0 || len(args) == 1 && contains([]string{"-v", "--verbose"}, args[0]) || len(args) == 2 && args[0] == "get-url"
	case "tag":
		return len(args) == 0 || len(args) == 1 && contains([]string{"-l", "--list"}, args[0])
	case "worktree":
		return len(args) == 1 && args[0] == "list"
	case "stash":
		return len(args) == 1 && args[0] == "list"
	case "reflog":
		return len(args) == 0 || len(args) == 1 && args[0] == "show"
	case "submodule":
		return len(args) > 0 && args[0] == "status"
	default:
		return false
	}
}

func gitSubcommand(args []string) (string, int, bool) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1], i + 1, true
			}
			return "", 0, false
		}
		if !strings.HasPrefix(arg, "-") {
			return arg, i, true
		}
		option, _, hasValue := strings.Cut(arg, "=")
		if gitGlobalValueOption(option) && !hasValue {
			i++
			if i >= len(args) {
				return "", 0, false
			}
			continue
		}
		if gitGlobalFlag(option) || hasValue || (strings.HasPrefix(arg, "-c") && len(arg) > 2) || (strings.HasPrefix(arg, "-C") && len(arg) > 2) {
			continue
		}
		return "", 0, false
	}
	return "", 0, false
}

func gitGlobalValueOption(option string) bool {
	return contains([]string{"-C", "-c", "--config-env", "--exec-path", "--git-dir", "--work-tree", "--namespace", "--super-prefix"}, option)
}

func gitGlobalFlag(option string) bool {
	return contains([]string{"--no-pager", "--paginate", "--no-optional-locks", "--bare", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--no-replace-objects", "--no-lazy-fetch", "--html-path", "--man-path", "--info-path", "--help", "--version"}, option)
}

func gitConfiguresExecutableConfig(args []string, commandIndex int) bool {
	for i := 1; i < commandIndex; i++ {
		arg := args[i]
		var config string
		switch {
		case arg == "-c" || arg == "--config-env":
			i++
			if i >= commandIndex {
				return true
			}
			config = args[i]
		case strings.HasPrefix(arg, "-c") && len(arg) > 2:
			config = arg[2:]
		case strings.HasPrefix(arg, "--config-env="):
			config = strings.TrimPrefix(arg, "--config-env=")
		}
		key, _, _ := strings.Cut(config, "=")
		if gitExecutionConfigKey(key) {
			return true
		}
	}
	return false
}

func gitConfigCommandWritesExecutableConfig(args []string) bool {
	var positionals []string
	write, readOnly := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		option, _, hasValue := strings.Cut(arg, "=")
		switch option {
		case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--name-only", "--show-origin", "--show-scope":
			readOnly = true
		case "--unset-regexp":
			if i+1 < len(args) && gitExecutionConfigPattern(args[i+1]) {
				return true
			}
			write = true
		case "--add", "--replace-all", "--unset", "--unset-all", "--rename-section", "--remove-section", "--edit", "-e":
			if option == "--edit" || option == "-e" {
				return true
			}
			if hasValue && strings.Contains(strings.ToLower(arg), "alias") {
				return true
			}
			write = true
		case "--file", "-f", "--type", "--bool", "--int", "--bool-or-int", "--path", "--expiry-date":
			if !hasValue {
				i++
			}
		case "--fixed-value", "--local", "--global", "--system", "--worktree", "--includes", "--no-includes", "--null", "-z":
		default:
			// Unknown config options that mention an execution key are not safe to infer.
			if gitExecutionConfigPattern(arg) {
				return true
			}
		}
	}
	for _, value := range positionals {
		if gitExecutionConfigSection(value) {
			return true // Section removal/rename can change executable settings.
		}
		if gitExecutionConfigKey(value) {
			if write || (!readOnly && len(positionals) > 1) {
				return true
			}
		}
	}
	return false
}

func gitExecutionConfigKey(key string) bool {
	key = strings.ToLower(key)
	for _, prefix := range []string{"alias.", "pager.", "difftool.", "mergetool.", "filter.", "credential.", "diff."} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	switch key {
	case "core.pager", "core.editor", "core.hookspath", "core.fsmonitor", "core.sshcommand", "core.askpass", "core.gitproxy", "sequence.editor", "interactive.difffilter":
		return true
	default:
		return false
	}
}

func gitExecutionConfigSection(section string) bool {
	section = strings.ToLower(section)
	return contains([]string{"alias", "pager", "difftool", "mergetool", "filter", "credential", "diff", "core", "sequence", "interactive"}, section)
}

func gitExecutionConfigPattern(pattern string) bool {
	pattern = strings.ToLower(pattern)
	for _, name := range []string{"alias", "pager", "difftool", "mergetool", "filter", "credential", "diff", "core", "sequence", "interactive"} {
		if strings.Contains(pattern, name) {
			return true
		}
	}
	return false
}

func shellExportsGitExecutionEnvironment(words []string) bool {
	if len(words) == 0 {
		return false
	}
	switch filepath.Base(words[0]) {
	case "export", "declare", "typeset", "readonly":
	default:
		return false
	}
	for _, word := range words[1:] {
		key, _, _ := strings.Cut(strings.TrimLeft(word, "-"), "=")
		if gitExecutionEnvironmentKey(key) {
			return true
		}
	}
	return false
}

func gitExecutionEnvironmentKey(key string) bool {
	key = strings.ToUpper(key)
	return strings.HasPrefix(key, "GIT_CONFIG_") || contains([]string{"GIT_PAGER", "PAGER", "GIT_EDITOR", "EDITOR", "VISUAL", "GIT_SEQUENCE_EDITOR", "GIT_EXTERNAL_DIFF", "GIT_EXTERNAL_DIFF_TRUST_EXIT_CODE"}, key)
}

var gitBuiltins = func() map[string]bool {
	result := make(map[string]bool)
	for _, command := range strings.Fields(`add am annotate apply archive bisect blame branch bugreport bundle cat-file check-attr check-ignore check-mailmap check-ref-format checkout checkout-index cherry cherry-pick citool clean clone column commit commit-tree config count-objects credential credential-cache credential-store describe diagnose diff diff-files diff-index diff-tree difftool fast-export fast-import fetch fetch-pack filter-branch fmt-merge-msg for-each-ref format-patch fsck gc get-tar-commit-id grep hash-object help http-backend http-fetch http-push imap-send index-pack init instaweb interpret-trailers log ls-files ls-remote ls-tree mailinfo mailsplit merge merge-base merge-file merge-index merge-ours merge-recursive merge-resolve merge-subtree mergetool mktag mktree mv name-rev notes pack-objects pack-redundant pack-refs patch-id prune prune-packed pull push quiltimport read-tree rebase receive-pack reflog remote repack replace request-pull rerere reset restore rev-list rev-parse revert rm send-pack shortlog show show-branch show-index show-ref sparse-checkout stage stash status stripspace submodule switch symbolic-ref tag unpack-file unpack-objects update-index update-ref update-server-info upload-archive upload-pack var verify-commit verify-pack verify-tag version whatchanged worktree write-tree maintenance range-diff scalar`) {
		result[command] = true
	}
	return result
}()
