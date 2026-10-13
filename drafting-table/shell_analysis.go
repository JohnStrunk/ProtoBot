package guard

import (
	"path/filepath"
	"regexp"
	"strings"
)

const quotedControlWordPrefix = "\x00quoted-control:"

var literalGovernedWritePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9_./-])ears-manager([ \t]+--output(([ \t]+|=)(json|human))?)*[ \t]+(project[ \t]+init|artifact[ \t]+put|interface[ \t]+(add|update)|requirement[ \t]+(add|update|retire)|change-set[ \t]+(create|update))([^a-z0-9_-]|$)`)

func inspectOtherShell(command string, depth int) ([]string, bool, bool) {
	if depth > maxShellAnalysisDepth || len(command) > maxShellCommandBytes || !otherShellSyntaxInspectable(command) {
		return nil, false, true
	}
	targets, unsafe := parseOtherShellRedirectsAtDepth(command, depth)
	if unsafe {
		return nil, false, true
	}
	changesDirectory := false
	for _, words := range shellCommandSegments(command) {
		if opaqueShellCommand(words) {
			return nil, false, true
		}
		args := unwrapCommand(words)
		if len(args) == 0 {
			continue
		}
		base := filepath.Base(args[0])
		if base == "eval" || base == "source" || base == "." {
			return nil, false, true
		}
		if wrapperChangesDirectory(words) {
			changesDirectory = true
		}
		if base == "cd" || base == "pushd" || base == "popd" || base == "source" || base == "." {
			changesDirectory = true
		}
		nested, safe := nestedShellCommandsAtDepth(words, depth)
		if !safe {
			return nil, false, true
		}
		for _, child := range nested {
			nestedTargets, nestedChangesDirectory, nestedUnsafe := inspectOtherShell(child.script, depth+1)
			if nestedUnsafe {
				return nil, false, true
			}
			targets = append(targets, nestedTargets...)
			changesDirectory = changesDirectory || nestedChangesDirectory
			if child.execDir && len(nestedTargets) > 0 {
				changesDirectory = true
			}
		}
	}
	return targets, changesDirectory, false
}

func wrapperChangesDirectory(words []string) bool {
	args := append([]string(nil), words...)
	for len(args) > 0 {
		switch filepath.Base(args[0]) {
		case "env":
			i := 1
			for i < len(args) {
				option := args[i]
				if option == "-C" || option == "--chdir" || strings.HasPrefix(option, "--chdir=") || (strings.HasPrefix(option, "-C") && len(option) > 2) {
					return true
				}
				if option == "-u" || option == "--unset" {
					i += 2
					continue
				}
				if option == "--" {
					i++
					break
				}
				if strings.HasPrefix(option, "-") || assignmentWord.MatchString(option) {
					i++
					continue
				}
				break
			}
			if i >= len(args) {
				return false
			}
			args = args[i:]
		case "sudo":
			i := 1
			for i < len(args) && strings.HasPrefix(args[i], "-") {
				option := args[i]
				if option == "-D" || option == "--chdir" || strings.HasPrefix(option, "--chdir=") || (strings.HasPrefix(option, "-D") && len(option) > 2) {
					return true
				}
				i++
				if (option == "-u" || option == "-g" || option == "-h" || option == "-p" || option == "-C" || option == "-T") && i < len(args) {
					i++
				}
			}
			if i >= len(args) {
				return false
			}
			args = args[i:]
		default:
			next := unwrapCommand(args)
			if len(next) == 0 || len(next) == len(args) {
				return false
			}
			args = next
		}
	}
	return false
}

type nestedShellScript struct {
	script  string
	execDir bool
}

type findAction struct {
	args    []string
	execDir bool
}

func nestedShellCommands(words []string) ([]nestedShellScript, bool) {
	return nestedShellCommandsAtDepth(words, 0)
}

func nestedShellCommandsAtDepth(words []string, depth int) ([]nestedShellScript, bool) {
	if depth > maxShellAnalysisDepth {
		return nil, false
	}
	if len(words) == 0 {
		return nil, true
	}
	name := filepath.Base(words[0])
	if name == "env" {
		_, found, safe := envSplitString(words)
		if !safe {
			return nil, false
		}
		if found {
			return nil, false // env -S has its own argv splitting rules.
		}
		unwrapped := unwrapCommand(words)
		if len(unwrapped) == 0 || sameWords(words, unwrapped) {
			return nil, false
		}
		return nestedShellCommandsAtDepth(unwrapped, depth+1)
	}
	if isShellInterpreter(name) {
		script, found, safe := shellCommandString(words)
		if !safe {
			return nil, false
		}
		if found {
			return []nestedShellScript{{script: script}}, true
		}
		return nil, true
	}
	if name == "find" {
		actions, safe := findActions(words)
		if !safe {
			return nil, false
		}
		var result []nestedShellScript
		for _, action := range actions {
			children, childSafe := nestedShellCommandsAtDepth(action.args, depth+1)
			if !childSafe {
				return nil, false
			}
			for _, child := range children {
				child.execDir = child.execDir || action.execDir
				result = append(result, child)
			}
		}
		return result, true
	}
	if name == "xargs" {
		command, found, safe := xargsCommand(words)
		if !safe {
			return nil, false
		}
		if !found {
			return nil, true // xargs defaults to echo when no command is given.
		}
		return nestedShellCommandsAtDepth(command, depth+1)
	}
	unwrapped := unwrapCommand(words)
	if len(unwrapped) == 0 {
		return nil, false
	}
	if !sameWords(words, unwrapped) {
		return nestedShellCommandsAtDepth(unwrapped, depth+1)
	}
	return nil, true
}

func sameWords(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func opaqueShellCommand(words []string) bool {
	if len(words) == 0 {
		return false
	}
	if shellStartupEnvironmentAssignment(words) {
		return true
	}
	args := unwrapCommand(stripShellControlPrefix(words))
	args = stripShellControlPrefix(args)
	if len(args) == 0 {
		return false
	}
	switch filepath.Base(args[0]) {
	case "eval", "source", ".", "trap", "coproc", "alias", "unalias", "shopt", "function":
		return true
	case "enable":
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "-") && strings.Contains(strings.TrimLeft(arg, "-"), "f") {
				return true
			}
		}
	case "set":
		return shellEnablesAllexport(args[1:])
	}
	return false
}

func shellStartupEnvironmentAssignment(words []string) bool {
	keys := []string{
		"BASH_ENV", "ENV", "ZDOTDIR", "SHELLOPTS", "BASHOPTS", "PROMPT_COMMAND",
		"LD_PRELOAD", "LD_AUDIT", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
		"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PERL5OPT", "PERL5LIB",
		"RUBYOPT", "RUBYLIB", "NODE_OPTIONS", "NODE_PATH",
	}
	for _, word := range words {
		key, _, assigned := strings.Cut(strings.TrimLeft(word, "-"), "=")
		if assigned && contains(keys, key) {
			return true
		}
	}
	return false
}

func shellEnablesAllexport(args []string) bool {
	for index, arg := range args {
		if arg == "-a" || arg == "--allexport" || arg == "-oallexport" {
			return true
		}
		if arg == "-o" && index+1 < len(args) && args[index+1] == "allexport" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(strings.TrimLeft(arg, "-"), "a") {
			return true
		}
	}
	return false
}

func envSplitString(args []string) (string, bool, bool) {
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-S" || args[i] == "--split-string":
			if i+1 >= len(args) {
				return "", false, false
			}
			return args[i+1], true, true
		case strings.HasPrefix(args[i], "--split-string="):
			return strings.TrimPrefix(args[i], "--split-string="), true, true
		}
	}
	return "", false, true
}

func shellCommandString(args []string) (string, bool, bool) {
	for i := 1; i < len(args); {
		arg := args[i]
		if arg == "--help" || arg == "--version" {
			return "", false, true
		}
		if arg == "--" {
			return "", false, false
		}
		if strings.HasPrefix(arg, "--") {
			option, optionValue, hasValue := strings.Cut(arg, "=")
			switch option {
			case "--noprofile", "--norc", "--posix", "--restricted", "--verbose", "--noediting", "--dump-po-strings", "--dump-strings":
				if hasValue {
					return "", false, false
				}
				i++
			case "--rcfile", "--init-file":
				if !hasValue {
					if i+1 >= len(args) {
						return "", false, false
					}
					optionValue = args[i+1]
					i += 2
				} else {
					i++
				}
				if optionValue != "/dev/null" {
					return "", false, false
				}
			case "--encoding":
				if !hasValue {
					if i+1 >= len(args) {
						return "", false, false
					}
					i += 2
				} else {
					i++
				}
			default:
				return "", false, false
			}
			continue
		}
		if strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "+") {
			flags := arg[1:]
			if flags == "o" || flags == "O" {
				if i+1 >= len(args) {
					return "", false, false
				}
				i += 2
				continue
			}
			if strings.HasPrefix(flags, "o") || strings.HasPrefix(flags, "O") {
				i++ // Option value is attached to -o or -O.
				continue
			}
			if strings.ContainsRune(flags, 'c') {
				if strings.LastIndexByte(flags, 'c') != len(flags)-1 || strings.Count(flags, "c") != 1 {
					return "", false, false
				}
				if i+1 >= len(args) {
					return "", false, false
				}
				return args[i+1], true, true
			}
			if !knownShellFlags(flags) {
				return "", false, false
			}
			i++
			continue
		}
		// A script file or positional command source is not inspectable here.
		return "", false, false
	}
	// A bare shell reads commands from stdin, so its input source is unknown.
	return "", false, false
}

func knownShellFlags(flags string) bool {
	for _, flag := range flags {
		if !strings.ContainsRune("abefhiklmnpqrtuvxBCEHPT", flag) {
			return false
		}
	}
	return flags != ""
}

func findActions(args []string) ([]findAction, bool) {
	var actions []findAction
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-exec", "-execdir", "-ok", "-okdir":
			end := i + 1
			for end < len(args) && args[end] != ";" && args[end] != "+" {
				end++
			}
			if end >= len(args) || end == i+1 {
				return nil, false
			}
			actions = append(actions, findAction{
				args:    append([]string(nil), args[i+1:end]...),
				execDir: args[i] == "-execdir" || args[i] == "-okdir",
			})
			i = end
		}
	}
	return actions, true
}

func xargsCommand(args []string) ([]string, bool, bool) {
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 == len(args) {
				return nil, false, true
			}
			return append([]string(nil), args[i+1:]...), true, true
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return append([]string(nil), args[i:]...), true, true
		}
		option, _, hasValue := strings.Cut(arg, "=")
		if !hasValue && isAttachedXargsValue(arg) {
			continue
		}
		switch option {
		case "-0", "--null", "-r", "--no-run-if-empty", "-x", "--exit", "-t", "--verbose", "-p", "--interactive", "--show-limits":
			if hasValue {
				return nil, false, false
			}
		case "-a", "--arg-file", "-d", "--delimiter", "-E", "-e", "--eof", "-I", "--replace", "-J", "-L", "-l", "--max-lines", "-n", "--max-args", "-P", "--max-procs", "-s", "--max-chars":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, false, false
				}
				i++
			}
		default:
			return nil, false, false
		}
	}
	return nil, false, true
}

func isAttachedXargsValue(option string) bool {
	for _, prefix := range []string{"-a", "-d", "-E", "-e", "-I", "-J", "-L", "-l", "-n", "-P", "-s"} {
		if strings.HasPrefix(option, prefix) && len(option) > len(prefix) {
			return true
		}
	}
	return false
}

type governedExecution uint8

const (
	governedExecutionNone governedExecution = iota
	governedExecutionExplicitWrite
	governedExecutionOpaque
)

func invokesGovernedWrite(command string) governedExecution {
	return invokesGovernedWriteAtDepth(command, 0)
}

func invokesGovernedWriteAtDepth(command string, depth int) governedExecution {
	if depth > maxShellAnalysisDepth || len(command) > maxShellCommandBytes || !otherShellSyntaxInspectable(command) {
		return governedExecutionOpaque
	}
	finding := governedExecutionNone
	for _, words := range shellCommandSegments(command) {
		current := governedWordsWriteAtDepth(words, depth)
		if current == governedExecutionExplicitWrite {
			return current
		}
		if current == governedExecutionOpaque {
			finding = current
		}
	}
	return finding
}

func shellCommandSegments(command string) [][]string {
	var segments [][]string
	var words []string
	var word strings.Builder
	started := false
	wordQuoted := false
	quote := byte(0)
	flushWord := func() {
		if started {
			value := word.String()
			if wordQuoted && isShellControlWord(value) {
				value = quotedControlWordPrefix + value
			}
			words = append(words, value)
			word.Reset()
			started = false
			wordQuoted = false
		}
	}
	flushSegment := func() {
		flushWord()
		if len(words) != 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	for i := 0; i < len(command); i++ {
		char := command[i]
		if quote == '\'' {
			if char == '\'' {
				quote = 0
			} else {
				word.WriteByte(char)
			}
			continue
		}
		if quote == '"' {
			switch char {
			case '"':
				quote = 0
			case '\\':
				if i+1 < len(command) {
					i++
					word.WriteByte(command[i])
				}
			default:
				word.WriteByte(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			started = true
			wordQuoted = true
		case '\\':
			if i+1 < len(command) {
				i++
				word.WriteByte(command[i])
				started = true
				wordQuoted = true
			}
		case ' ', '\t', '\r':
			flushWord()
		case '\n', ';', '|', '&', '(', ')':
			flushSegment()
			for i+1 < len(command) && strings.ContainsRune(";|&", rune(command[i+1])) {
				i++
			}
		case '<':
			if i+1 < len(command) && command[i+1] == '<' {
				flushWord()
				tail, afterBody, ok := skipHereDocument(command, i)
				if !ok {
					flushSegment()
					return segments
				}
				segments = append(segments, shellCommandSegments(tail)...)
				i = afterBody - 1
			} else {
				flushWord()
			}
		case '>':
			flushWord()
		default:
			word.WriteByte(char)
			started = true
		}
	}
	flushSegment()
	return segments
}

func skipHereDocument(command string, operator int) (string, int, bool) {
	index := operator + 2
	stripTabs := false
	if index < len(command) && command[index] == '-' {
		stripTabs = true
		index++
	}
	for index < len(command) && (command[index] == ' ' || command[index] == '\t') {
		index++
	}
	quoted := byte(0)
	if index < len(command) && (command[index] == '\'' || command[index] == '"') {
		quoted = command[index]
		index++
	}
	start := index
	for index < len(command) {
		if quoted != 0 {
			if command[index] == quoted {
				break
			}
		} else if strings.ContainsRune(" \t\r\n;|&<>", rune(command[index])) {
			break
		}
		index++
	}
	delimiter := command[start:index]
	if delimiter == "" {
		return "", 0, false
	}
	if quoted != 0 {
		if index >= len(command) {
			return "", 0, false
		}
		index++
	}
	delimiterEnd := index
	lineBreak := strings.IndexByte(command[index:], '\n')
	if lineBreak < 0 {
		return "", 0, false
	}
	headerEnd := index + lineBreak
	lineStart := headerEnd + 1
	for lineStart <= len(command) {
		lineEnd := strings.IndexByte(command[lineStart:], '\n')
		end := len(command)
		if lineEnd >= 0 {
			end = lineStart + lineEnd
		}
		line := strings.TrimSuffix(command[lineStart:end], "\r")
		if stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delimiter {
			after := end
			if end == len(command) {
				return command[delimiterEnd:headerEnd], end, true
			}
			after++
			return command[delimiterEnd:headerEnd], after, true
		}
		if end == len(command) {
			break
		}
		lineStart = end + 1
	}
	return "", 0, false
}

func governedWordsWrite(words []string) governedExecution {
	return governedWordsWriteAtDepth(words, 0)
}

func governedWordsWriteAtDepth(words []string, depth int) governedExecution {
	if depth > maxShellAnalysisDepth {
		return governedExecutionOpaque
	}
	if len(words) == 0 {
		return governedExecutionNone
	}
	if opaqueShellCommand(words) {
		return governedExecutionOpaque
	}
	gitExecutionEnvironmentOverride := hasGitExecutionEnvironmentOverride(words)
	if gitExecutionEnvironmentOverride {
		return governedExecutionOpaque
	}
	words = stripShellControlPrefix(words)
	if len(words) == 0 {
		return governedExecutionNone
	}
	if shellExportsGitExecutionEnvironment(words) {
		return governedExecutionOpaque
	}
	if filepath.Base(words[0]) == "env" {
		_, found, safe := envSplitString(words)
		if !safe {
			return governedExecutionOpaque
		}
		if found {
			return governedExecutionOpaque
		}
	}
	args := unwrapCommand(words)
	if len(args) == 0 {
		return governedExecutionOpaque
	}
	name := filepath.Base(args[0])
	if opaqueGovernedExecution(name, args) {
		return governedExecutionOpaque
	}
	if isShellInterpreter(name) {
		script, found, safe := shellCommandString(args)
		if !safe {
			return governedExecutionOpaque
		}
		if !found {
			return governedExecutionNone
		}
		return invokesGovernedWriteAtDepth(script, depth+1)
	}
	if name == "find" {
		actions, safe := findActions(args)
		if !safe {
			return governedExecutionOpaque
		}
		finding := governedExecutionNone
		for _, action := range actions {
			current := governedWordsWriteAtDepth(action.args, depth+1)
			if current == governedExecutionExplicitWrite {
				return current
			}
			if current == governedExecutionOpaque {
				finding = current
			}
		}
		return finding
	}
	if name == "xargs" {
		command, found, safe := xargsCommand(args)
		if !safe {
			return governedExecutionOpaque
		}
		if !found {
			return governedExecutionNone
		}
		current := governedWordsWriteAtDepth(command, depth+1)
		if current == governedExecutionNone && len(command) > 0 && filepath.Base(command[0]) == "ears-manager" {
			// xargs appends standard-input tokens to the child argv, so a
			// governed write can be assembled from uninspectable stdin data.
			return governedExecutionOpaque
		}
		return current
	}
	if name != "ears-manager" {
		if len(args) > 0 && strings.HasPrefix(args[0], quotedControlWordPrefix) {
			return governedExecutionNone
		}
		if containsLiteralGovernedWrite(args) {
			return governedExecutionExplicitWrite
		}
		return governedExecutionNone
	}
	args, safe := stripOutputOptions(args[1:])
	if !safe {
		return governedExecutionOpaque
	}
	if len(args) == 0 || contains(args, "--help") || contains(args, "--version") {
		return governedExecutionNone
	}
	name = args[0]
	if len(args) >= 2 {
		name += " " + args[1]
	}
	switch name {
	case "project init", "artifact put", "interface add", "interface update",
		"requirement add", "requirement update", "requirement retire",
		"change-set create", "change-set update":
		return governedExecutionExplicitWrite
	default:
		return governedExecutionNone
	}
}

func containsLiteralGovernedWrite(args []string) bool {
	// shortcut: this backstop recognizes literal argv sequences only; extend it
	// when a concrete wrapper string format is identified.
	if len(args) == 0 {
		return false
	}
	if literalOutputCommand(args) {
		return false
	}
	for index, arg := range args {
		if filepath.Base(arg) != "ears-manager" || index+1 >= len(args) {
			if code, found := commandStringOperand(args, index); found && governedWriteInCommandString(code) {
				return true
			}
			continue
		}
		if governedWordsWriteAtDepth(append([]string{"ears-manager"}, args[index+1:]...), 0) == governedExecutionExplicitWrite {
			return true
		}
		if code, found := commandStringOperand(args, index); found && governedWriteInCommandString(code) {
			return true
		}
	}
	return false
}

func literalOutputCommand(args []string) bool {
	name := filepath.Base(args[0])
	if contains([]string{"echo", "printf", "cat", "grep", "rg", "head", "tail", "wc", "sort", "uniq", "cut", "tr", "pwd", "ls", "date", "true", "false"}, name) {
		return true
	}
	return name == "git" && len(args) > 1 && contains([]string{"grep", "status", "diff", "log", "show", "rev-parse", "branch"}, args[1])
}

func commandStringOperand(args []string, index int) (string, bool) {
	arg := args[index]
	for _, short := range []string{"-c", "-e", "-S"} {
		if arg == short {
			if index+1 < len(args) {
				return args[index+1], true
			}
			return "", false
		}
		if strings.HasPrefix(arg, short) && len(arg) > len(short) {
			return strings.TrimPrefix(strings.TrimPrefix(arg, short), "="), true
		}
	}
	for _, long := range []string{"--command", "--eval", "--execute", "--split-string", "--expression", "--cmd", "--run", "--script", "--program"} {
		if arg == long {
			if index+1 < len(args) {
				return args[index+1], true
			}
			return "", false
		}
		if strings.HasPrefix(arg, long+"=") {
			return strings.TrimPrefix(arg, long+"="), true
		}
	}
	return "", false
}

func governedWriteInCommandString(command string) bool {
	if literalGovernedWritePattern.MatchString(command) {
		return true
	}
	for _, words := range shellCommandSegments(command) {
		for index, word := range words {
			if filepath.Base(word) == "ears-manager" && index+1 < len(words) &&
				governedWordsWriteAtDepth(words[index:], 1) == governedExecutionExplicitWrite {
				return true
			}
		}
	}
	return false
}

// opaqueGovernedExecution identifies command forms whose execution can be
// hidden from the shell-word scanner. Informational commands and ordinary
// script-file invocations remain available.
func opaqueGovernedExecution(name string, args []string) bool {
	switch {
	case isCodeInterpreter(name, args):
		return true
	case isRecipeRunner(name):
		return !isInformationalCommand(args)
	case name == "go":
		// Build/inspection commands do not execute the resulting Go package;
		// test, run, generate, and tool do execute code outside this scanner.
		return goExecutionOpaque(args)
	case name == "git":
		return gitInvocationOpaque(args)
	case isOpaqueExecutionWrapper(name):
		return isOpaqueWrapperInvocation(name, args)
	default:
		return false
	}
}

func stripOutputOptions(args []string) ([]string, bool) {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != "--output" {
			result = append(result, args[i])
			continue
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return nil, false
		}
		i++
	}
	return result, true
}

func stripShellControlPrefix(words []string) []string {
	index := 0
	for index < len(words) && (isShellControlWord(words[index]) || assignmentWord.MatchString(words[index])) {
		index++
	}
	return words[index:]
}

func isShellControlWord(word string) bool {
	switch word {
	case "do", "then", "else", "elif", "if", "for", "while", "until", "case", "select", "!":
		return true
	default:
		return false
	}
}

func unwrapCommand(words []string) []string {
	args := append([]string(nil), words...)
	for len(args) > 0 {
		name := filepath.Base(args[0])
		switch name {
		case "env":
			i := 1
			for i < len(args) {
				if args[i] == "-S" || args[i] == "--split-string" || strings.HasPrefix(args[i], "--split-string=") ||
					(strings.HasPrefix(args[i], "-") && !strings.HasPrefix(args[i], "--") && strings.Contains(args[i], "S")) {
					return nil // env -S has its own argv grammar; do not guess after unwrapping.
				}
				if args[i] == "--" {
					i++
					break
				}
				if args[i] == "-u" || args[i] == "--unset" || args[i] == "-C" || args[i] == "--chdir" {
					i += 2
					continue
				}
				if strings.HasPrefix(args[i], "-") || assignmentWord.MatchString(args[i]) {
					i++
					continue
				}
				break
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "command", "exec", "nohup", "time", "nice":
			i := 1
			for i < len(args) && strings.HasPrefix(args[i], "-") {
				option := args[i]
				i++
				if (option == "-n" || option == "--adjustment" || option == "-f" || option == "--format") && i < len(args) {
					i++
				}
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "builtin":
			if len(args) < 2 {
				return nil
			}
			args = args[1:]
		case "timeout":
			i := 1
			for i < len(args) && strings.HasPrefix(args[i], "-") {
				option := args[i]
				i++
				if (option == "-s" || option == "--signal" || option == "-k" || option == "--kill-after") && i < len(args) {
					i++
				}
			}
			if i < len(args) {
				i++ // timeout duration
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "stdbuf":
			i := 1
			for i < len(args) {
				option := args[i]
				if option == "--help" || option == "--version" {
					return args
				}
				if option == "--" {
					i++
					break
				}
				if option == "-i" || option == "-o" || option == "-e" || option == "--input" || option == "--output" || option == "--error" {
					i += 2
					continue
				}
				if strings.HasPrefix(option, "--input=") || strings.HasPrefix(option, "--output=") || strings.HasPrefix(option, "--error=") ||
					(strings.HasPrefix(option, "-i") && len(option) > 2) || (strings.HasPrefix(option, "-o") && len(option) > 2) || (strings.HasPrefix(option, "-e") && len(option) > 2) {
					i++
					continue
				}
				if strings.HasPrefix(option, "-") {
					return nil
				}
				break
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "setsid":
			i := 1
			for i < len(args) {
				switch args[i] {
				case "-c", "--ctty", "-f", "--fork", "-w", "--wait":
					i++
				case "--help", "--version":
					return args
				case "--":
					i++
					goto setsidCommand
				default:
					if strings.HasPrefix(args[i], "-") {
						return nil
					}
					goto setsidCommand
				}
			}
		setsidCommand:
			if i >= len(args) {
				return nil
			}
			args = args[i:]
		case "busybox":
			if len(args) < 2 {
				return nil
			}
			args = args[1:]
		case "sudo":
			if isInformationalCommand(args) {
				return args
			}
			return nil // sudo option parsing is too broad to unwrap safely.
		default:
			for len(args) > 0 && assignmentWord.MatchString(args[0]) {
				args = args[1:]
			}
			return args
		}
	}
	return nil
}

func isShellInterpreter(name string) bool {
	switch name {
	case "sh", "bash", "dash", "zsh", "ksh", "mksh":
		return true
	default:
		return false
	}
}
