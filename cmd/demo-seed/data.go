package main

import (
	"image/color"
	"time"
)

// The demo shop's content: an Indonesian fashion shop, so a demo reads like a
// pilot merchant's admin. Prices are whole rupiah here and minor units once
// written (BR-006).

const day = 24 * time.Hour

type staffMember struct {
	name, local, role, status string
}

// The owner comes from -owner-email; the rest sign in at <local>@<slug>.example.com,
// unique across shops (BR-020) and never anyone's real mailbox. Nobody is
// invited through the service, so no invitation is ever sent.
var staff = []staffMember{
	{"Dewi Lestari", "dewi.lestari", "admin", "active"},
	{"Budi Santoso", "budi.santoso", "ops", "active"},
	{"Andi Pratama", "andi.pratama", "viewer", "active"},
	{"Rina Kurniawati", "rina.kurniawati", "ops", "invited"},
	{"Fajar Nugroho", "fajar.nugroho", "ops", "disabled"},
}

var brandNames = []string{"Nusantara Denim", "Kain Studio", "Langkah", "Rimba Outdoor", "Senja Aksesoris", "Batik Kita"}

// archivedBrand and archivedCategory are archived at the end, so both lists
// show what an archived row looks like.
const (
	archivedBrand    = "Kalea Basic"
	archivedCategory = "Promo 11.11"
)

type demoCategory struct {
	key, name, kind, parent string
}

// Parents before children; product definitions name categories by key.
var categories = []demoCategory{
	{"pria", "Pakaian Pria", "category", ""},
	{"kaos", "Kaos", "category", "pria"},
	{"kemeja", "Kemeja", "category", "pria"},
	{"celana", "Celana", "category", "pria"},
	{"wanita", "Pakaian Wanita", "category", ""},
	{"atasan", "Atasan", "category", "wanita"},
	{"dress", "Dress", "category", "wanita"},
	{"rok", "Rok", "category", "wanita"},
	{"sepatu", "Sepatu", "category", ""},
	{"sneakers", "Sneakers", "category", "sepatu"},
	{"sandal", "Sandal", "category", "sepatu"},
	{"aksesoris", "Aksesoris", "category", ""},
	{"tas", "Tas", "category", "aksesoris"},
	{"topi", "Topi", "category", "aksesoris"},
	{"lebaran", "Koleksi Lebaran", "collection", ""},
	{"bestseller", "Best Seller", "collection", ""},
	{"batik", "Seri Batik Modern", "series", ""},
	{"olahraga", "Olahraga & Outdoor", "activity", ""},
	{"promo", archivedCategory, "collection", ""},
}

type saleKind int

const (
	noSale        saleKind = iota
	saleNow                // 15% off, started 5 days ago, ends in 9
	saleScheduled          // 20% off from 3 days on
	saleOpen               // 10% off, no end date
)

type demoProduct struct {
	title, brand, sku, desc string
	cats                    []string
	attrs                   map[string]any
	colours, sizes          []string // the Warna and Ukuran axes; nil leaves one out
	tone                    string   // the card colour when there is no Warna axis
	price                   int64    // rupiah
	weight                  int32    // grams
	sale                    saleKind
	state                   string // active, draft or archived
	// gap is what keeps a draft from passing the publish check (BR-038):
	// "photo", "weight", "price" or "category". Empty is a draft that would pass.
	gap string
}

var (
	apparel = []string{"S", "M", "L", "XL"}
	shoes   = []string{"39", "40", "41", "42", "43", "44"}
)

