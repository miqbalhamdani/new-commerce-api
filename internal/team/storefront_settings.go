package team

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
	"github.com/miqbalhamdani/new-commerce-api/internal/tenant"
)

// Storefront settings: 04-api-spec.md §4 (P1-216, P1-703; BR-120, BR-122,
// BR-127, BR-129, BR-142). How the shop's website sells and looks.

type StorefrontSettings = sqlcgen.StorefrontSetting

// Couriers are the Biteship courier codes a shop can offer (BR-120): the
// contract's CourierCode.
var Couriers = []string{"jne", "jnt", "sicepat", "anteraja", "pos", "tiki", "ninja", "lion", "idexpress", "sap", "rpx", "wahana"}

var postalCodeRe = regexp.MustCompile(`^[0-9]{5}$`)

// The shop's look (BR-142).
var (
	whatsappRe     = regexp.MustCompile(`^62[0-9]{8,13}$`)
	brandColorRe   = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	instagramRe    = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)
	shopeeURLRe    = regexp.MustCompile(`^https://([a-z0-9-]+\.)*shopee\.co\.id(/\S*)?$`)
	tokopediaURLRe = regexp.MustCompile(`^https://([a-z0-9-]+\.)*tokopedia\.com(/\S*)?$`)
	logoTypes      = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}
)

const (
	maxTaglineRunes = 160
	maxAddressRunes = 300
	maxHoursRunes   = 120
	maxShopURLRunes = 300
	logoPresignTTL  = 10 * time.Minute
)

// shopImage is an image the look keeps under {tenant}/shop/: the logo and the
// hero photo (BR-053, BR-142).
type shopImage struct {
	field, name string
	max         int64
	limit       string
}

var (
	logoImage = shopImage{field: "logo_r2_key", name: "logo", max: 2 << 20, limit: "2 MB"}
	heroImage = shopImage{field: "hero_r2_key", name: "hero photo", max: 5 << 20, limit: "5 MB"}
)

// Change is one field of a PATCH: Set means it was sent, and a nil Value
// with Set clears it (BR-009).
type Change[T any] struct {
	Set   bool
	Value *T
}

// StorefrontPatch is PATCH /v1/storefront-settings. Booleans and the courier
// list are not nullable, so they are plain pointers: nil leaves them alone.
type StorefrontPatch struct {
	ContactEmail, GoogleClientID, OriginPostalCode, BankTransferInstructions Change[string]
	MidtransEnvironment, MidtransClientKey, MidtransServerKey                Change[string]
	LogoR2Key, HeroR2Key, WhatsappNumber, Tagline, BrandColor, Instagram     Change[string]
	Address, OpeningHours, ShopeeURL, TokopediaURL                           Change[string]
	License, CancellationPolicy, DocumentRequirements                        Change[string] // BR-142
	RebuildHookURL                                                           Change[string] // BR-098
	ShippingCouriers                                                         *[]string
	BankTransferEnabled, MidtransEnabled                                     *bool
	Theme                                                                    *string
}

// StorefrontSettings is the shop's row, made on first use for a shop that
// came after the migration's backfill.
func (s *Service) StorefrontSettings(ctx context.Context) (StorefrontSettings, error) {
	var out StorefrontSettings
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		if err := q.EnsureStorefrontSettings(ctx, uuid.New()); err != nil {
			return err
		}
		out, err = q.GetStorefrontSettings(ctx)
		return err
	})
	return out, err
}

