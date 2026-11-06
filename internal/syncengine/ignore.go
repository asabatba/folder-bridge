package syncengine

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ignoreFile is included in the v2 manifest so both computers can honor
// both sets of ignore rules. Contents are capped to prevent excess memory use.
type ignoreFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type ignoreRule struct {
	base          string
	regex         *regexp.Regexp
	negate        bool
	directoryOnly bool
	source        string
	line          int
	pattern       string
}

type ignoreMatcher struct {
	files []ignoreFile
	rules []ignoreRule
}

const maxIgnoreFileSize = 64 << 10
const maxIgnorePolicySize = 1 << 20

func isIgnoreFilename(name string) bool { return name == ".gitignore" || name == ".fbignore" }

func isInternalPath(rel string) bool {
	for _, component := range strings.Split(rel, "/") {
		if component == ".git" || strings.HasPrefix(component, ".fb-part-") {
			return true
		}
	}
	return false
}

func newIgnoreMatcher(files []ignoreFile) (*ignoreMatcher, error) {
	ordered := append([]ignoreFile(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i].Path, ordered[j].Path
		da, db := strings.Count(a, "/"), strings.Count(b, "/")
		if da != db {
			return da < db
		}
		if path.Dir(a) != path.Dir(b) {
			return path.Dir(a) < path.Dir(b)
		}
		return a < b
	})
	m := &ignoreMatcher{}
	return m, m.appendFiles(ordered)
}

func (m *ignoreMatcher) appendFiles(files []ignoreFile) error {
	total := 0
	seen := make(map[string]bool)
	for _, f := range m.files {
		total += len(f.Text)
		seen[f.Path] = true
	}
	for _, f := range files {
		if strings.Contains(f.Path, "\\") || path.Clean(f.Path) != f.Path || strings.HasPrefix(f.Path, "/") || f.Path == ".." || strings.HasPrefix(f.Path, "../") || !isIgnoreFilename(path.Base(f.Path)) || isInternalPath(f.Path) || f.Path == "" {
			return fmt.Errorf("invalid ignore-file path %q", f.Path)
		}
		if seen[f.Path] {
			return fmt.Errorf("duplicate ignore file %q", f.Path)
		}
		seen[f.Path] = true
		if len(f.Text) > maxIgnoreFileSize {
			return fmt.Errorf("ignore file %q exceeds 64 KiB", f.Path)
		}
		total += len(f.Text)
		if total > maxIgnorePolicySize {
			return fmt.Errorf("ignore policies exceed 1 MiB")
		}
		base := path.Dir(f.Path)
		if base == "." {
			base = ""
		}
		scanner := bufio.NewScanner(strings.NewReader(f.Text))
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := strings.TrimSuffix(scanner.Text(), "\r")
			rule, ok, err := parseIgnoreRule(base, line)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Path, err)
			}
			if ok {
				rule.source = f.Path
				rule.line = lineNumber
				rule.pattern = line
				m.rules = append(m.rules, rule)
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		m.files = append(m.files, f)
	}
	return nil
}

func parseIgnoreRule(base, line string) (ignoreRule, bool, error) {
	// Git ignores trailing spaces unless escaped with a backslash.
	for len(line) > 0 && line[len(line)-1] == ' ' {
		backslashes := 0
		for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			line = line[:len(line)-2] + " "
			break
		}
		line = line[:len(line)-1]
	}
	if line == "" || line[0] == '#' {
		return ignoreRule{}, false, nil
	}
	negated := false
	if line[0] == '!' {
		negated = true
		line = line[1:]
	}
	if line == "" {
		return ignoreRule{}, false, nil
	}
	directoryOnly := strings.HasSuffix(line, "/") && !strings.HasSuffix(line, "\\/")
	if directoryOnly {
		line = strings.TrimSuffix(line, "/")
	}
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimPrefix(line, "/")
	if line == "" {
		return ignoreRule{}, false, nil
	}
	hasSlash := strings.Contains(line, "/")
	expr, err := globRegex(line)
	if err != nil {
		return ignoreRule{}, false, err
	}
	if hasSlash || anchored {
		expr = "^" + expr + "$"
	} else {
		expr = "(?:^|/)" + expr + "$"
	}
	compiled, err := regexp.Compile(expr)
	if err != nil {
		return ignoreRule{}, false, err
	}
	return ignoreRule{base: base, regex: compiled, negate: negated, directoryOnly: directoryOnly}, true, nil
}

