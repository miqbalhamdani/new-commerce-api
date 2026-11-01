package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/miqbalhamdani/new-commerce-api/internal/blog"
	"github.com/miqbalhamdani/new-commerce-api/internal/team"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// A demo shop per website theme (P1-804; BR-143): -theme picks the catalog
// and the look. clothing keeps the fashion catalog in data.go; the rest come
// from the sample shops in the themes' Claude Design files (sets/*.json),
// photos and all.

// whatsappBelli is where a demo shop's order button goes: a prospect trying
// one reaches Belli, not a stranger.
const whatsappBelli = "6285726202606"

type themeLook struct {
	set, tagline, address, hours, hero string
	weight                             int32 // grams, every product of the set
}

var looks = map[string]themeLook{
	"clothing": {tagline: "Pakaian harian yang awet, dijahit di Jakarta.", address: "Jakarta Selatan, DKI Jakarta",
		hours: "Setiap hari 09.00–21.00", hero: pexels(2475259)},
	"busana": {set: "busana", tagline: "Busana muslim harian, dijahit rapi di Bandung.", address: "Bandung, Jawa Barat",
		hours: "Setiap hari 09.00–21.00", hero: pexels(8995906), weight: 400},
	"woman": {set: "woman", tagline: "Tenang dipakai, mudah dipadukan.", address: "Jakarta Selatan, DKI Jakarta",
		hours: "Setiap hari 09.00–21.00", hero: pexels(19456445), weight: 350},
	"katalog": {set: "skincare", tagline: "Perawatan kulit harian dari bahan lokal Indonesia.", address: "Sleman, DI Yogyakarta",
		hours: "Senin–Sabtu 09.00–17.00", hero: unsplash("1581182800629-7d90925ad072"), weight: 150},
	"katalog-hangat": {set: "skincare", tagline: "Rawat kulit dengan bahan dari kebun Nusantara.", address: "Surakarta, Jawa Tengah",
		hours: "Senin–Sabtu 09.00–17.00", hero: unsplash("1620916566398-39f1143ab7be"), weight: 150},
	"furnitur": {set: "furnitur", tagline: "Furnitur jati & rotan buatan tangan dari Jepara.", address: "Jepara, Jawa Tengah",
		hours: "Senin–Sabtu 08.00–17.00", hero: pexels(8112338), weight: 15000},
	"furnitur-earthy": {set: "furnitur", tagline: "Kayu jati dan rotan, dikerjakan perajin Jepara.", address: "Jepara, Jawa Tengah",
		hours: "Senin–Sabtu 08.00–17.00", hero: pexels(8112338), weight: 15000},
	"furnitur-ceria": {set: "furnitur", tagline: "Perabot rumah yang ringan, ceria, dan awet.", address: "Bekasi, Jawa Barat",
		hours: "Setiap hari 10.00–20.00", hero: pexels(8112338), weight: 15000},
}

func pexels(id int) string {
	return fmt.Sprintf("https://images.pexels.com/photos/%d/pexels-photo-%d.jpeg?auto=compress&cs=tinysrgb&w=1200", id, id)
}

func unsplash(id string) string {
	return "https://images.unsplash.com/photo-" + id + "?fm=jpg&fit=crop&w=1200&q=75"
}

//go:embed sets/*.json
var setFiles embed.FS

// demoSet is a sets/*.json file: the design's sample shop.
type demoSet struct {
	Categories []struct{ Key, Name, Kind string } `json:"categories"`
	Products   []struct {
		Title, Desc string
		Cats        []string
		Attrs       [][2]string
		Price       int64
		Regular     int64 // the crossed-out price when on sale, else 0
		Options     []demoOption
		Photos      []string
	} `json:"products"`
	Posts []demoPost `json:"posts"`
}

type demoOption struct {
	Name   string `json:"name"`
	Values []struct {
		Value string `json:"value"`
		Add   int64  `json:"add"` // rupiah on top of the product's price
	} `json:"values"`
}

type demoPost struct {
	Title, Tag, Excerpt, Body, Cover string
	Products                         []string // product titles
}

// catalogData is what seedCatalog makes: the fashion catalog with the brands
// and archived rows a full admin demo shows, or a theme's set.
type catalogData struct {
	brands        []string
	archivedBrand string
	categories    []demoCategory
	products      []demoProduct
	posts         []demoPost
	retires       bool // archive a variant, a brand and a collection after the orders
}

func fashionCatalog() catalogData {
	return catalogData{brands: brandNames, archivedBrand: archivedBrand, categories: categories, products: products, retires: true}
}

