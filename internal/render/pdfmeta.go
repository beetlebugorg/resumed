package render

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

// Producer is written into the PDF Info dictionary of every document resumed
// generates.
const Producer = "resumed"

// typst writes /Creator and leaves /Producer unset, and its compiler exposes no
// option for either. setProducer therefore edits the finished file, appending a
// PDF incremental update: a second copy of the Info object with /Producer
// added, a cross-reference section covering that one object, and a trailer
// whose /Prev points at the cross-reference section already in the file. The
// original bytes are untouched, so a reader that stops at the first trailer
// still sees a valid document.
//
// This handles the files typst produces today: a classic cross-reference table
// with the Info dictionary as a plain uncompressed object. A cross-reference
// stream or an Info dictionary inside an object stream is left alone rather
// than guessed at.
func setProducer(path, producer string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	trailer, err := lastTrailer(data)
	if err != nil {
		return err
	}
	infoNum, err := dictRef(trailer, "Info")
	if err != nil {
		return fmt.Errorf("trailer has no /Info reference: %w", err)
	}
	startxref, err := lastStartxref(data)
	if err != nil {
		return err
	}

	objPat := regexp.MustCompile(`(?s)(^|[\r\n])` + strconv.Itoa(infoNum) + ` 0 obj\s*<<(.*?)>>\s*endobj`)
	all := objPat.FindAllSubmatchIndex(data, -1)
	if len(all) == 0 {
		return fmt.Errorf("info object %d is not a plain dictionary", infoNum)
	}
	// An incremental update appends a newer copy of an object, and the last one
	// in the file is the one in force. Reading the first would miss a /Producer
	// added by an earlier call and append a second copy on every run.
	m := all[len(all)-1]
	body := data[m[4]:m[5]]
	if bytes.Contains(body, []byte("/Producer")) {
		return nil
	}

	var out bytes.Buffer
	out.Write(data)
	if !bytes.HasSuffix(data, []byte("\n")) {
		out.WriteByte('\n')
	}

	offset := out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n<<%s/Producer(%s)>>\nendobj\n", infoNum, body, escapePDFString(producer))

	xref := out.Len()
	// One subsection covering the single object this update replaces. Entries
	// are exactly 20 bytes, trailing space included.
	fmt.Fprintf(&out, "xref\n%d 1\n%010d 00000 n \n", infoNum, offset)
	fmt.Fprintf(&out, "trailer\n<<%s/Prev %d>>\nstartxref\n%d\n%%%%EOF\n", trailer, startxref, xref)

	return os.WriteFile(path, out.Bytes(), 0o644)
}

// lastTrailer returns the contents of the final trailer dictionary, without its
// enclosing angle brackets.
func lastTrailer(data []byte) ([]byte, error) {
	i := bytes.LastIndex(data, []byte("trailer"))
	if i < 0 {
		return nil, fmt.Errorf("no trailer: the file may use a cross-reference stream")
	}
	open := bytes.Index(data[i:], []byte("<<"))
	if open < 0 {
		return nil, fmt.Errorf("trailer has no dictionary")
	}
	open += i + 2
	depth := 1
	for j := open; j+1 < len(data); j++ {
		switch {
		case data[j] == '<' && data[j+1] == '<':
			depth++
			j++
		case data[j] == '>' && data[j+1] == '>':
			depth--
			if depth == 0 {
				return data[open:j], nil
			}
			j++
		}
	}
	return nil, fmt.Errorf("trailer dictionary is unterminated")
}

var startxrefPat = regexp.MustCompile(`startxref\s+(\d+)`)

func lastStartxref(data []byte) (int, error) {
	all := startxrefPat.FindAllSubmatch(data, -1)
	if len(all) == 0 {
		return 0, fmt.Errorf("no startxref")
	}
	return strconv.Atoi(string(all[len(all)-1][1]))
}

// dictRef reads an indirect reference such as "/Info 244 0 R" and returns its
// object number.
func dictRef(dict []byte, key string) (int, error) {
	pat := regexp.MustCompile(`/` + key + `\s+(\d+)\s+\d+\s+R`)
	m := pat.FindSubmatch(dict)
	if m == nil {
		return 0, fmt.Errorf("key /%s not found", key)
	}
	return strconv.Atoi(string(m[1]))
}

// escapePDFString escapes the three characters that end or nest a PDF literal
// string.
func escapePDFString(s string) string {
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '(', ')':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
