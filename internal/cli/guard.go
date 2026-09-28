package cli

import (
	"os"
	"path"
	"regexp"
	"strings"
)

// The agent's sh tool runs command lines a model wrote, and a model can be
// wrong, or be steered by instructions planted in a file it read. Before a
// command runs, mote sorts it into one of three kinds:
//
//   - blocked: catastrophic and never useful to an agent (rm -rf ~, mkfs,
//     curl | sh). Never run, whatever the flags.
//   - read-only: every part is a program from a short list of readers (ls,
//     cat, grep, git log...) with no redirection into files and nothing
//     mote cannot see through, such as $(...). --yes runs these unasked.
//   - anything else: asked about, unless sh is sandboxed and --yes given.
//
// This reads a command line as text, so it is a seatbelt, not a security
// boundary: it fails safe (what it cannot understand is not read-only), but
// the blocked list can be talked around by a determined command. The
// sandbox is what bounds the damage of whatever gets through.

// shVerdict is how mote treats one command line.
type shVerdict struct {
	blocked  bool
	readOnly bool
	reason   string // why it is blocked, or why it is not read-only
}

// shWord is a token of a command line: a word with its quotes removed, or
// an operator such as | or >.
type shWord struct {
	text string
	op   bool
}

// lexShell splits a POSIX command line into words and operators. It
// understands quotes, backslashes, command separators and redirections;
// complex reports syntax it does not follow (substitutions, subshells,
// here-documents), which makes a command not read-only.
func lexShell(cmd string) (words []shWord, complex string, ok bool) {
	var cur strings.Builder
	held := false
	flush := func() {
		if held {
			words = append(words, shWord{text: cur.String()})
			cur.Reset()
			held = false
		}
	}
	op := func(s string) { flush(); words = append(words, shWord{text: s, op: true}) }
	// The first thing noticed is the reason given.
	note := func(why string) {
		if complex == "" {
			complex = why
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		next := byte(0)
		if i+1 < len(cmd) {
			next = cmd[i+1]
		}
		switch {
		case c == '\\' && i+1 < len(cmd):
			i++
			cur.WriteByte(cmd[i])
			held = true
		case c == '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return nil, "", false
			}
			cur.WriteString(cmd[i+1 : i+1+j])
			held = true
			i += j + 1
		case c == '"':
			i++
			for ; i < len(cmd) && cmd[i] != '"'; i++ {
				if cmd[i] == '\\' && i+1 < len(cmd) {
					i++
				} else if cmd[i] == '`' || (cmd[i] == '$' && i+1 < len(cmd) && cmd[i+1] == '(') {
					note("it substitutes a command's output")
				}
				cur.WriteByte(cmd[i])
			}
			if i >= len(cmd) {
				return nil, "", false
			}
			held = true
		case c == '`':
			note("it substitutes a command's output")
			cur.WriteByte(c)
			held = true
		case c == '$' && next == '(':
			note("it substitutes a command's output")
			cur.WriteString("$(")
			held = true
			i++
		case (c == '<' || c == '>') && next == '(':
			note("it uses process substitution")
			cur.WriteByte(c)
			cur.WriteByte('(')
			held = true
			i++
		case c == '(' || c == ')' || c == '{' || c == '}':
			if c == '{' || c == '}' {
				// Braces inside a word ({a,b}, ${HOME}) are words.
				if held || (next != ' ' && next != '\t' && next != '\n' && next != 0 && c == '{') {
					cur.WriteByte(c)
					held = true
					continue
				}
			}
			note("it uses a subshell or group")
			op(string(c))
		case c == '<' && next == '<':
			note("it uses a here-document")
			op("<<")
			i++
		case c == '>' || (c == '&' && next == '>'):
			// A file descriptor number right before > belongs to it (2>).
			if c == '>' && held && isDigits(cur.String()) {
				cur.Reset()
				held = false
			}
			flush()
			o := ">"
			if c == '&' {
				o, i = "&>", i+1
			}
			if i+1 < len(cmd) && cmd[i+1] == '>' {
				o += ">"
				i++
			}
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				o += "&" // duplication, as in 2>&1
				i++
			}
			op(o)
		case c == '<':
			op("<")
		case c == '|' || c == '&':
			if next == c {
				op(string([]byte{c, c}))
				i++
			} else {
				op(string(c))
			}
		case c == ';' || c == '\n':
			op(";")
		case c == ' ' || c == '\t' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			held = true
		}
	}
	flush()
	return words, complex, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// simpleCmd is one command of a line, with where its output goes.
type simpleCmd struct {
	args      []string
	redirects []string // targets of > and >>
	pipedTo   bool     // its output feeds the next command
}