func globRegex(glob string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(glob); {
		c := glob[i]
		switch c {
		case '\\':
			i++
			if i < len(glob) {
				out.WriteString(regexp.QuoteMeta(string(glob[i])))
				i++
			} else {
				out.WriteString(`\\`)
			}
		case '*':
			j := i
			for j < len(glob) && glob[j] == '*' {
				j++
			}
			if j-i >= 2 {
				if j < len(glob) && glob[j] == '/' {
					out.WriteString("(?:.*/)?")
					i = j + 1
				} else {
					out.WriteString(".*")
					i = j
				}
			} else {
				out.WriteString("[^/]*")
				i++
			}
		case '?':
			out.WriteString("[^/]")
			i++
		case '[':
			j := i + 1
			if j < len(glob) && (glob[j] == '!' || glob[j] == '^') {
				j++
			}
			if j < len(glob) && glob[j] == ']' {
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				j++
			}
			if j == len(glob) {
				out.WriteString(`\[`)
				i++
				continue
			}
			charClass := glob[i+1 : j]
			if strings.HasPrefix(charClass, "!") {
				charClass = "^" + charClass[1:]
			}
			out.WriteString("[" + charClass + "]")
			i = j + 1
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	return out.String(), nil
}

// isIgnored honors last matching rule, with nested ignore files overriding
// parent rules. A negation cannot resurrect a file whose parent is ignored.
func (m *ignoreMatcher) isIgnored(rel string, isDir bool) bool {
	if isInternalPath(rel) {
		return true
	}
	if m == nil {
		return false
	}
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		candidate := strings.Join(parts[:i], "/")
		candidateDir := i < len(parts) || isDir
		ignored := false
		for _, rule := range m.rules {
			sub := candidate
			if rule.base != "" {
				if !strings.HasPrefix(candidate, rule.base+"/") {
					continue
				}
				sub = strings.TrimPrefix(candidate, rule.base+"/")
			}
			if rule.directoryOnly && !candidateDir {
				continue
			}
			if rule.regex.MatchString(sub) {
				ignored = !rule.negate
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

// IgnoreDecision describes the decisive rule (or why an internal path is
// always excluded). A negation is reported as an explicit allow decision.
type IgnoreDecision struct {
	Path     string `json:"path"`
	Ignored  bool   `json:"ignored"`
	Internal bool   `json:"internal,omitempty"`
	Source   string `json:"source,omitempty"`
	Line     int    `json:"line,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Ancestor string `json:"ancestor,omitempty"`
	Reason   string `json:"reason"`
}

func (m *ignoreMatcher) Explain(rel string, isDir bool) IgnoreDecision {
	result := IgnoreDecision{Path: rel, Reason: "No matching ignore rule"}
	if isInternalPath(rel) {
		result.Ignored = true
		result.Internal = true
		result.Source = "FolderBridge internal policy"
		result.Reason = "Internal .git or temporary path (always excluded)"
		return result
	}
	if m == nil {
		return result
	}
	parts := strings.Split(rel, "/")
	for i := 1; i <= len(parts); i++ {
		candidate := strings.Join(parts[:i], "/")
		candidateDir := i < len(parts) || isDir
		ignored := false
		var matched *ignoreRule
		for j := range m.rules {
			rule := &m.rules[j]
			sub := candidate
			if rule.base != "" {
				if !strings.HasPrefix(candidate, rule.base+"/") {
					continue
				}
				sub = strings.TrimPrefix(candidate, rule.base+"/")
			}
			if rule.directoryOnly && !candidateDir {
				continue
			}
			if rule.regex.MatchString(sub) {
				ignored = !rule.negate
				matched = rule
			}
		}
		if matched != nil {
			result.Source = matched.source
			result.Line = matched.line
			result.Pattern = matched.pattern
			result.Ancestor = candidate
			result.Ignored = ignored
			if ignored {
				result.Reason = "Excluded by matching rule"
			} else {
				result.Reason = "Allowed by negation rule"
			}
		}
		if ignored {
			if candidate != rel {
				result.Reason = "Parent directory excluded; child negation cannot override it"
			}
			return result
		}
	}
	result.Ignored = false
	return result
}

func readIgnoreFilesInDir(root, rel string) ([]ignoreFile, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	var out []ignoreFile
	for _, filename := range []string{".fbignore", ".gitignore"} {
		full := filepath.Join(dir, filename)
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxIgnoreFileSize {
			return nil, fmt.Errorf("%s exceeds 64 KiB", full)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		name := filename
		if rel != "" {
			name = rel + "/" + filename
		}
		out = append(out, ignoreFile{Path: name, Text: string(data)})
	}
	return out, nil
}

// pathIgnoreChecker caches compiled rules for folders within one network request.
// The caller holds h.fileMu, so rule files do not change through this peer.
type pathIgnoreChecker struct {
	root  string
	cache map[string]*ignoreMatcher
}

func newPathIgnoreChecker(root string) *pathIgnoreChecker {
	return &pathIgnoreChecker{root: root, cache: make(map[string]*ignoreMatcher)}
}

func (c *pathIgnoreChecker) matcher(dir string) (*ignoreMatcher, error) {
	if m, ok := c.cache[dir]; ok {
		return m, nil
	}
	var files []ignoreFile
	if dir != "" {
		parent := path.Dir(dir)
		if parent == "." {
			parent = ""
		}
		m, err := c.matcher(parent)
		if err != nil {
			return nil, err
		}
		files = append(files, m.files...)
	}
	chunk, err := readIgnoreFilesInDir(c.root, dir)
	if err != nil {
		return nil, err
	}
	files = append(files, chunk...)
	matcher, err := newIgnoreMatcher(files)
	if err != nil {
		return nil, err
	}
	c.cache[dir] = matcher
	return matcher, nil
}

func (c *pathIgnoreChecker) isIgnored(rel string) (bool, error) {
	if isInternalPath(rel) {
		return true, nil
	}
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	matcher, err := c.matcher(dir)
	if err != nil {
		return false, err
	}
	return matcher.isIgnored(rel, false), nil
}

// localIgnoreForPath checks policies along the path's ancestor directories.
func localIgnoreForPath(root, rel string) (bool, error) {
	return newPathIgnoreChecker(root).isIgnored(rel)
}
