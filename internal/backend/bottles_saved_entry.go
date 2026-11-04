package backend

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

// SavedWindowsEntry is portable user intent. Paths are relative to the Bottle,
// never an upstream auto-discovery UUID. Existing registrations remain readable.
type SavedWindowsEntry struct {
	Path string   `json:"path"`
	CWD  string   `json:"cwd"`
	Args []string `json:"args,omitempty"`
}

type WindowsEntryOptions struct {
	CWD  string
	Args []string
}

func quotedProgramArgs(args []string) (string, error) {
	if len(args) > 64 {
		return "", errors.New("too many application arguments")
	}
	quoted := make([]string, 0, len(args))
	total := 0
	for _, arg := range args {
		total += len(arg)
		if total > 8192 || strings.ContainsAny(arg, "\x00\r\n") {
			return "", errors.New("invalid application argument")
		}
		// Bottles passes this string through its command parser. Quote every argument;
		// do not let shell operators or expansions acquire command semantics.
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return strings.Join(quoted, " "), nil
}
func newSavedEntry(root, entry string, options WindowsEntryOptions) (*SavedWindowsEntry, error) {
	if _, e := quotedProgramArgs(options.Args); e != nil {
		return nil, e
	}
	cwd := options.CWD
	if cwd == "" {
		cwd = filepath.Dir(entry)
	}
	// The public Bottles add CLI saves the executable's parent as its working
	// directory. Do not silently drop a different requested directory.
	if cwd != filepath.Dir(entry) {
		return nil, errors.New("this Bottles CLI supports the executable directory as working directory; a different directory is not silently substituted")
	}
	rel, e := filepath.Rel(root, entry)
	if e != nil || !filepath.IsLocal(rel) {
		return nil, errors.New("entry escaped Bottle")
	}
	wd, e := filepath.Rel(root, cwd)
	if e != nil || !filepath.IsLocal(wd) {
		return nil, errors.New("working directory escaped Bottle")
	}
	return &SavedWindowsEntry{Path: filepath.ToSlash(rel), CWD: filepath.ToSlash(wd), Args: append([]string(nil), options.Args...)}, nil
}
func (s SavedWindowsEntry) verify(root, entry string, program map[string]json.RawMessage) error {
	if !filepath.IsLocal(s.Path) || !filepath.IsLocal(s.CWD) || strings.ContainsAny(s.Path+s.CWD, "\\:\x00\r\n") || filepath.Join(root, filepath.FromSlash(s.Path)) != entry || filepath.Join(root, filepath.FromSlash(s.CWD)) != filepath.Dir(entry) {
		return errors.New("saved entry path or working directory changed")
	}
	expected, e := quotedProgramArgs(s.Args)
	if e != nil {
		return e
	}
	var args, folder string
	if json.Unmarshal(program["arguments"], &args) != nil {
		args = ""
	}
	if args != expected {
		return errors.New("saved application arguments changed")
	}
	if raw := program["folder"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &folder) != nil || folder != filepath.Dir(entry) {
			return errors.New("saved application working directory changed")
		}
	}
	return nil
}

// Discovery arguments use Bottles' POSIX-style tokenization. Parsing never
// expands variables or executes text; saved arguments are individually quoted.
func discoveredProgramArgs(raw string) ([]string, error) {
	if len(raw) > 8192 {
		return nil, errors.New("program arguments too long")
	}
	var result []string
	var token strings.Builder
	quote := rune(0)
	escaped, started := false, false
	runes := []rune(raw)
	for i, c := range runes {
		if escaped {
			token.WriteRune(c)
			escaped = false
			started = true
			continue
		}
		if c == '\\' && quote != '\'' {
			if quote == '"' && i+1 < len(runes) && !strings.ContainsRune("$`\\\"\n", runes[i+1]) {
				token.WriteRune(c)
				continue
			}
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				token.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if unicode.IsSpace(c) {
			if started {
				result = append(result, token.String())
				token.Reset()
				started = false
			}
			continue
		}
		token.WriteRune(c)
		started = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("unsupported unclosed program arguments")
	}
	if started {
		result = append(result, token.String())
	}
	if _, e := quotedProgramArgs(result); e != nil {
		return nil, e
	}
	return result, nil
}
