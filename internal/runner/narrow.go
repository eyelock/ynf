package runner

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A sensor scope narrows the sensor the harness declares to the item, and nothing else: ynh's
// --sensor-overlay substitutes the declared command for the run, so a scope free to say anything
// could say `true` (ADR-006).

// shellMeta are the characters that, outside quotes, make a shell do more than split words.
const shellMeta = ";|&<>()`$*?[]{}!"

// words splits s into words with shell quoting rules and evaluates nothing. In strict mode, which
// a scope gets, an unquoted operator, expansion or comment, and any substitution inside double
// quotes, is an error; the harness's own declared command is read leniently.
func words(s string, strict bool) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	flush := func() {
		if in {
			out = append(out, cur.String())
			cur.Reset()
			in = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '\n' || c == '\r':
			if strict {
				return nil, errors.New("it has a newline, which separates commands")
			}
			flush()
		case c == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j == len(rs) {
				return nil, errors.New("it has an unterminated single quote")
			}
			cur.WriteString(string(rs[i+1 : j]))
			in, i = true, j
		case c == '"':
			in = true
			j := i + 1
			for ; j < len(rs) && rs[j] != '"'; j++ {
				switch {
				case rs[j] == '\\' && j+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[j+1]):
					j++
					cur.WriteRune(rs[j])
				case (rs[j] == '$' || rs[j] == '`') && strict:
					return nil, fmt.Errorf("%q inside double quotes is an expansion", string(rs[j]))
				default:
					cur.WriteRune(rs[j])
				}
			}
			if j == len(rs) {
				return nil, errors.New("it has an unterminated double quote")
			}
			i = j
		case c == '\\':
			if i+1 == len(rs) {
				return nil, errors.New("it ends in a backslash")
			}
			i++
			cur.WriteRune(rs[i])
			in = true
		case strict && strings.ContainsRune(shellMeta, c):
			return nil, fmt.Errorf("it has the shell operator or expansion %q", string(c))
		case strict && !in && (c == '#' || c == '~'):
			return nil, fmt.Errorf("it has %q at the start of a word, a comment or a home expansion", string(c))
		default:
			cur.WriteRune(c)
			in = true
		}
	}
	flush()
	return out, nil
}