// commands splits lexed words into simple commands. Background & counts as
// a separator: a backgrounded command still runs.
func commands(words []shWord) []simpleCmd {
	var out []simpleCmd
	cur := simpleCmd{}
	end := func(piped bool) {
		cur.pipedTo = piped
		if len(cur.args) > 0 || len(cur.redirects) > 0 {
			out = append(out, cur)
		}
		cur = simpleCmd{}
	}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if !w.op {
			cur.args = append(cur.args, w.text)
			continue
		}
		switch w.text {
		case "|":
			end(true)
		case ";", "&&", "||", "&", "(", ")", "{", "}":
			end(false)
		case "<", "<<":
			i++ // the input file
		default: // >, >>, &>, >&, ...
			if i+1 < len(words) && !words[i+1].op {
				i++
				if !strings.HasSuffix(w.text, "&") {
					cur.redirects = append(cur.redirects, words[i].text)
				}
			}
		}
	}
	end(false)
	return out
}

// program strips what runs another command (sudo, env, nice...) and
// variable assignments, returning the program that really runs, its
// arguments, and the wrappers that were removed.
func program(args []string) (string, []string, []string) {
	var wrappers []string
	for len(args) > 0 {
		a := args[0]
		if strings.Contains(a, "=") && !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "/") {
			args = args[1:] // VAR=value
			continue
		}
		base := path.Base(a)
		switch base {
		case "sudo", "doas", "nice", "nohup", "time", "command", "builtin", "exec", "env", "xargs", "timeout", "stdbuf":
			wrappers = append(wrappers, base)
			args = args[1:]
			// Their own options, and timeout's duration, come before the
			// command they run.
			for len(args) > 0 && (strings.HasPrefix(args[0], "-") || (base == "timeout" && isDuration(args[0]))) {
				args = args[1:]
			}
			continue
		}
		return base, args[1:], wrappers
	}
	return "", nil, wrappers
}

var durationRe = regexp.MustCompile(`^[0-9.]+[smhd]?$`)

func isDuration(s string) bool { return durationRe.MatchString(s) }

// readOnlyPrograms only read, given the argument checks in readOnlyArgs.
var readOnlyPrograms = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "egrep": true, "fgrep": true,
	"rg": true, "pwd": true, "echo": true, "printf": true, "file": true, "stat": true, "du": true, "df": true,
	"which": true, "whoami": true, "id": true, "date": true, "uname": true, "hostname": true, "cut": true,
	"tr": true, "diff": true, "cmp": true, "basename": true, "dirname": true, "realpath": true, "readlink": true,
	"tree": true, "jq": true, "nl": true, "column": true, "true": true, "false": true, "test": true, "[": true,
	"md5sum": true, "sha1sum": true, "sha256sum": true, "seq": true, "expr": true, "cd": true, "type": true,
	"find": true, "git": true, "sort": true, "uniq": true,
}

var readOnlyGit = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "ls-files": true, "rev-parse": true,
	"blame": true, "grep": true, "describe": true, "shortlog": true, "branch": true, "remote": true, "tag": true,
}

// readOnlyArgs rejects the options that make a reader write or run
// something: find -delete and -exec, rg --pre, sort -o, git subcommands
// that change a repository, and the like.
func readOnlyArgs(prog string, args []string) (bool, string) {
	switch prog {
	case "find":
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return false, "find " + a + " changes or runs things"
			}
		}
	case "rg":
		for _, a := range args {
			if strings.HasPrefix(a, "--pre") {
				return false, "rg --pre runs a command"
			}
		}
	case "sort":
		for _, a := range args {
			if a == "-o" || strings.HasPrefix(a, "--output") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "o")) {
				return false, "sort -o writes a file"
			}
		}
	case "uniq":
		operands := 0
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				operands++
			}
		}
		if operands > 1 {
			return false, "uniq with two files writes the second"
		}
	case "git":
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			if args[0] == "-C" || args[0] == "-c" {
				if args[0] == "-c" {
					return false, "git -c can set a command to run"
				}
				args = args[1:]
			}
			if len(args) > 0 {
				args = args[1:]
			}
		}
		if len(args) == 0 || !readOnlyGit[args[0]] {
			sub := ""
			if len(args) > 0 {
				sub = " " + args[0]
			}
			return false, "git" + sub + " can change the repository"
		}
		// branch, remote and tag only list when given no names.
		if args[0] == "branch" || args[0] == "remote" || args[0] == "tag" {
			for _, a := range args[1:] {
				switch a {
				case "-a", "-r", "-v", "-vv", "--list", "-l", "--show-current", "--all", "--remotes", "--verbose":
				default:
					return false, "git " + args[0] + " " + a + " can change the repository"
				}
			}
		}
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--ext-diff") {
				return false, "git " + a + " writes a file or runs a program"
			}
		}
	}
	return true, ""
}

// Paths whose recursive removal or ownership change is never an agent's
// business.
var precious = map[string]bool{
	"/": true, "/*": true, "~": true, "~/*": true, "$HOME": true, "${HOME}": true, "$HOME/*": true, "${HOME}/*": true,
	"/home": true, "/home/*": true, "/root": true, "/etc": true, "/usr": true, "/var": true, "/bin": true,
	"/sbin": true, "/lib": true, "/lib64": true, "/boot": true, "/opt": true, "/dev": true, "/proc": true, "/sys": true,
	"/System": true, "/Users": true, "/Users/*": true, "/Library": true, "/Applications": true,
}