// UpdateStorefrontSettings applies a PATCH. The merged row is checked under
// the row lock, so the CHECKs never surface as a 500: Midtrans on needs its
// environment and both keys, and one payment method must stay on (BR-122).
// A server key is sealed before it is stored (BR-129) and appears in no
// audit row, log or response.
func (s *Service) UpdateStorefrontSettings(ctx context.Context, p StorefrontPatch) (StorefrontSettings, error) {
	if err := validStorefrontPatch(p); err != nil {
		return StorefrontSettings{}, err
	}
	for img, c := range map[shopImage]Change[string]{logoImage: p.LogoR2Key, heroImage: p.HeroR2Key} {
		if c.Value != nil {
			if err := s.checkShopImage(ctx, img, strings.TrimSpace(*c.Value)); err != nil {
				return StorefrontSettings{}, err
			}
		}
	}
	tenantID, _ := tenant.FromContext(ctx)
	var sealed []byte
	if p.MidtransServerKey.Set && p.MidtransServerKey.Value != nil {
		var err error
		if sealed, err = s.box.Seal(tenantID, []byte(strings.TrimSpace(*p.MidtransServerKey.Value))); err != nil {
			return StorefrontSettings{}, err
		}
	}

	var after StorefrontSettings
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		if err := q.EnsureStorefrontSettings(ctx, uuid.New()); err != nil {
			return err
		}
		before, err := q.LockStorefrontSettings(ctx)
		if err != nil {
			return err
		}
		next := sqlcgen.UpdateStorefrontSettingsParams{
			ContactEmail: pick(p.ContactEmail, before.ContactEmail), GoogleClientID: pick(p.GoogleClientID, before.GoogleClientID),
			OriginPostalCode: pick(p.OriginPostalCode, before.OriginPostalCode), ShippingCouriers: before.ShippingCouriers,
			BankTransferEnabled: before.BankTransferEnabled, BankTransferInstructions: pick(p.BankTransferInstructions, before.BankTransferInstructions),
			MidtransEnabled: before.MidtransEnabled, MidtransEnvironment: pick(p.MidtransEnvironment, before.MidtransEnvironment),
			MidtransClientKey: pick(p.MidtransClientKey, before.MidtransClientKey), MidtransServerKey: before.MidtransServerKey,
			Theme: before.Theme, BrandColor: pick(p.BrandColor, before.BrandColor),
			LogoR2Key: pick(p.LogoR2Key, before.LogoR2Key), HeroR2Key: pick(p.HeroR2Key, before.HeroR2Key),
			WhatsappNumber: pick(p.WhatsappNumber, before.WhatsappNumber), Tagline: pick(p.Tagline, before.Tagline),
			Instagram: pick(p.Instagram, before.Instagram), Address: pick(p.Address, before.Address),
			OpeningHours: pick(p.OpeningHours, before.OpeningHours), ShopeeUrl: pick(p.ShopeeURL, before.ShopeeUrl),
			TokopediaUrl: pick(p.TokopediaURL, before.TokopediaUrl),
			License:      pick(p.License, before.License), CancellationPolicy: pick(p.CancellationPolicy, before.CancellationPolicy),
			DocumentRequirements: pick(p.DocumentRequirements, before.DocumentRequirements),
			RebuildHookUrl:       pick(p.RebuildHookURL, before.RebuildHookUrl),
		}
		// A colour belongs to its theme: another theme without a colour starts on
		// that theme's first (BR-142, BR-143).
		if p.Theme != nil && *p.Theme != before.Theme {
			next.Theme = *p.Theme
			if !p.BrandColor.Set {
				next.BrandColor = nil
			}
		}
		if colors := Themes[next.Theme]; next.BrandColor != nil && !slices.Contains(colors[:], *next.BrandColor) {
			return fieldError("brand_color", "brand_color is one of the "+next.Theme+" theme's colours: "+strings.Join(colors[:], ", "))
		}
		if p.ShippingCouriers != nil {
			next.ShippingCouriers = *p.ShippingCouriers
		}
		if p.BankTransferEnabled != nil {
			next.BankTransferEnabled = *p.BankTransferEnabled
		}
		if p.MidtransEnabled != nil {
			next.MidtransEnabled = *p.MidtransEnabled
		}
		if p.MidtransServerKey.Set {
			next.MidtransServerKey = sealed // nil clears it
		}
		if next.MidtransEnabled && (next.MidtransEnvironment == nil || next.MidtransClientKey == nil || next.MidtransServerKey == nil) {
			return fieldError("midtrans_enabled", "Midtrans needs its environment, client key and server key before it can be turned on.")
		}
		if !next.BankTransferEnabled && !next.MidtransEnabled {
			return fieldError("bank_transfer_enabled", "Keep at least one payment method on.")
		}
		if after, err = q.UpdateStorefrontSettings(ctx, next); err != nil {
			return err
		}
		changed, was, now := settingsDiff(before, after)
		if len(changed) == 0 {
			return nil
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "storefront_settings.update", SubjectType: "tenant",
			SubjectID: tenantID.String(), Before: was, After: now})
	})
	return after, err
}

