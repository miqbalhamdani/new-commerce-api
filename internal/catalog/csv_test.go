package catalog

import (
	"strings"
	"testing"
)

func TestReadCSV(t *testing.T) {
	mapping := map[string]string{"Nama Produk": "title", "SKU": "sku", "Harga": "regular_price", "Warna": "option:Colour"}
	for name, file := range map[string]string{
		"comma":             "Nama Produk,SKU,Harga,Warna,Catatan\nKaos,TS-1,199000,Hitam,x\n\nKaos,TS-2,\"199.000\",Putih,y\n",
		"semicolon and BOM": "\xef\xbb\xbfNama Produk;SKU;Harga;Warna;Catatan\nKaos;TS-1;199000;Hitam;x\n;;;;\nKaos;TS-2;199.000;Putih;y\n",
	} {
		t.Run(name, func(t *testing.T) {
			header, rows, err := readCSV(strings.NewReader(file), mapping)
			if err != nil {
				t.Fatal(err)
			}
			if header[0] != "Nama Produk" || len(rows) != 2 {
				t.Fatalf("header %q rows %d", header, len(rows))
			}
			if rows[0].Line != 2 || rows[1].Line != 4 {
				t.Errorf("lines %d, %d: want the file's own numbers 2 and 4", rows[0].Line, rows[1].Line)
			}
			if rows[1].Values["sku"] != "TS-2" || rows[1].Values["option:Colour"] != "Putih" || rows[1].Values["Catatan"] != "" {
				t.Errorf("values %v", rows[1].Values)
			}
		})
	}
}

func TestParseRupiah(t *testing.T) {
	for in, want := range map[string]int64{"199000": 19900000, "199.000": 19900000, "199,000": 19900000,
		"Rp 1.299.000": 129900000, "0": 0} {
		got, err := parseRupiah(in)
		if err != nil || got != want {
			t.Errorf("parseRupiah(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-5", "12x"} {
		if _, err := parseRupiah(bad); err == nil {
			t.Errorf("parseRupiah(%q) accepted", bad)
		}
	}
}
