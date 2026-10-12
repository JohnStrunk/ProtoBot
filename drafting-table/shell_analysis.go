package guard

import (
	"path/filepath"
	"strings"
)

const quotedControlWordPrefix = "\x00quoted-control:"

func inspectOtherShell(command string, depth int) ([]string, bool, bool) {
	if depth > 8 {
		return nil, false, true
	}
	targets, unsafe := parseOtherShellRedirects(command)
	if unsafe {
		return nil, false, true
	}
	changesDirectory := false
	for _, words := range shellCommandSegments(command) {
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
		nested, safe := nestedShellCommands(words)
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
	if len(words) == 0 {
		return nil, true
	}
	name := filepath.Base(words[0])
	if name == "env" {
		script, found, safe := envSplitString(words)
		if !safe {
			return nil, false
		}
		if found {
			return []nestedShellScript{{script: script}}, true
		}
		unwrapped := unwrapCommand(words)
		if len(unwrapped) == 0 || sameWords(words, unwrapped) {
			return nil, false
		}
		return nestedShellCommands(unwrapped)
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
			children, childSafe := nestedShellCommands(action.args)
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
		return nestedShellCommands(command)
	}
	unwrapped := unwrapCommand(words)
	if len(unwrapped) == 0 {
		return nil, false
	}
	if !sameWords(words, unwrapped) {
		return nestedShellCommands(unwrapped)
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
			option, _, hasValue := strings.Cut(arg, "=")
			switch option {
			case "--noprofile", "--norc", "--posix", "--restricted", "--verbose", "--debugger", "--noediting", "--dump-po-strings", "--dump-strings":
				if hasValue {
					return "", false, false
				}
				i++
			case "--rcfile", "--init-file", "--encoding":
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

func invokesGovernedWrite(command string, manifest adapterManifest) bool {
	if !contains(manifest.GovernedCommands, "ears-manager") {
		return false
	}
	for _, words := range shellCommandSegments(command) {
		if governedWordsWrite(words) {
			return true
		}
	}
	return false
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

func governedWordsWrite(words []string) bool {
	if len(words) == 0 {
		return false
	}
	words = stripShellControlPrefix(words)
	if len(words) == 0 {
		return false
	}
	if filepath.Base(words[0]) == "env" {
		script, found, safe := envSplitString(words)
		if !safe {
			return true
		}
		if found {
			return invokesGovernedWrite(script, adapterManifest{GovernedCommands: []string{"ears-manager"}})
		}
	}
	args := unwrapCommand(words)
	if len(args) == 0 {
		return true
	}
	name := filepath.Base(args[0])
	if isShellInterpreter(name) {
		script, found, safe := shellCommandString(args)
		if !safe {
			return true
		}
		return found && invokesGovernedWrite(script, adapterManifest{GovernedCommands: []string{"ears-manager"}})
	}
	if name == "find" {
		actions, safe := findActions(args)
		if !safe {
			return true
		}
		for _, action := range actions {
			if governedWordsWrite(action.args) {
				return true
			}
		}
		return false
	}
	if name == "xargs" {
		command, found, safe := xargsCommand(args)
		if !safe {
			return true
		}
		return found && governedWordsWrite(command)
	}
	if name != "ears-manager" {
		return false
	}
	args = args[1:]
	if len(args) >= 2 && args[0] == "--output" && args[1] == "json" {
		args = args[2:]
	}
	if len(args) == 0 || contains(args, "--help") || contains(args, "--version") {
		return false
	}
	name = args[0]
	if len(args) >= 2 {
		name += " " + args[1]
	}
	switch name {
	case "project init", "artifact put", "interface add", "interface update",
		"requirement add", "requirement update", "requirement retire",
		"change-set create", "change-set update":
		return true
	default:
		return false
	}
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
	case "do", "then", "else", "elif", "if", "for", "while", "until", "case", "select", "!", "coproc":
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
		case "busybox":
			if len(args) < 2 {
				return nil
			}
			args = args[1:]
		case "sudo":
			i := 1
			for i < len(args) && strings.HasPrefix(args[i], "-") {
				i++
				if i < len(args) && !strings.HasPrefix(args[i], "-") && strings.Contains(args[i-1], "u") {
					i++
				}
			}
			for i < len(args) && assignmentWord.MatchString(args[i]) {
				i++
			}
			if i >= len(args) {
				return nil
			}
			args = args[i:]
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
