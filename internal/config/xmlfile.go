package config

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// rootElement is the name of config.xml's root element (like Servarr's CONFIG_ELEMENT_NAME).
const rootElement = "Config"

// maxFileSize bounds how much of config.xml is read; a real file is well under 2 KiB.
const maxFileSize = 1 << 20

// node is one child of <Config> in document order: either a known setting (field ≥ 0), whose
// value is rendered from a Config on save, or anything else (field < 0) kept byte-for-byte.
type node struct {
	field int
	raw   []byte
}

// document is the parsed structure of config.xml. It is immutable once Load returns: values live
// in Config, the document only remembers element order and the unknown content to round-trip.
type document struct {
	nodes []node
}

// parsed is the result of reading config.xml.
type parsed struct {
	doc    *document
	values map[int]string // field index → trimmed text of its (first) element
	dirty  bool           // the file must be rewritten (duplicates dropped, names canonicalised)
}

// newDocument returns an empty document (a new config.xml).
func newDocument() *document { return &document{} }

// parseDocument parses config.xml. Known elements are matched case-insensitively; when an element
// occurs more than once only one is kept (Servarr keeps appending duplicates, see
// docs/research/arr-conventions.md §1.1): the first non-blank value of a known element, the first
// occurrence of an unknown one. Unknown elements, comments and processing instructions inside
// <Config> are preserved verbatim. Anything that is not well-formed XML with a single <Config>
// root (optionally followed by comments/processing instructions) is an error.
func parseDocument(data []byte) (*parsed, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	if err := findRoot(dec); err != nil {
		return nil, err
	}

	p := &parsed{doc: &document{}, values: make(map[int]string)}
	seenUnknown := make(map[string]bool)
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("missing </%s>", rootElement)
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement: // </Config>
			if err := checkEpilog(dec); err != nil {
				return nil, err
			}
			return p, nil
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("unexpected text %q inside <%s>", truncate(string(bytes.TrimSpace(t)), 40), rootElement)
			}
		case xml.StartElement:
			if err := p.element(dec, t, data, start, seenUnknown); err != nil {
				return nil, err
			}
		default: // comments, processing instructions, directives
			p.doc.nodes = append(p.doc.nodes, node{field: -1, raw: bytes.Clone(data[start:dec.InputOffset()])})
		}
	}
}

// checkEpilog verifies that only whitespace, comments and processing instructions follow the root
// element. Anything else (a second <Config>, stray text) would be silently dropped by the next
// save, so it is reported instead.
func checkEpilog(dec *xml.Decoder) error {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.Comment, xml.ProcInst:
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("unexpected text %q after </%s>", truncate(string(bytes.TrimSpace(t)), 40), rootElement)
			}
		default:
			return fmt.Errorf("unexpected content after </%s>", rootElement)
		}
	}
}

// findRoot advances dec past the <Config> start tag, skipping a prolog (declaration, comments).
func findRoot(dec *xml.Decoder) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("no <%s> root element", rootElement)
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != rootElement {
				return fmt.Errorf("root element is <%s>, expected <%s>", t.Name.Local, rootElement)
			}
			return nil
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("unexpected text before <%s>", rootElement)
			}
		}
	}
}

// element handles one child element of <Config> that started at byte offset start.
func (p *parsed) element(dec *xml.Decoder, t xml.StartElement, data []byte, start int64, seenUnknown map[string]bool) error {
	idx := fieldIndexByXML(t.Name.Local)
	if idx < 0 {
		if err := dec.Skip(); err != nil {
			return err
		}
		key := strings.ToLower(t.Name.Local)
		if seenUnknown[key] {
			p.dirty = true // duplicate unknown element: keep the first only
			return nil
		}
		seenUnknown[key] = true
		p.doc.nodes = append(p.doc.nodes, node{field: -1, raw: bytes.Clone(data[start:dec.InputOffset()])})
		return nil
	}

	var v struct {
		Text string `xml:",chardata"`
	}
	if err := dec.DecodeElement(&v, &t); err != nil {
		return err
	}
	text := strings.TrimSpace(v.Text)
	if prev, dup := p.values[idx]; dup {
		// Duplicate known element: the first non-blank value wins (so a blank <ApiKey/> ahead of
		// the real one cannot cause a new key to be generated); the extras are dropped on save.
		p.dirty = true
		if prev == "" && text != "" {
			p.values[idx] = text
		}
		return nil
	}
	if t.Name.Local != fields[idx].xml || t.Name.Space != "" {
		p.dirty = true // rewrite with the canonical element name
	}
	p.values[idx] = text
	p.doc.nodes = append(p.doc.nodes, node{field: idx})
	return nil
}

// render serialises the document with the known values taken from c, Servarr style: no XML
// declaration, <Config> root, one element per line indented by two spaces.
func (d *document) render(c *Config) []byte {
	var b bytes.Buffer
	b.WriteString("<" + rootElement + ">\n")
	for _, n := range d.nodes {
		b.WriteString("  ")
		if n.field < 0 {
			b.Write(n.raw)
		} else {
			f := fields[n.field]
			b.WriteString("<" + f.xml + ">")
			_ = xml.EscapeText(&b, []byte(f.format(c))) // writes to a bytes.Buffer never fail
			b.WriteString("</" + f.xml + ">")
		}
		b.WriteByte('\n')
	}
	b.WriteString("</" + rootElement + ">\n")
	return b.Bytes()
}

// utf8BOM is the byte order mark some Windows editors put in front of UTF-8 files.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// readConfigFile reads path, refusing implausibly large files.
func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maxFileSize)
	}
	return data, nil
}

// writeFileAtomic replaces path with data (mode 0600) so that readers only ever see the old or
// the new complete file: write a temp file in the same directory, fsync, rename, fsync the dir.
// When path is a symlink, the file it points to is replaced and the link is kept.
func writeFileAtomic(path string, data []byte) (err error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	syncDir(dir)
	return nil
}

// syncDir makes a rename durable. Best effort: not every platform supports syncing directories.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
