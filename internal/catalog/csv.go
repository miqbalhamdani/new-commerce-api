package catalog

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// importRow is one data row of an import file: where it was, what it said,
// and the mapped values by target ("title", "sku", "option:Colour", …).
type importRow struct {
	Line   int
	Raw    []string
	Values map[string]string
}

// readCSV parses an import file (BR-044): a UTF-8 BOM is dropped, the
// delimiter is ',' or ';' -- whichever the header has more of, since an
// Indonesian Excel export uses ';' -- and each row keeps its original line
// number for errors.csv. mapping is header -> target; unmapped columns are
// kept in Raw only.
func readCSV(r io.Reader, mapping map[string]string) (header []string, rows []importRow, err error) {
	br := bufio.NewReader(r)
	if b, _ := br.Peek(3); bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	first, _ := br.Peek(4096)
	firstLine, _, _ := bytes.Cut(first, []byte("\n"))
	comma := ','
	if bytes.Count(firstLine, []byte(";")) > bytes.Count(firstLine, []byte(",")) {
		comma = ';'
	}

	cr := csv.NewReader(br)
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes = comma, -1, true
	header, err = cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("the file is empty")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read header: %w", err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	targets := make([]string, len(header))
	for i, h := range header {
		targets[i] = mapping[h]
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return header, rows, nil
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", line, err)
		}
		if blank(rec) {
			continue
		}
		row := importRow{Line: line, Raw: rec, Values: map[string]string{}}
		for i, v := range rec {
			if i < len(targets) && targets[i] != "" {
				row.Values[targets[i]] = strings.TrimSpace(v)
			}
		}
		rows = append(rows, row)
	}
}

func blank(rec []string) bool {
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// parseRupiah reads a whole-rupiah amount as the spreadsheet shows it --
// "199000", "199.000", "199,000", "Rp 199.000" -- into minor units (×100,
// BR-006). Separators are thousands separators: rupiah has no cents in
// practice, so "199.000" can only mean a hundred and ninety-nine thousand.
func parseRupiah(s string) (int64, error) {
	clean := strings.NewReplacer("Rp", "", "rp", "", "IDR", "", ".", "", ",", "", " ", "").Replace(s)
	n, err := strconv.ParseInt(clean, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not an amount in rupiah", s)
	}
	return n * 100, nil
}

func parseGrams(s string) (int32, error) {
	n, err := strconv.ParseInt(strings.NewReplacer(".", "", ",", "", " ", "", "g", "").Replace(s), 10, 32)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a weight in grams", s)
	}
	return int32(n), nil
}