var products = []demoProduct{
	{title: "Kaos Polos Premium Cotton Combed 30s", brand: "Kain Studio", sku: "KPP", state: "active",
		desc:    "Kaos polos berbahan katun combed 30s yang adem dan tidak mudah melar. Jahitan rantai di bahu, cocok untuk dipakai harian.",
		cats:    []string{"kaos", "bestseller"},
		attrs:   map[string]any{"bahan": "Katun combed 30s", "potongan": "Regular fit", "perawatan": "Cuci dengan air dingin"},
		colours: []string{"Hitam", "Putih", "Navy", "Abu Misty"}, sizes: apparel, price: 129000, weight: 200},
	{title: "Kaos Oversize Graphic Senja", brand: "Kain Studio", sku: "KOS", state: "active", sale: saleNow,
		desc:    "Kaos oversize dengan sablon plastisol bergambar senja di punggung. Bahan 24s yang tebal dan jatuh.",
		cats:    []string{"kaos"},
		attrs:   map[string]any{"bahan": "Katun combed 24s", "potongan": "Oversize", "sablon": "Plastisol"},
		colours: []string{"Hitam", "Krem"}, sizes: []string{"M", "L", "XL"}, price: 159000, weight: 230},
	{title: "Kemeja Linen Lengan Panjang", brand: "Kain Studio", sku: "KLP", state: "active",
		desc:    "Kemeja linen ringan dengan kerah mandarin. Nyaman untuk cuaca tropis, rapi untuk acara santai maupun kantor.",
		cats:    []string{"kemeja", "lebaran"},
		attrs:   map[string]any{"bahan": "Linen rami", "kerah": "Mandarin"},
		colours: []string{"Putih", "Sage", "Biru Muda"}, sizes: []string{"M", "L", "XL"}, price: 289000, weight: 280},
	{title: "Kemeja Batik Modern Slim Fit", brand: "Batik Kita", sku: "KBM", state: "active", sale: saleOpen,
		desc:    "Batik cap motif klasik dengan potongan slim fit modern. Dilapisi furing di bagian dalam.",
		cats:    []string{"kemeja", "batik", "lebaran"},
		attrs:   map[string]any{"bahan": "Katun primisima", "teknik": "Batik cap", "furing": true},
		colours: []string{"Parang Biru", "Kawung Coklat"}, sizes: []string{"M", "L", "XL", "XXL"}, price: 349000, weight: 300},
	{title: "Celana Chino Slim Fit", brand: "Nusantara Denim", sku: "CCS", state: "active",
		desc:    "Chino stretch dengan potongan slim yang tetap leluasa. Saku belakang berkancing.",
		cats:    []string{"celana"},
		attrs:   map[string]any{"bahan": "Katun twill stretch", "potongan": "Slim fit"},
		colours: []string{"Krem", "Navy", "Hitam"}, sizes: []string{"30", "32", "34"}, price: 299000, weight: 450},
	{title: "Jeans Selvedge Indigo 14oz", brand: "Nusantara Denim", sku: "JSI", state: "active", tone: "Indigo",
		desc:  "Denim selvedge 14oz yang belum dicuci, akan memudar mengikuti pemakaian. Dijahit di Bandung.",
		cats:  []string{"celana", "bestseller"},
		attrs: map[string]any{"bahan": "Denim selvedge 14oz", "asal": "Bandung"},
		sizes: []string{"29", "30", "31", "32", "33", "34"}, price: 549000, weight: 750},
	{title: "Blouse Rayon Motif Bunga", brand: "Kain Studio", sku: "BRM", state: "active", sale: saleScheduled,
		desc:    "Blouse rayon bermotif bunga kecil dengan kancing depan. Ringan dan tidak menerawang.",
		cats:    []string{"atasan"},
		attrs:   map[string]any{"bahan": "Rayon viscose", "lengan": "Tiga perempat"},
		colours: []string{"Merah Marun", "Hijau Army"}, sizes: []string{"S", "M", "L"}, price: 189000, weight: 180},
	{title: "Tunik Muslimah Katun", brand: "Batik Kita", sku: "TMK", state: "active",
		desc:    "Tunik panjang berbahan katun dengan aksen kancing kayu. Pas dipadukan dengan celana kulot.",
		cats:    []string{"atasan", "lebaran"},
		attrs:   map[string]any{"bahan": "Katun toyobo", "panjang": "Di bawah lutut"},
		colours: []string{"Krem", "Dusty Pink", "Navy"}, sizes: []string{"M", "L", "XL"}, price: 219000, weight: 250},
	{title: "Dress Midi Plisket", brand: "Kain Studio", sku: "DMP", state: "active", sale: saleNow,
		desc:    "Dress midi plisket dengan tali pinggang. Bahan jatuh dan tidak mudah kusut.",
		cats:    []string{"dress", "lebaran"},
		attrs:   map[string]any{"bahan": "Polyester plisket", "panjang": "Midi"},
		colours: []string{"Sage", "Hitam", "Dusty Pink"}, sizes: []string{"S/M", "L/XL"}, price: 329000, weight: 350},
	{title: "Rok Plisket Premium", brand: "Kain Studio", sku: "RPP", state: "active",
		desc:    "Rok plisket all size dengan karet pinggang. Panjang 85 cm.",
		cats:    []string{"rok"},
		attrs:   map[string]any{"bahan": "Hyget premium", "ukuran": "All size, lingkar pinggang 60-90 cm"},
		colours: []string{"Hitam", "Krem", "Mocca"}, price: 159000, weight: 250},
	{title: "Sneakers Kanvas Klasik", brand: "Langkah", sku: "SKK", state: "active",
		desc:    "Sneakers kanvas dengan sol karet vulkanisir. Desain klasik yang cocok dengan semua gaya.",
		cats:    []string{"sneakers", "bestseller"},
		attrs:   map[string]any{"bahan": "Kanvas 12oz", "sol": "Karet vulkanisir"},
		colours: []string{"Putih", "Hitam"}, sizes: shoes, price: 399000, weight: 900},
	{title: "Sepatu Lari Ringan Aksa", brand: "Langkah", sku: "SLA", state: "active", sale: saleNow,
		desc:    "Sepatu lari dengan upper knit yang bernapas dan midsole EVA empuk. Bobot hanya 240 gram per sepatu.",
		cats:    []string{"sneakers", "olahraga"},
		attrs:   map[string]any{"upper": "Knit", "midsole": "EVA", "berat": "240 g per sepatu"},
		colours: []string{"Abu", "Navy"}, sizes: shoes, price: 699000, weight: 800},
	{title: "Sandal Kulit Slide", brand: "Langkah", sku: "SKS", state: "active",
		desc:    "Sandal slide kulit sapi asli dengan footbed empuk. Dibuat oleh pengrajin di Garut.",
		cats:    []string{"sandal"},
		attrs:   map[string]any{"bahan": "Kulit sapi", "asal": "Garut"},
		colours: []string{"Coklat", "Hitam"}, sizes: []string{"39", "40", "41", "42", "43"}, price: 249000, weight: 600},
	{title: "Tas Ransel Kanvas 20L", brand: "Rimba Outdoor", sku: "TRK", state: "active",
		desc:    "Ransel kanvas 20 liter dengan slot laptop 14 inci dan lapisan tahan air.",
		cats:    []string{"tas", "olahraga"},
		attrs:   map[string]any{"kapasitas": "20 L", "laptop": "Hingga 14 inci", "tahan_air": true},
		colours: []string{"Hijau Army", "Hitam"}, price: 379000, weight: 850},
	{title: "Topi Baseball Bordir", brand: "Senja Aksesoris", sku: "TBB", state: "active",
		desc:    "Topi baseball katun dengan bordir logo kecil dan pengait besi di belakang.",
		cats:    []string{"topi"},
		attrs:   map[string]any{"bahan": "Katun drill", "pengait": "Besi"},
		colours: []string{"Hitam", "Krem", "Navy"}, price: 99000, weight: 120},
	{title: "Tote Bag Kanvas Pasar", brand: "Senja Aksesoris", sku: "TBK", state: "active", tone: "Krem",
		desc:  "Tote bag kanvas tebal untuk belanja ke pasar atau ke kampus. Muat laptop 13 inci.",
		cats:  []string{"tas"},
		attrs: map[string]any{"bahan": "Kanvas 16oz", "ukuran": "38 x 42 cm"},
		price: 89000, weight: 200},
	{title: "Jaket Windbreaker Outdoor", brand: "Rimba Outdoor", sku: "JWO", state: "active",
		desc:    "Jaket windbreaker ringan yang bisa dilipat ke sakunya sendiri. Tahan angin dan gerimis.",
		cats:    []string{"pria", "olahraga"},
		attrs:   map[string]any{"bahan": "Nylon taslan", "tahan_air": "Gerimis ringan"},
		colours: []string{"Hijau Army", "Hitam"}, sizes: []string{"M", "L", "XL"}, price: 459000, weight: 400},

	{title: "Kemeja Flanel Kotak", brand: "Kain Studio", sku: "KFK", state: "draft",
		desc:    "Kemeja flanel motif kotak untuk musim hujan. Siap dipublikasikan.",
		cats:    []string{"kemeja"},
		attrs:   map[string]any{"bahan": "Flanel katun"},
		colours: []string{"Merah", "Hijau"}, sizes: []string{"M", "L", "XL"}, price: 259000, weight: 300},
	{title: "Hoodie Fleece Basic", brand: "Kain Studio", sku: "HFB", state: "draft", gap: "photo",
		desc:    "Hoodie fleece dengan kantong kanguru. Menunggu foto produk.",
		cats:    []string{"pria"},
		colours: []string{"Abu Misty", "Hitam"}, sizes: []string{"M", "L", "XL"}, price: 279000, weight: 500},
	{title: "Celana Kulot Linen", brand: "Kain Studio", sku: "CKL", state: "draft", gap: "weight",
		desc:    "Kulot linen berpinggang karet. Berat paket belum diisi.",
		cats:    []string{"wanita"},
		colours: []string{"Krem", "Hitam"}, price: 199000},
	{title: "Sepatu Boots Kulit", brand: "Langkah", sku: "SBK", state: "draft", gap: "price",
		desc: "Boots kulit dengan sol karet tebal. Harga belum ditentukan.",
		cats: []string{"sepatu"}, tone: "Coklat",
		sizes: []string{"40", "41", "42", "43"}, weight: 1200},
	{title: "Syal Batik Sutra", brand: "Batik Kita", sku: "SBS", state: "draft", gap: "category", tone: "Kawung Coklat",
		desc: "Syal sutra bermotif kawung. Kategorinya belum dipilih.",
		cats: []string{"batik"}, price: 179000, weight: 150},

	{title: "Kaos Edisi Kemerdekaan 2025", brand: "Kain Studio", sku: "KEK", state: "archived",
		desc:    "Edisi terbatas 17 Agustus 2025. Sudah tidak diproduksi.",
		cats:    []string{"kaos"},
		colours: []string{"Merah", "Putih"}, sizes: []string{"M", "L", "XL"}, price: 119000, weight: 200},
	{title: "Sandal Jepit Classic", brand: "Langkah", sku: "SJC", state: "archived",
		desc:    "Sandal jepit karet. Diganti oleh Sandal Kulit Slide.",
		cats:    []string{"sandal"},
		colours: []string{"Hitam", "Biru"}, sizes: []string{"39", "40", "41", "42", "43"}, price: 49000, weight: 300},
	{title: "Masker Kain 3 Lapis", brand: "Senja Aksesoris", sku: "MKL", state: "archived", tone: "Navy",
		desc: "Masker kain tiga lapis. Tidak dijual lagi.",
		cats: []string{"aksesoris"}, price: 25000, weight: 50},
}

