package secrets

import "strings"

// Variable is plaintext for a running script only. Never serialize or persist it.
type Variable struct {
	Name  string `json:"-"`
	Value []byte `json:"-"`
}

// ValidVariableName keeps secrets out of shell startup, command lookup, loader,
// and Piped runtime settings. These are application inputs, not shell options.
func ValidVariableName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for index, letter := range name {
		if !(letter >= 'A' && letter <= 'Z' || letter == '_' || index > 0 && letter >= '0' && letter <= '9') {
			return false
		}
	}
	for _, prefix := range []string{"BASH", "LD_", "DYLD_", "SUDO_", "PIPED_", "HIST", "LC_", "MALLOC_"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	switch name {
	case "PS0", "PS1", "PS2", "PS3", "PS4", "GLIBC_TUNABLES",
		"GCONV_PATH", "LOCPATH", "NLSPATH", "PIPESTATUS", "PATH", "HOME",
		"SHELL", "ENV", "IFS", "CDPATH", "GLOBIGNORE", "SHELLOPTS",
		"PROMPT_COMMAND", "TMPDIR", "TERM", "USER", "LOGNAME", "UID", "EUID",
		"PPID", "GROUPS", "FUNCNAME", "DIRSTACK", "SHLVL", "RANDOM", "SRANDOM",
		"SECONDS", "LINENO", "OPTIND", "OPTARG", "OPTERR", "POSIXLY_CORRECT",
		"LANG", "EPOCHSECONDS", "EPOCHREALTIME", "_", "PWD", "OLDPWD",
		"OSTYPE", "MACHTYPE", "HOSTTYPE", "TIMEFORMAT", "TMOUT", "IGNOREEOF",
		"FIGNORE", "INPUTRC", "MAIL", "MAILPATH", "MAILCHECK":
		return false
	}
	return true
}
