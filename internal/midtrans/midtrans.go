// Package midtrans talks to Midtrans for the shop that owns the payment: each
// shop has its own account, keys and environment (BR-122, BR-123). It holds no
// state and touches no database.
package midtrans

// Hosts per environment. Snap is the hosted payment page; the core API answers
// Get Status (BR-124).
var hosts = map[string]struct{ snap, api string }{
	"sandbox":    {snap: "https://app.sandbox.midtrans.com", api: "https://api.sandbox.midtrans.com"},
	"production": {snap: "https://app.midtrans.com", api: "https://api.midtrans.com"},
}

// SnapJS is the script a website loads to open Snap for environment.
func SnapJS(environment string) string { return hosts[environment].snap + "/snap/snap.js" }
