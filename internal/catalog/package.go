package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
)

// Package is an umrah shop's package details (BR-171), stored as
// products.package with every field present: unset ones null, unset lists
// empty, so the storefront returns it as it is.
type Package struct {
	DepartureDate      *string  `json:"departure_date"`
	DepartureText      *string  `json:"departure_text"`
	ReturnDate         *string  `json:"return_date"`
	DurationDays       *int     `json:"duration_days"`
	Airline            *string  `json:"airline"`
	Route              *string  `json:"route"`
	Hotels             []Hotel  `json:"hotels"`
	SeatsLeft          *int     `json:"seats_left"`
	SeatsTotal         *int     `json:"seats_total"`
	Itinerary          []Day    `json:"itinerary"`
	Includes           []string `json:"includes"`
	Excludes           []string `json:"excludes"`
	Bonus              []string `json:"bonus"`
	PriceNote          *string  `json:"price_note"`
	CancellationPolicy *string  `json:"cancellation_policy"`
}

// Hotel is one stay; Variant names the option value whose choice uses it, nil
// for every choice.
type Hotel struct {
	City    string  `json:"city"`
	Name    string  `json:"name"`
	Stars   *int    `json:"stars"`
	Nights  *int    `json:"nights"`
	Variant *string `json:"variant"`
}

// Day is one day of the itinerary; day N is the Nth.
type Day struct {
	Title string  `json:"title"`
	Text  *string `json:"text"`
}

// ParsePackage reads a package from a request body and checks it as BR-171
// says: an unknown field or a broken rule is 422 on package.{field}. Text is
// trimmed, and empty text is unset.
func ParsePackage(raw json.RawMessage) (*Package, error) {
	var p Package
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, packageDecodeError(err)
	}
	if err := p.normalize(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadPackage reads a stored package; nil for none.
func LoadPackage(stored []byte) *Package {
	if len(stored) == 0 {
		return nil
	}
	var p Package
	if json.Unmarshal(stored, &p) != nil {
		return nil
	}
	p.lists()
	return &p
}

// packageDecodeError names the field a body got wrong: an unknown one, or one
// of the wrong JSON type.
func packageDecodeError(err error) error {
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) && typ.Field != "" {
		return packageError(typ.Field, "has the wrong type")
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return packageError(strings.Trim(name, `"`), "is not a package field")
	}
	return packageError("", "must be an object of package details")
}

func packageError(field, detail string) error {
	name := "package"
	if field != "" {
		name += "." + field
	}
	return apperrors.ValidationFailed(name + " " + detail).WithFields(apperrors.Field{Name: name, Detail: detail})
}

func (p *Package) lists() {
	for _, l := range []*[]string{&p.Includes, &p.Excludes, &p.Bonus} {
		if *l == nil {
			*l = []string{}
		}
	}
	if p.Hotels == nil {
		p.Hotels = []Hotel{}
	}
	if p.Itinerary == nil {
		p.Itinerary = []Day{}
	}
}

// normalize trims and checks every field against BR-171.
func (p *Package) normalize() error {
	p.lists()
	for _, f := range []struct {
		name string
		v    **string
		max  int
	}{
		{"departure_text", &p.DepartureText, 40}, {"airline", &p.Airline, 80}, {"route", &p.Route, 120},
		{"price_note", &p.PriceNote, 160}, {"cancellation_policy", &p.CancellationPolicy, 3000},
	} {
		if err := optionalText(f.name, f.v, f.max); err != nil {
			return err
		}
	}
	var dep, ret time.Time
	for _, f := range []struct {
		name string
		v    **string
		at   *time.Time
	}{{"departure_date", &p.DepartureDate, &dep}, {"return_date", &p.ReturnDate, &ret}} {
		if err := optionalText(f.name, f.v, 10); err != nil {
			return err
		}
		if *f.v == nil {
			continue
		}
		t, err := time.Parse(time.DateOnly, **f.v)
		if err != nil {
			return packageError(f.name, "is a date, YYYY-MM-DD")
		}
		*f.at = t
	}
	if !dep.IsZero() && !ret.IsZero() && ret.Before(dep) {
		return packageError("return_date", "is not before the departure")
	}
	if err := between("duration_days", p.DurationDays, 1, 60); err != nil {
		return err
	}
	if err := between("seats_left", p.SeatsLeft, 0, 1_000_000); err != nil {
		return err
	}
	if err := between("seats_total", p.SeatsTotal, 0, 1_000_000); err != nil {
		return err
	}
	if p.SeatsLeft != nil && p.SeatsTotal != nil && *p.SeatsLeft > *p.SeatsTotal {
		return packageError("seats_left", "is not above seats_total")
	}
	if len(p.Hotels) > 10 {
		return packageError("hotels", "has at most 10 hotels")
	}
	for i := range p.Hotels {
		h, at := &p.Hotels[i], "hotels."+strconv.Itoa(i)+"."
		if err := requiredText(at+"city", &h.City, 40); err != nil {
			return err
		}
		if err := requiredText(at+"name", &h.Name, 120); err != nil {
			return err
		}
		if err := between(at+"stars", h.Stars, 1, 5); err != nil {
			return err
		}
		if err := between(at+"nights", h.Nights, 1, 60); err != nil {
			return err
		}
		if err := optionalText(at+"variant", &h.Variant, 100); err != nil {
			return err
		}
	}
	if len(p.Itinerary) > 40 {
		return packageError("itinerary", "has at most 40 days")
	}
	for i := range p.Itinerary {
		d, at := &p.Itinerary[i], "itinerary."+strconv.Itoa(i)+"."
		if err := requiredText(at+"title", &d.Title, 120); err != nil {
			return err
		}
		if err := optionalText(at+"text", &d.Text, 1500); err != nil {
			return err
		}
	}
	for _, l := range []struct {
		name string
		v    []string
	}{{"includes", p.Includes}, {"excludes", p.Excludes}, {"bonus", p.Bonus}} {
		if len(l.v) > 30 {
			return packageError(l.name, "has at most 30 entries")
		}
		for i := range l.v {
			if err := requiredText(l.name+"."+strconv.Itoa(i), &l.v[i], 200); err != nil {
				return err
			}
		}
	}
	return nil
}

func optionalText(name string, v **string, max int) error {
	if *v == nil {
		return nil
	}
	t := strings.TrimSpace(**v)
	if t == "" {
		*v = nil
		return nil
	}
	if utf8.RuneCountInString(t) > max {
		return packageError(name, "is at most "+strconv.Itoa(max)+" characters")
	}
	*v = &t
	return nil
}

func requiredText(name string, v *string, max int) error {
	*v = strings.TrimSpace(*v)
	if *v == "" {
		return packageError(name, "is required")
	}
	if utf8.RuneCountInString(*v) > max {
		return packageError(name, "is at most "+strconv.Itoa(max)+" characters")
	}
	return nil
}

func between(name string, v *int, lo, hi int) error {
	if v != nil && (*v < lo || *v > hi) {
		return packageError(name, "is from "+strconv.Itoa(lo)+" to "+strconv.Itoa(hi))
	}
	return nil
}
