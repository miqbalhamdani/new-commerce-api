package config

import (
	"time"
	_ "time/tzdata" // the zone must load on a host or image with no zoneinfo
)

// WIB is Asia/Jakarta, UTC+7: the zone of the database session and of every
// timestamp on the wire (BR-007). A shop elsewhere in Indonesia still gets WIB
// from the API; the admin converts for display.
var WIB = mustLoad("Asia/Jakarta")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // embedded tzdata makes this unreachable
	}
	return loc
}