// archivedVariant is archived inside a product that stays on sale, so the
// matrix shows an archived row too (product title, then its option values).
var archivedVariant = struct {
	product string
	options []string
}{"Jaket Windbreaker Outdoor", []string{"Hitam", "XL"}}

// swatches are the placeholder cards' colours.
var swatches = map[string]color.RGBA{
	"Hitam": {0x22, 0x22, 0x22, 0xff}, "Putih": {0xf2, 0xf1, 0xee, 0xff}, "Navy": {0x1f, 0x2f, 0x4f, 0xff},
	"Abu Misty": {0xb9, 0xbc, 0xc2, 0xff}, "Abu": {0x8c, 0x8f, 0x94, 0xff}, "Krem": {0xe8, 0xdc, 0xc4, 0xff},
	"Sage": {0xa7, 0xb5, 0x9b, 0xff}, "Biru Muda": {0xa9, 0xc7, 0xe3, 0xff}, "Biru": {0x2a, 0x5d, 0xb0, 0xff},
	"Parang Biru": {0x2d, 0x4f, 0x7c, 0xff}, "Kawung Coklat": {0x7a, 0x51, 0x34, 0xff}, "Indigo": {0x2e, 0x3a, 0x5f, 0xff},
	"Merah Marun": {0x7b, 0x23, 0x32, 0xff}, "Merah": {0xb3, 0x26, 0x2d, 0xff}, "Hijau Army": {0x4b, 0x53, 0x20, 0xff},
	"Hijau": {0x2f, 0x6b, 0x3a, 0xff}, "Dusty Pink": {0xd8, 0xa7, 0xa7, 0xff}, "Mocca": {0xa4, 0x7c, 0x5b, 0xff},
	"Coklat": {0x6b, 0x42, 0x26, 0xff},
}