func validStorefrontPatch(p StorefrontPatch) error {
	if v := p.ContactEmail.Value; v != nil {
		if _, err := mail.ParseAddress(*v); err != nil || strings.ContainsAny(*v, "<> ") {
			return fieldError("contact_email", "contact_email is an email address")
		}
	}
	if v := p.OriginPostalCode.Value; v != nil && !postalCodeRe.MatchString(*v) {
		return fieldError("origin_postal_code", "origin_postal_code is 5 digits")
	}
	if p.ShippingCouriers != nil {
		for _, c := range *p.ShippingCouriers {
			if !slices.Contains(Couriers, c) {
				return fieldError("shipping_couriers", c+" is not a Biteship courier this shop can offer: "+strings.Join(Couriers, ", "))
			}
		}
	}
	if v := p.Theme; v != nil {
		if _, ok := Themes[*v]; !ok {
			return fieldError("theme", *v+" is not a theme: busana, clothing, woman, katalog, katalog-hangat, furnitur, furnitur-earthy or furnitur-ceria")
		}
	}
	if v := p.MidtransEnvironment.Value; v != nil && *v != "sandbox" && *v != "production" {
		return fieldError("midtrans_environment", "midtrans_environment is sandbox or production")
	}
	if v := p.WhatsappNumber.Value; v != nil && !whatsappRe.MatchString(strings.TrimSpace(*v)) {
		return fieldError("whatsapp_number", "whatsapp_number is 62 and 8 to 13 more digits, such as 6281234567890")
	}
	if v := p.Tagline.Value; v != nil && utf8.RuneCountInString(strings.TrimSpace(*v)) > maxTaglineRunes {
		return fieldError("tagline", "tagline is at most 160 characters")
	}
	if v := p.BrandColor.Value; v != nil && !brandColorRe.MatchString(strings.TrimSpace(*v)) {
		return fieldError("brand_color", "brand_color is #rrggbb in lower case")
	}
	if v := p.Instagram.Value; v != nil && !instagramRe.MatchString(strings.TrimSpace(*v)) {
		return fieldError("instagram", "instagram is the handle without @: letters, digits, . and _")
	}
	if v := p.Address.Value; v != nil && utf8.RuneCountInString(strings.TrimSpace(*v)) > maxAddressRunes {
		return fieldError("address", "address is at most 300 characters")
	}
	if v := p.OpeningHours.Value; v != nil && utf8.RuneCountInString(strings.TrimSpace(*v)) > maxHoursRunes {
		return fieldError("opening_hours", "opening_hours is at most 120 characters")
	}
	if v := p.RebuildHookURL.Value; v != nil && !ValidRebuildHook(strings.TrimSpace(*v)) {
		return fieldError("rebuild_hook_url", "rebuild_hook_url is an https:// deploy hook on api.vercel.com, api.cloudflare.com or api.netlify.com, at most 500 characters")
	}
	for field, limit := range map[string]struct {
		c   Change[string]
		max int
	}{"license": {p.License, 120}, "cancellation_policy": {p.CancellationPolicy, 3000},
		"document_requirements": {p.DocumentRequirements, 3000}} {
		if v := limit.c.Value; v != nil && utf8.RuneCountInString(strings.TrimSpace(*v)) > limit.max {
			return fieldError(field, field+" is at most "+strconv.Itoa(limit.max)+" characters")
		}
	}
	for field, link := range map[string]struct {
		c    Change[string]
		re   *regexp.Regexp
		host string
	}{"shopee_url": {p.ShopeeURL, shopeeURLRe, "shopee.co.id"}, "tokopedia_url": {p.TokopediaURL, tokopediaURLRe, "tokopedia.com"}} {
		if v := link.c.Value; v != nil {
			if u := strings.TrimSpace(*v); utf8.RuneCountInString(u) > maxShopURLRunes || !link.re.MatchString(u) {
				return fieldError(field, field+" is an https:// link on "+link.host+", at most 300 characters")
			}
		}
	}
	for field, c := range map[string]Change[string]{"google_client_id": p.GoogleClientID, "midtrans_client_key": p.MidtransClientKey,
		"midtrans_server_key": p.MidtransServerKey, "tagline": p.Tagline, "address": p.Address, "opening_hours": p.OpeningHours,
		"license": p.License, "cancellation_policy": p.CancellationPolicy, "document_requirements": p.DocumentRequirements} {
		if c.Value != nil && strings.TrimSpace(*c.Value) == "" {
			return fieldError(field, field+" cannot be blank; send null to remove it")
		}
	}
	return nil
}

