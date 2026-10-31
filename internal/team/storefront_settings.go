package team

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
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
	whatsappRe   = regexp.MustCompile(`^62[0-9]{8,13}$`)
	brandColorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	instagramRe  = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)
	logoTypes    = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}
)

const (
	maxTaglineRunes = 160
	maxLogoBytes    = 2 << 20
	logoPresignTTL  = 10 * time.Minute
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
	LogoR2Key, WhatsappNumber, Tagline, BrandColor, Instagram                Change[string]
	ShippingCouriers                                                         *[]string
	BankTransferEnabled, MidtransEnabled                                     *bool
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
	if v := p.LogoR2Key.Value; v != nil {
		if err := s.checkLogo(ctx, strings.TrimSpace(*v)); err != nil {
			return StorefrontSettings{}, err
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
			LogoR2Key: pick(p.LogoR2Key, before.LogoR2Key), WhatsappNumber: pick(p.WhatsappNumber, before.WhatsappNumber),
			Tagline: pick(p.Tagline, before.Tagline), BrandColor: pick(p.BrandColor, before.BrandColor),
			Instagram: pick(p.Instagram, before.Instagram),
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
	for field, c := range map[string]Change[string]{"google_client_id": p.GoogleClientID, "midtrans_client_key": p.MidtransClientKey,
		"midtrans_server_key": p.MidtransServerKey, "tagline": p.Tagline} {
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
	add("logo_r2_key", a.LogoR2Key, b.LogoR2Key)
	add("whatsapp_number", a.WhatsappNumber, b.WhatsappNumber)
	add("tagline", a.Tagline, b.Tagline)
	add("brand_color", a.BrandColor, b.BrandColor)
	add("instagram", a.Instagram, b.Instagram)
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

// Presign is a signed direct upload of the shop's logo, and where it will be
// served from.
type Presign struct {
	URL       string
	Key       string
	ExpiresIn int
	PublicURL string
}

// PresignLogo signs the logo's upload to {tenant}/shop/ (BR-053, BR-142).
// There is no confirm: the key is saved as logo_r2_key, checked then.
func (s *Service) PresignLogo(ctx context.Context, mime string, bytes int64, sha string) (Presign, error) {
	ext, ok := logoTypes[mime]
	if !ok {
		return Presign{}, fieldError("mime_type", "a logo is image/jpeg, image/png or image/webp")
	}
	if bytes > maxLogoBytes {
		return Presign{}, fieldError("bytes", "a logo is at most 2 MB")
	}
	tenantID, _ := tenant.FromContext(ctx)
	key := fmt.Sprintf("%s/shop/%s.%s", tenantID, sha, ext)
	u, err := s.Files.PresignPut(ctx, key, logoPresignTTL)
	return Presign{URL: u, Key: key, ExpiresIn: int(logoPresignTTL.Seconds()), PublicURL: s.Files.PublicURL(key)}, err
}

// checkLogo holds a logo key to the shop's own prefix and to an uploaded
// image within the size limit (BR-051, BR-142).
func (s *Service) checkLogo(ctx context.Context, key string) error {
	tenantID, _ := tenant.FromContext(ctx)
	if !strings.HasPrefix(key, tenantID.String()+"/shop/") {
		return fieldError("logo_r2_key", "the key is not a logo upload for this shop")
	}
	mime, size, err := s.Files.Head(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return fieldError("logo_r2_key", "nothing was uploaded under this key")
	}
	if err != nil {
		return err
	}
	if _, ok := logoTypes[mime]; !ok {
		return fieldError("logo_r2_key", "the uploaded object is "+mime+", not an image")
	}
	if size > maxLogoBytes {
		return fieldError("logo_r2_key", "the uploaded logo is over 2 MB")
	}
	return nil
}

// LogoURL is where a logo key is served, or nil with no logo.
func (s *Service) LogoURL(key *string) *string {
	if key == nil || s.Files == nil {
		return nil
	}
	u := s.Files.PublicURL(*key)
	return &u
}

// StorefrontConfig is what a shop's website reads once to render sign-in,
// payment and its look (P1-224, P1-703): public by design, nothing secret in
// it.
func (s *Service) StorefrontConfig(ctx context.Context) (sqlcgen.StorefrontConfigRow, error) {
	var out sqlcgen.StorefrontConfigRow
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) (err error) {
		out, err = q.StorefrontConfig(ctx)
		return err
	})
	return out, err
}