type demoCustomer struct {
	name, email, phone string
	city               int // index into addresses
	since              int // days since signing up
	verified, google   bool
}

var customers = []demoCustomer{
	{"Rina Wati", "rina.wati@example.com", "+6281234567801", 0, 110, true, false},
	{"Siti Rahmawati", "siti.rahmawati@example.com", "+6281234567802", 1, 106, true, false},
	{"Agus Setiawan", "agus.setiawan@example.com", "+6281234567803", 2, 102, true, false},
	{"Sari Putri", "sari.putri@example.com", "+6281234567804", 3, 99, true, true},
	{"Dimas Prasetyo", "dimas.prasetyo@example.com", "+6281234567805", 4, 95, true, false},
	{"Putri Ayu", "putri.ayu@example.com", "+6281234567806", 5, 92, false, false},
	{"Eko Wahyudi", "eko.wahyudi@example.com", "+6281234567807", 6, 88, true, false},
	{"Nadia Safitri", "nadia.safitri@example.com", "+6281234567808", 7, 84, true, false},
	{"Reza Firmansyah", "reza.firmansyah@example.com", "+6281234567809", 8, 80, true, false},
	{"Lestari Handayani", "lestari.handayani@example.com", "+6281234567810", 9, 77, true, false},
	{"Yoga Aditya", "yoga.aditya@example.com", "+6281234567811", 0, 74, false, false},
	{"Ratna Sari", "ratna.sari@example.com", "+6281234567812", 2, 71, true, false},
	{"Kevin Halim", "kevin.halim@example.com", "+6281234567813", 1, 5, true, false},
	{"Ayu Lestari", "ayu.lestari@example.com", "+6281234567814", 4, 2, false, false},
}

