package orders

import "testing"

// TestRupiah: amounts in an email read the way an Indonesian writes them
// (BR-006).
func TestRupiah(t *testing.T) {
	for minor, want := range map[int64]string{
		0: "Rp 0", 100: "Rp 1", 1500000: "Rp 15.000", 19900000: "Rp 199.000", 19900050: "Rp 199.000,50",
		100000000000: "Rp 1.000.000.000", -2000000: "-Rp 20.000",
	} {
		if got := Rupiah(minor); got != want {
			t.Errorf("Rupiah(%d) = %q, want %q", minor, got, want)
		}
	}
}