func isPrecious(p string) bool {
	if p != "/" {
		p = strings.TrimRight(p, "/")
	}
	if precious[p] {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = strings.TrimRight(home, "/")
		return p == home || p == home+"/*"
	}
	return false
}

var (
	forkBombRe = regexp.MustCompile(`:\s*\(\s*\)\s*\{`)
	devDiskRe  = regexp.MustCompile(`^/dev/(sd|hd|vd|xvd|nvme|mmcblk|disk|rdisk)`)
	// A download handed straight to an interpreter, including inside a
	// substitution the lexer does not follow.
	fetchRunRe = regexp.MustCompile(`\b(curl|wget|fetch)\b[^|;&]*\|\s*(sudo\s+)?(sh|bash|zsh|dash|ksh|fish|python3?|perl|ruby|node)\b|\b(sh|bash|zsh|dash)\s+-c\s+["']?\$\(\s*(curl|wget)`)
	shells     = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
		"python": true, "python3": true, "perl": true, "ruby": true, "node": true}
)

func hasFlag(args []string, long string, short byte) bool {
	for _, a := range args {
		if a == long {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.IndexByte(a, short) > 0 {
			return true
		}
	}
	return false
}

// blockedCmd reports a command mote never runs for an agent.
func blockedCmd(prog string, args []string) string {
	operands := func() []string {
		var out []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				out = append(out, a)
			}
		}
		return out
	}
	switch {
	case prog == "rm":
		for _, a := range args {
			if a == "--no-preserve-root" {
				return "rm --no-preserve-root"
			}
		}
		if hasFlag(args, "--recursive", 'r') || hasFlag(args, "--recursive", 'R') {
			for _, o := range operands() {
				if isPrecious(o) {
					return "recursive rm of " + o
				}
			}
		}
	case prog == "chmod" || prog == "chown" || prog == "chgrp":
		if hasFlag(args, "--recursive", 'R') {
			for _, o := range operands() {
				if isPrecious(o) {
					return "recursive " + prog + " of " + o
				}
			}
		}
	case strings.HasPrefix(prog, "mkfs") || prog == "wipefs" || prog == "fdisk" || prog == "sfdisk" || prog == "parted" || prog == "mkswap":
		return prog + " formats or repartitions disks"
	case prog == "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=/dev/") && a != "of=/dev/null" {
				return "dd onto a device"
			}
		}
	case prog == "shutdown" || prog == "reboot" || prog == "halt" || prog == "poweroff":
		return prog
	case prog == "init" || prog == "telinit":
		for _, a := range args {
			if a == "0" || a == "6" {
				return prog + " " + a
			}
		}
	case prog == "shred":
		for _, o := range operands() {
			if strings.HasPrefix(o, "/dev/") {
				return "shred of a device"
			}
		}
	}
	return ""
}

// classifyShell decides how a command line is treated. See the top of the
// file for what it can and cannot promise.
func classifyShell(line string) shVerdict {
	if forkBombRe.MatchString(line) {
		return shVerdict{blocked: true, reason: "a fork bomb"}
	}
	if fetchRunRe.MatchString(line) {
		return shVerdict{blocked: true, reason: "running a download without looking at it"}
	}
	words, complex, ok := lexShell(line)
	if !ok {
		return shVerdict{reason: "its quotes do not close"}
	}
	cmds := commands(words)
	verdict := shVerdict{readOnly: true}
	notReadOnly := func(why string) {
		if verdict.readOnly {
			verdict.readOnly, verdict.reason = false, why
		}
	}
	if complex != "" {
		notReadOnly(complex)
	}
	for i, c := range cmds {
		for _, r := range c.redirects {
			if devDiskRe.MatchString(r) {
				return shVerdict{blocked: true, reason: "writing to a disk device"}
			}
			if r != "/dev/null" && r != "/dev/stdout" && r != "/dev/stderr" {
				notReadOnly("it writes to " + r)
			}
		}
		prog, args, wrappers := program(c.args)
		if prog == "" {
			continue
		}
		if why := blockedCmd(prog, args); why != "" {
			return shVerdict{blocked: true, reason: why}
		}
		// A wrapper's options can take values (sudo -u root, nice -n 5),
		// so the real program may sit further along: try each word.
		if len(wrappers) > 0 {
			for j, a := range c.args {
				if why := blockedCmd(path.Base(a), c.args[j+1:]); why != "" {
					return shVerdict{blocked: true, reason: why}
				}
			}
		}
		if c.pipedTo && i+1 < len(cmds) && (prog == "curl" || prog == "wget") {
			if next, _, _ := program(cmds[i+1].args); shells[next] {
				return shVerdict{blocked: true, reason: "running a download without looking at it"}
			}
		}
		if len(wrappers) > 0 {
			notReadOnly(wrappers[0] + " runs another command")
			continue
		}
		if !readOnlyPrograms[prog] {
			notReadOnly(prog + " is not a known read-only program")
			continue
		}
		if ok, why := readOnlyArgs(prog, args); !ok {
			notReadOnly(why)
		}
	}
	if len(cmds) == 0 {
		notReadOnly("it runs nothing mote can see")
	}
	return verdict
}