func loadSet(name string, look themeLook) (catalogData, error) {
	raw, err := setFiles.ReadFile("sets/" + name + ".json")
	if err != nil {
		return catalogData{}, err
	}
	var s demoSet
	if err := json.Unmarshal(raw, &s); err != nil {
		return catalogData{}, fmt.Errorf("sets/%s.json: %w", name, err)
	}
	out := catalogData{posts: s.Posts}
	for _, c := range s.Categories {
		out.categories = append(out.categories, demoCategory{key: c.Key, name: c.Name, kind: c.Kind})
	}
	for i, p := range s.Products {
		def := demoProduct{title: p.Title, desc: p.Desc, cats: p.Cats, sku: initials(p.Title, i+1), price: p.Price,
			regular: p.Regular, options: p.Options, photos: p.Photos, weight: look.weight, state: "active",
			attrs: map[string]any{}}
		for _, a := range p.Attrs {
			def.attrs[a[0]] = a[1]
		}
		if def.photos == nil {
			def.photos = []string{}
		}
		// The design shows one product without a photo; a product needs one to
		// go live (BR-038), so it waits as a draft.
		if len(def.photos) == 0 {
			def.state, def.gap = "draft", "photo"
		}
		out.products = append(out.products, def)
	}
	return out, nil
}

// initials is a set product's code: its words' first letters and its place,
// so two titles with the same initials differ (GFM1, KLO11).
func initials(title string, n int) string {
	var b strings.Builder
	for _, w := range strings.Fields(title) {
		if r := []rune(w)[0]; unicode.IsLetter(r) && b.Len() < 4 {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return fmt.Sprintf("%s%d", b.String(), n)
}

// fetchPhoto downloads a design's photo. A demo shop is seeded once, so the
// photos are fetched then rather than kept in the repository.
func fetchPhoto(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 10<<20))
}

// imageExt is the extension a downloaded photo is stored under, or "" for
// anything but JPEG, PNG or WebP.
func imageExt(data []byte) string {
	return map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}[http.DetectContentType(data)]
}

// look sets the theme, its first colour and the rest of the shop's look, the
// hero photo too when it downloads (BR-142).
func (sd *seeder) look(ctx context.Context, theme string, l themeLook) error {
	str := func(s string) team.Change[string] { return team.Change[string]{Set: true, Value: &s} }
	p := team.StorefrontPatch{Theme: &theme, Tagline: str(l.tagline), WhatsappNumber: str(whatsappBelli),
		Address: str(l.address), OpeningHours: str(l.hours)}
	if data, err := sd.photoBytes(ctx, l.hero); err == nil {
		tenantID, _ := tenant.FromContext(ctx)
		key := fmt.Sprintf("%s/shop/%s.%s", tenantID, sha(data), imageExt(data))
		if err := sd.files.Put(ctx, key, bytes.NewReader(data), int64(len(data)), http.DetectContentType(data)); err != nil {
			return err
		}
		p.HeroR2Key = str(key)
	}
	_, err := sd.team.UpdateStorefrontSettings(ctx, p)
	return err
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// photoBytes fetches a photo when the seeder may, as an image it can store.
func (sd *seeder) photoBytes(ctx context.Context, url string) ([]byte, error) {
	if sd.fetch == nil || url == "" {
		return nil, fmt.Errorf("no photo fetching")
	}
	data, err := sd.fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	if imageExt(data) == "" {
		return nil, fmt.Errorf("%s: not an image", url)
	}
	return data, nil
}

// seedPosts publishes the set's articles, newest first in the list, each with
// its cover when it downloads and the products it mentions (BR-131, BR-135).
func (sd *seeder) seedPosts(ctx context.Context, posts []demoPost, cat catalogResult, now time.Time) (int, error) {
	byTitle := map[string]uuid.UUID{}
	for _, p := range cat.products {
		if p.def.state == "active" {
			byTitle[p.def.title] = p.id
		}
	}
	for i, d := range posts {
		ids := []uuid.UUID{}
		for _, t := range d.Products {
			if id, ok := byTitle[t]; ok {
				ids = append(ids, id)
			}
		}
		tags := []string{d.Tag}
		post, err := sd.blog.CreatePost(ctx, blog.PostInput{Title: &d.Title, Body: &d.Body, Excerpt: &d.Excerpt,
			Tags: &tags, ProductIDs: &ids})
		if err != nil {
			return i, fmt.Errorf("%s: %w", d.Title, err)
		}
		published, status := now.Add(-time.Duration(4+i*14)*day), "published"
		in := blog.PostInput{Status: &status, PublishedAt: &published}
		if data, err := sd.photoBytes(ctx, d.Cover); err == nil {
			tenantID, _ := tenant.FromContext(ctx)
			key := fmt.Sprintf("%s/blogs/%s/%s.%s", tenantID, post.ID, sha(data), imageExt(data))
			if err := sd.files.Put(ctx, key, bytes.NewReader(data), int64(len(data)), http.DetectContentType(data)); err != nil {
				return i, err
			}
			in.CoverKey = &key
		}
		if _, err := sd.blog.UpdatePost(ctx, post.ID, in); err != nil {
			return i, fmt.Errorf("publish %s: %w", d.Title, err)
		}
	}
	return len(posts), nil
}
