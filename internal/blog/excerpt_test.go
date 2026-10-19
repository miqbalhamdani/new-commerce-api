package blog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// BR-132: the excerpt worked out from a post's body.
func TestExcerpt(t *testing.T) {
	long := "Seprai dan selimut merupakan sarana pelengkap istirahat Anda setelah menjalani hari yang " +
		"penat dan penuh aktivitas. Untuk itu, demi memenuhi kenyamanan dalam beristirahat hingga tidur " +
		"lelap, Anda harus memilih paket sprei yang tepat."

	cases := []struct{ name, body, want string }{
		{"empty", "", ""},
		{"short paragraph as is", "Tidur nyenyak itu penting.", "Tidur nyenyak itu penting."},
		{"skips headings and rules", "# Judul\n\n---\n\n## 1. Anggaran\n\nTentukan anggaran.", "Tentukan anggaran."},
		{"first paragraph only", "Satu.\n\nDua.", "Satu."},
		{"joins a paragraph's lines", "Baris satu\nbaris dua.", "Baris satu baris dua."},
		{"links keep their text", "Baca [panduan ukuran](/size-guide) dulu.", "Baca panduan ukuran dulu."},
		{"images go", "![kasur](/a.jpg) Kasur yang rapi.", "Kasur yang rapi."},
		{"emphasis and code go", "Pilih **TENCEL™** atau _katun_ dan `cuci` pelan.", "Pilih TENCEL™ atau katun dan cuci pelan."},
		{"quote and list markers go", "> 1. Tentukan anggaran\n> 2. Ukur kasur", "Tentukan anggaran Ukur kasur"},
		{"html goes", "Halo <b>dunia</b>.", "Halo dunia."},
		{"windows newlines", "# A\r\n\r\nIsi.", "Isi."},
		{"code fence skipped", "```\ncode\n```\n\nTeks.", "Teks."},
	}
	for _, c := range cases {
		if got := Excerpt(c.body); got != c.want {
			t.Errorf("%s: Excerpt(%q) = %q, want %q", c.name, c.body, got, c.want)
		}
	}

	got := Excerpt(long)
	if !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) > excerptRunes+1 {
		t.Errorf("long paragraph: %q is not cut at %d runes with an ellipsis", got, excerptRunes)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) || strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Errorf("long paragraph: %q is not cut on a word boundary", got)
	}

	written := "Pilih paket sprei yang tepat."
	if ResolveExcerpt(&written, long) != written {
		t.Error("a written excerpt wins")
	}
	blank := "  "
	if ResolveExcerpt(&blank, "Isi.") != "Isi." || ResolveExcerpt(nil, "Isi.") != "Isi." {
		t.Error("no written excerpt falls back to the body")
	}
}