var addresses = []map[string]any{
	{"line1": "Jl. Melati No. 12", "line2": nil, "city": "Bandung", "province": "Jawa Barat", "postal_code": "40115"},
	{"line1": "Jl. Kenanga No. 4", "line2": "Blok C", "city": "Surabaya", "province": "Jawa Timur", "postal_code": "60231"},
	{"line1": "Jl. Jend. Sudirman No. 88", "line2": "Apartemen Taman Anggrek, Tower 2", "city": "Jakarta Selatan", "province": "DKI Jakarta", "postal_code": "12190"},
	{"line1": "Jl. Malioboro No. 21", "line2": nil, "city": "Yogyakarta", "province": "DI Yogyakarta", "postal_code": "55213"},
	{"line1": "Jl. Gatot Subroto No. 7", "line2": "Lantai 2", "city": "Medan", "province": "Sumatera Utara", "postal_code": "20112"},
	{"line1": "Jl. Sunset Road No. 45", "line2": nil, "city": "Denpasar", "province": "Bali", "postal_code": "80361"},
	{"line1": "Jl. Pettarani No. 30", "line2": nil, "city": "Makassar", "province": "Sulawesi Selatan", "postal_code": "90222"},
	{"line1": "Jl. Pandanaran No. 9", "line2": nil, "city": "Semarang", "province": "Jawa Tengah", "postal_code": "50241"},
	{"line1": "Jl. Ijen No. 15", "line2": nil, "city": "Malang", "province": "Jawa Timur", "postal_code": "65119"},
	{"line1": "Jl. Margonda Raya No. 100", "line2": "Ruko No. 3", "city": "Depok", "province": "Jawa Barat", "postal_code": "16424"},
}