func pick(c Change[string], current *string) *string {
	if !c.Set {
		return current
	}
	if c.Value == nil {
		return nil
	}
	v := strings.TrimSpace(*c.Value)
	return &v
}

// settingsDiff is the audit row's before and after: changed fields only, and
// for the server key only whether one is set -- never the key (BR-129).
func settingsDiff(a, b StorefrontSettings) (changed []string, before, after map[string]any) {
	before, after = map[string]any{}, map[string]any{}
	add := func(name string, x, y any) {
		if !equal(x, y) {
			changed = append(changed, name)
			before[name], after[name] = x, y
		}
	}
	add("contact_email", a.ContactEmail, b.ContactEmail)
	add("google_client_id", a.GoogleClientID, b.GoogleClientID)
	add("origin_postal_code", a.OriginPostalCode, b.OriginPostalCode)
	add("shipping_couriers", a.ShippingCouriers, b.ShippingCouriers)
	add("bank_transfer_enabled", a.BankTransferEnabled, b.BankTransferEnabled)
	add("bank_transfer_instructions", a.BankTransferInstructions, b.BankTransferInstructions)
	add("midtrans_enabled", a.MidtransEnabled, b.MidtransEnabled)
	add("midtrans_environment", a.MidtransEnvironment, b.MidtransEnvironment)
	add("midtrans_client_key", a.MidtransClientKey, b.MidtransClientKey)
	add("theme", a.Theme, b.Theme)
	add("brand_color", a.BrandColor, b.BrandColor)
	add("logo_r2_key", a.LogoR2Key, b.LogoR2Key)
	add("hero_r2_key", a.HeroR2Key, b.HeroR2Key)
	add("whatsapp_number", a.WhatsappNumber, b.WhatsappNumber)
	add("tagline", a.Tagline, b.Tagline)
	add("instagram", a.Instagram, b.Instagram)
	add("address", a.Address, b.Address)
	add("opening_hours", a.OpeningHours, b.OpeningHours)
	add("shopee_url", a.ShopeeUrl, b.ShopeeUrl)
	add("tokopedia_url", a.TokopediaUrl, b.TokopediaUrl)
	add("license", a.License, b.License)
	add("cancellation_policy", a.CancellationPolicy, b.CancellationPolicy)
	add("document_requirements", a.DocumentRequirements, b.DocumentRequirements)
	// The hook is a credential of the owner's host: whether one is set, never
	// the URL (BR-098).
	if !equal(a.RebuildHookUrl, b.RebuildHookUrl) {
		changed = append(changed, "rebuild_hook_url")
		before["rebuild_hook_set"], after["rebuild_hook_set"] = a.RebuildHookUrl != nil, b.RebuildHookUrl != nil
	}
	if !slices.Equal(a.MidtransServerKey, b.MidtransServerKey) {
		changed = append(changed, "midtrans_server_key")
		before["midtrans_server_key_set"], after["midtrans_server_key_set"] = a.MidtransServerKey != nil, b.MidtransServerKey != nil
	}
	return changed, before, after
}

