package guard

import "testing"

func TestGitExecutionConfigDetection(t *testing.T) {
	t.Run("keys", func(t *testing.T) {
		for _, key := range []string{
			"diff.external", "diff.converter.textconv", "diff.converter.command",
			"merge.driver.driver", "gpg.program", "gpg.ssh.program",
			"remote.origin.uploadpack", "remote.origin.receivepack",
			"uploadpack.packobjectshook", "include.path", "includeIf.gitdir:/repo.path",
			"sendemail.smtpserver", "sendemail.work.smtpserver", "submodule.example.update",
			"man.viewer", "man.probe.cmd", "man.probe.path",
			"browser.probe.cmd", "browser.probe.path", "help.browser", "web.browser", "instaweb.httpd",
		} {
			if !gitExecutionConfigKey(key) {
				t.Errorf("gitExecutionConfigKey(%q) = false", key)
			}
		}
		for _, key := range []string{"remote.origin.url", "help.autocorrect", "web.autocorrect", "instaweb.local"} {
			if gitExecutionConfigKey(key) {
				t.Errorf("gitExecutionConfigKey(%q) = true", key)
			}
		}
	})

	t.Run("sections", func(t *testing.T) {
		for _, section := range []string{
			"diff", "diff.converter", "merge.driver", "gpg.ssh",
			"remote.origin", "uploadpack", "include", "includeIf.gitdir:/repo",
			"sendemail", "sendemail.work", "submodule", "submodule.example",
			"man", "man.probe", "browser", "browser.probe", "help", "web", "web.browser", "instaweb", "instaweb.httpd",
		} {
			if !gitExecutionConfigSection(section) {
				t.Errorf("gitExecutionConfigSection(%q) = false", section)
			}
		}
	})

	t.Run("patterns", func(t *testing.T) {
		for _, pattern := range []string{
			`^diff\.external$`, `^diff\..*\.textconv$`, `^diff\..*\.command$`,
			`^merge\..*\.driver$`, `^gpg(\..*)?\.program$`,
			`^remote\..*\.(uploadpack|receivepack)$`, `^uploadpack\.packobjectshook$`,
			`^include\.path$`, `^includeIf\..*\.path$`,
			`^sendemail\.smtpserver$`, `^sendemail\..*\.smtpserver$`, `^help\..*$`, `^submodule\..*\.update$`,
			`^man\.viewer$`, `^man\..*\.(cmd|path)$`,
			`^browser\..*\.(cmd|path)$`, `^help\.browser$`, `^web\.browser$`, `^instaweb\.httpd$`,
		} {
			if !gitExecutionConfigPattern(pattern) {
				t.Errorf("gitExecutionConfigPattern(%q) = false", pattern)
			}
		}
	})
}

func TestRipgrepConfigEnvironmentMakesInvocationOpaque(t *testing.T) {
	t.Setenv("RIPGREP_CONFIG_PATH", "/tmp/rg-config")
	if !opaqueGovernedExecution("rg", []string{"rg", "."}) {
		t.Fatal("ripgrep with an environment config path was considered inspectable")
	}
}

func TestCommandExecutionEnvironmentOverrides(t *testing.T) {
	for _, key := range []string{
		"GIT_SSH_COMMAND", "GIT_SSH", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_TEMPLATE_DIR", "GIT_CEILING_DIRECTORIES",
	} {
		if !commandExecutionEnvironmentKey(key) {
			t.Errorf("commandExecutionEnvironmentKey(%q) = false", key)
		}
		if !commandExecutionEnvironmentAssignment(key + "=value") {
			t.Errorf("commandExecutionEnvironmentAssignment(%q) = false", key+"=value")
		}
	}

	functionVariable := "BASH_FUNC_pwn%%=() { ears-manager requirement add; }"
	if !assignmentWord.MatchString(functionVariable) {
		t.Fatal("assignmentWord did not recognize an exported Bash-function variable")
	}
	if !commandExecutionEnvironmentAssignment(functionVariable) {
		t.Fatal("exported Bash-function variable was not recognized as an execution override")
	}
	for _, assignment := range []string{"FOO=value", "BUILD_NUMBER=42", "GIT_COLOR=always", "BASH_FUNC_pwn=value"} {
		if commandExecutionEnvironmentAssignment(assignment) {
			t.Errorf("unrelated assignment %q was considered an execution override", assignment)
		}
	}
}

func TestGitInstawebIsOpaque(t *testing.T) {
	if !gitInvocationOpaque([]string{"git", "instaweb"}) {
		t.Fatal("git instaweb was considered a read-only command")
	}
}

func TestGitConfigConversionFlagsDoNotConsumeKeys(t *testing.T) {
	for _, option := range []string{"--bool", "--int", "--bool-or-int", "--path", "--expiry-date"} {
		t.Run(option, func(t *testing.T) {
			args := []string{option, "core.fsmonitor", "/tmp/evil"}
			state, safe := scanGitConfigWriteOptions(args)
			if !safe || len(state.positionals) != 2 || state.positionals[0] != "core.fsmonitor" || state.positionals[1] != "/tmp/evil" {
				t.Fatalf("opaque config scan = %+v, safe=%v; key was consumed", state, safe)
			}
			if !gitConfigCommandWritesExecutableConfig(args) {
				t.Fatal("config scan did not detect the executable key")
			}

			options, safe := parseGitConfigWriteOptions(args)
			if !safe || len(options.positionals) != 2 || options.positionals[0] != "core.fsmonitor" || options.positionals[1] != "/tmp/evil" {
				t.Fatalf("write-target config scan = %+v, safe=%v; key was consumed", options, safe)
			}
		})
	}
}

func TestGitConfigConversionFlagsRejectAttachedValues(t *testing.T) {
	for _, option := range []string{"--bool", "--int", "--bool-or-int", "--path", "--expiry-date"} {
		t.Run(option, func(t *testing.T) {
			args := []string{option + "=value", "core.fsmonitor", "/tmp/evil"}
			if _, safe := scanGitConfigWriteOptions(args); safe {
				t.Fatal("opaque config scan accepted an invalid attached value")
			}
			if _, safe := parseGitConfigWriteOptions(args); safe {
				t.Fatal("write-target config scan accepted an invalid attached value")
			}
		})
	}
}

func TestGitConfigTypeOptionConsumesAttachedAndSeparateValues(t *testing.T) {
	for _, args := range [][]string{
		{"--global", "--type=path", "user.name", "value"},
		{"--global", "--type", "path", "user.name", "value"},
	} {
		t.Run(args[1], func(t *testing.T) {
			state, safe := scanGitConfigWriteOptions(args)
			if !safe || len(state.positionals) != 2 || state.positionals[0] != "user.name" || state.positionals[1] != "value" {
				t.Fatalf("opaque config scan = %+v, safe=%v", state, safe)
			}
			if gitConfigCommandWritesExecutableConfig(args) {
				t.Fatal("harmless user.name config was considered executable")
			}

			options, safe := parseGitConfigWriteOptions(args)
			if !safe || len(options.positionals) != 2 || options.positionals[0] != "user.name" || options.positionals[1] != "value" {
				t.Fatalf("write-target config scan = %+v, safe=%v", options, safe)
			}
			command := append([]string{"git", "config"}, args...)
			if targets, safe := gitConfigWriteTargets(command, projectState{}, ""); !safe || len(targets) != 0 {
				t.Fatalf("global write targets = %v, safe=%v", targets, safe)
			}
		})
	}
}
