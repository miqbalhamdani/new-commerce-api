package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// TestParsePackage is BR-171's rules for a package from a request body.
func TestParsePackage(t *testing.T) {
	field := func(err error) string {
		var e *apperrors.Error
		if !errors.As(err, &e) || len(e.Fields) != 1 {
			return ""
		}
		return e.Fields[0].Name
	}
	for _, tt := range []struct{ body, field string }{
		{`{"flight":"x"}`, "package.flight"},
		{`{"seats_left":"ten"}`, "package.seats_left"},
		{`[]`, "package"},
		{`{"departure_date":"14-12-2026"}`, "package.departure_date"},
		{`{"departure_date":"2026-12-14","return_date":"2026-12-13"}`, "package.return_date"},
		{`{"seats_left":46,"seats_total":45}`, "package.seats_left"},
		{`{"seats_left":-1}`, "package.seats_left"},
		{`{"duration_days":61}`, "package.duration_days"},
		{`{"hotels":[{"city":"Makkah","name":" "}]}`, "package.hotels.0.name"},
		{`{"hotels":[{"city":"Makkah","name":"Hilton","stars":6}]}`, "package.hotels.0.stars"},
		{`{"itinerary":[{"title":""}]}`, "package.itinerary.0.title"},
		{`{"includes":["Visa",""]}`, "package.includes.1"},
		{`{"departure_text":"` + strings.Repeat("a", 41) + `"}`, "package.departure_text"},
	} {
		if _, err := ParsePackage(json.RawMessage(tt.body)); field(err) != tt.field {
			t.Errorf("%s: %v, want 422 on %s", tt.body, err, tt.field)
		}
	}

	p, err := ParsePackage(json.RawMessage(`{"departure_text":" Desember 2026 ","airline":"  ",
		"hotels":[{"city":"Makkah","name":"Hilton","variant":"Promo"}],"seats_left":0,"seats_total":45,
		"itinerary":[{"title":"Jakarta – Madinah","text":"Berangkat"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if *p.DepartureText != "Desember 2026" || p.Airline != nil || *p.SeatsLeft != 0 || *p.Hotels[0].Variant != "Promo" {
		t.Errorf("normalised: %+v", p)
	}
	// Stored, it has every field: unset ones null, unset lists empty.
	b, _ := json.Marshal(p)
	var stored map[string]any
	_ = json.Unmarshal(b, &stored)
	if len(stored) != 15 || stored["route"] != nil || stored["includes"] == nil || len(stored["includes"].([]any)) != 0 {
		t.Errorf("stored: %s", b)
	}
	if LoadPackage(nil) != nil || LoadPackage(b) == nil {
		t.Error("LoadPackage")
	}
}