// assignment matches a leading NAME=value word.
var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// rawWords splits s on unquoted blanks and keeps each word as written: quotes, backslashes and
// expansions stay in the text, so two words are equal only when they are the same characters.
// end[i] is the offset in s just after word i. An open quote runs to the end of s.
func rawWords(s string) (ws []string, end []int) {
	start := -1
	flush := func(i int) {
		if start >= 0 {
			ws = append(ws, s[start:i])
			end = append(end, i)
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
		}
		switch c {
		case '\\':
			i++
		case '\'':
			if j := strings.IndexByte(s[i+1:], '\''); j >= 0 {
				i += j + 1
			} else {
				i = len(s)
			}
		case '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		}
	}
	flush(len(s))
	return ws, end
}

// envPrefix returns the leading NAME=value words of s as written, and the rest of s.
func envPrefix(s string) (prefix []string, rest string) {
	ws, end := rawWords(s)
	n := 0
	for n < len(ws) && assignment.MatchString(ws[n]) {
		n++
	}
	if n == 0 {
		return nil, s
	}
	return ws[:n], s[end[n-1]:]
}

// pathChars is what a path word the scope adds may be made of.
var pathChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// scopeDir reduces a path word to the directory it names, relative and clean: `./...` is the
// root, `./a/...` is `a`. ok is false for a word that is not a plain relative path: absolute,
// leading dash, a `..` segment, or anything outside the safe characters.
func scopeDir(w string) (dir string, ok bool) {
	if w == "" || strings.HasPrefix(w, "-") || strings.HasPrefix(w, "/") || !pathChars.MatchString(w) {
		return "", false
	}
	if slices.Contains(strings.Split(w, "/"), "..") {
		return "", false
	}
	for strings.HasPrefix(w, "./") {
		w = strings.TrimPrefix(w, "./")
	}
	if w == "." || w == "..." {
		return "", true
	}
	w = strings.TrimSuffix(w, "...")
	return strings.TrimSuffix(w, "/"), true
}

// isPathWord says whether a declared word names a place a scope may narrow to something beneath:
// `.`, a package pattern such as `./...`, or a directory-like path with a slash. A flag, a word
// with no slash (`run`, `test`) and a file (its last segment has an extension, as in
// `scripts/check.sh`) are not; nor is the program, which callers skip.
func isPathWord(w string) bool {
	if w == "." || strings.HasSuffix(w, "...") {
		return true
	}
	if strings.HasPrefix(w, "-") || !strings.Contains(w, "/") {
		return false
	}
	last := w[strings.LastIndex(w, "/")+1:]
	return last == "" || last == "." || !strings.Contains(last, ".")
}

// beneath says whether scope word p names declared path word d or something under it.
func beneath(d, p string) bool {
	dd, ok := scopeDir(d)
	if !ok {
		return false
	}
	pd, ok := scopeDir(p)
	return ok && (dd == "" || pd == dd || strings.HasPrefix(pd, dd+"/"))
}

// Narrows reports why scope is not the declared sensor command narrowed, or nil when it is. Both
// are split into words with shell quoting rules and not evaluated. The words must match one for
// one and in order, except that a path word of the declared command may be replaced by one or
// more path words beneath it (`./...` by `./x/...`, `.` by any relative path); a declared command
// with no path word may have path words appended at the end. The scope may hold no shell
// operator or expansion. The declared command's leading NAME=value assignments are kept exactly
// as written, expansions included, and the scope may not add, drop, reorder or change one.
func Narrows(declared, scope string) error {
	// The declared command's leading environment assignments are the harness author's, expansions
	// included: the scope keeps them word for word, as written, or is refused.
	dpre, declared := envPrefix(declared)
	spre, scope := envPrefix(scope)
	if !slices.Equal(dpre, spre) {
		switch {
		case len(dpre) == 0:
			return fmt.Errorf("the scope sets %s, which the declared command does not", strings.Join(spre, " "))
		case len(spre) == 0:
			return fmt.Errorf("the scope drops the declared environment %s", strings.Join(dpre, " "))
		default:
			return fmt.Errorf("the scope's environment %s is not the declared %s: assignments are kept exactly as declared", strings.Join(spre, " "), strings.Join(dpre, " "))
		}
	}
	dw, err := words(declared, false)
	if err != nil {
		return fmt.Errorf("the declared command cannot be read: %w", err)
	}
	sw, err := words(scope, true)
	if err != nil {
		return err
	}
	if len(sw) == 0 {
		return errors.New("the scope is empty")
	}
	if len(dw) == 0 {
		return errors.New("the declared command is empty")
	}
	// The program, the first word, is never a path.
	isPath := func(i int) bool { return i > 0 && isPathWord(dw[i]) }
	hasPath := false
	for i := range dw {
		hasPath = hasPath || isPath(i)
	}
	j := 0
	for i, d := range dw {
		if isPath(i) {
			start := j
			for j < len(sw) && beneath(d, sw[j]) && (j == start || i+1 == len(dw) || sw[j] != dw[i+1]) {
				j++
			}
			switch {
			case j > start:
			case j == len(sw):
				return fmt.Errorf("the path %q is missing", d)
			case strings.HasPrefix(sw[j], "-"):
				return flagProblem(dw, sw[j])
			default:
				return fmt.Errorf("%q is not %q or a path beneath it", sw[j], d)
			}
			continue
		}
		switch {
		case j == len(sw):
			return fmt.Errorf("%q is missing", d)
		case sw[j] == d:
			j++
		case i == 0:
			return fmt.Errorf("the program is %q, not %q", sw[j], d)
		case strings.HasPrefix(sw[j], "-"):
			return flagProblem(dw, sw[j])
		case strings.HasPrefix(d, "-"):
			return fmt.Errorf("flag %s is missing, found %q", d, sw[j])
		default:
			return fmt.Errorf("found %q where the declared command has %q", sw[j], d)
		}
	}
	for ; j < len(sw); j++ {
		switch {
		case hasPath:
			return fmt.Errorf("%q is extra: the declared command allows nothing but a narrower path", sw[j])
		case strings.HasPrefix(sw[j], "-"):
			return flagProblem(dw, sw[j])
		}
		if _, ok := scopeDir(sw[j]); !ok {
			return fmt.Errorf("%q cannot be appended: only a relative path, with no .. or leading dash", sw[j])
		}
	}
	return nil
}

// flagProblem explains a flag the scope has where the declared command does not.
func flagProblem(dw []string, flag string) error {
	if slices.Contains(dw, flag) {
		return fmt.Errorf("flag %s is out of order", flag)
	}
	return fmt.Errorf("flag %s is not in the declared command", flag)
}