// walkIns are manual orders' buyers: WhatsApp and Instagram orders typed in by staff.
var walkIns = []struct{ name, phone, note string }{
	{"Hendra Wijaya", "+6285731000017", "Order via WhatsApp"},
	{"Lina Marlina", "+6285731000034", "Order via Instagram DM"},
	{"Yusuf Hakim", "+6285731000051", "Order via WhatsApp, minta dikirim hari ini"},
	{"Maya Sari", "+6285731000068", "Reseller, diskon 10%"},
	{"Taufik Hidayat", "+6285731000085", "Order via WhatsApp"},
	{"Nurul Aini", "+6285731000102", "Bungkus kado, kartu ucapan: Selamat ulang tahun!"},
	{"Bayu Saputra", "+6285731000119", "Order via Instagram DM"},
	{"Indah Permata", "+6285731000136", "Reseller, diskon 10%"},
}

type channel int

const (
	manual   channel = iota // typed in by staff, paid by bank transfer
	transfer                // storefront, bank transfer
	midtrans                // storefront, Midtrans with its payment attempts
)

type demoOrder struct {
	ago   time.Duration // placed this long before the seed runs
	final string        // pending, paid, processing, shipped, completed, cancelled, owed, refunded
	via   channel
}

// Oldest first, so order numbers rise with placed_at: old orders have
// finished, recent ones are still moving through the board.
var orderPlan = []demoOrder{
	{75 * day, "completed", transfer}, {72 * day, "completed", manual}, {70 * day, "completed", midtrans},
	{67 * day, "cancelled", transfer}, {65 * day, "completed", midtrans}, {62 * day, "refunded", transfer},
	{60 * day, "completed", manual}, {57 * day, "completed", midtrans}, {55 * day, "completed", transfer},
	{52 * day, "cancelled", manual}, {50 * day, "completed", midtrans}, {47 * day, "completed", transfer},
	{45 * day, "refunded", midtrans}, {42 * day, "completed", manual}, {40 * day, "completed", transfer},
	{37 * day, "completed", midtrans}, {35 * day, "cancelled", midtrans}, {32 * day, "completed", transfer},
	{30 * day, "completed", manual}, {27 * day, "refunded", transfer}, {25 * day, "completed", midtrans},
	{22 * day, "completed", transfer}, {20 * day, "completed", manual}, {18 * day, "owed", midtrans},
	{16 * day, "cancelled", transfer}, {14 * day, "completed", transfer}, {12 * day, "shipped", midtrans},
	{10 * day, "shipped", manual}, {9 * day, "owed", transfer}, {8 * day, "shipped", transfer},
	{7 * day, "shipped", midtrans}, {6 * day, "shipped", transfer}, {5 * day, "processing", manual},
	{5*day - 6*time.Hour, "shipped", midtrans}, {4 * day, "processing", transfer}, {3 * day, "processing", midtrans},
	{3*day - 5*time.Hour, "cancelled", manual}, {2 * day, "paid", transfer}, {2*day - 4*time.Hour, "processing", transfer},
	{36 * time.Hour, "paid", midtrans}, {30 * time.Hour, "paid", manual}, {26 * time.Hour, "pending", transfer},
	{20 * time.Hour, "paid", transfer}, {14 * time.Hour, "pending", midtrans}, {9 * time.Hour, "pending", manual},
	{5 * time.Hour, "pending", transfer}, {2 * time.Hour, "pending", midtrans}, {40 * time.Minute, "pending", manual},
}

var storefrontNotes = []string{"Tolong dibungkus rapi ya, untuk hadiah", "Titip di pos satpam kalau tidak ada orang", "Kirim sebelum hari Jumat ya kak"}

var cancelReasons = map[channel]string{
	manual:   "Pembeli membatalkan lewat WhatsApp",
	transfer: "Transfer tidak masuk dalam 24 jam",
	midtrans: "Pembayaran Midtrans kedaluwarsa",
}