func equal(x, y any) bool {
	switch x := x.(type) {
	case *string:
		y := y.(*string)
		return x == nil && y == nil || x != nil && y != nil && *x == *y
	case []string:
		return slices.Equal(x, y.([]string))
	default:
		return x == y
	}
}

// Presign is a signed direct upload of the shop's logo or hero photo, and
// where it will be served from.
type Presign struct {
	URL       string
	Key       string
	ExpiresIn int
	PublicURL string
}

// PresignLogo signs the logo's upload to {tenant}/shop/ (BR-053, BR-142).
// There is no confirm: the key is saved as logo_r2_key, checked then.
func (s *Service) PresignLogo(ctx context.Context, mime string, bytes int64, sha string) (Presign, error) {
	return s.presignShopImage(ctx, logoImage, mime, bytes, sha)
}

// PresignHero signs the hero photo's upload, as PresignLogo, up to 5 MB; the
// key is saved as hero_r2_key.
func (s *Service) PresignHero(ctx context.Context, mime string, bytes int64, sha string) (Presign, error) {
	return s.presignShopImage(ctx, heroImage, mime, bytes, sha)
}

func (s *Service) presignShopImage(ctx context.Context, img shopImage, mime string, bytes int64, sha string) (Presign, error) {
	ext, ok := logoTypes[mime]
	if !ok {
		return Presign{}, fieldError("mime_type", "a "+img.name+" is image/jpeg, image/png or image/webp")
	}
	if bytes > img.max {
		return Presign{}, fieldError("bytes", "a "+img.name+" is at most "+img.limit)
	}
	tenantID, _ := tenant.FromContext(ctx)
	key := fmt.Sprintf("%s/shop/%s.%s", tenantID, sha, ext)
	u, err := s.Files.PresignPut(ctx, key, logoPresignTTL)
	return Presign{URL: u, Key: key, ExpiresIn: int(logoPresignTTL.Seconds()), PublicURL: s.Files.PublicURL(key)}, err
}

// checkShopImage holds a logo or hero key to the shop's own prefix and to an
// uploaded image within the size limit (BR-051, BR-142).
func (s *Service) checkShopImage(ctx context.Context, img shopImage, key string) error {
	tenantID, _ := tenant.FromContext(ctx)
	if !strings.HasPrefix(key, tenantID.String()+"/shop/") {
		return fieldError(img.field, "the key is not a "+img.name+" upload for this shop")
	}
	mime, size, err := s.Files.Head(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return fieldError(img.field, "nothing was uploaded under this key")
	}
	if err != nil {
		return err
	}
	if _, ok := logoTypes[mime]; !ok {
		return fieldError(img.field, "the uploaded object is "+mime+", not an image")
	}
	if size > img.max {
		return fieldError(img.field, "the uploaded "+img.name+" is over "+img.limit)
	}
	return nil
}

// LogoURL is where a logo or hero key is served, or nil with none.
func (s *Service) LogoURL(key *string) *string {
	if key == nil || s.Files == nil {
		return nil
	}
	u := s.Files.PublicURL(*key)
	return &u
}

// StorefrontConfig is what a shop's website reads once to render sign-in,
// payment and its look (P1-224, P1-703, P1-802): public by design, nothing
// secret in it.
func (s *Service) StorefrontConfig(ctx context.Context) (sqlcgen.StorefrontConfigRow, error) {
	var out sqlcgen.StorefrontConfigRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		out, err = q.StorefrontConfig(ctx)
		return err
	})
	return out, err
}
