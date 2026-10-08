package catalog

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db"
	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/jobs"
	apperrors "github.com/miqbalhamdani/new-commerce-api/internal/platform/errors"
	"github.com/miqbalhamdani/new-commerce-api/internal/storage"
)

// importBatch is how many rows commit together (BR-044).
const importBatch = 500

var importTargets = map[string]bool{"title": true, "sku": true, "regular_price": true, "sale_price": true,
	"weight_grams": true, "barcode": true}

var importKey = regexp.MustCompile(`^jobs/([0-9a-f-]{36})/([0-9a-f-]{36})/upload\.csv$`)

// ImportParams is what a product_import job carries.
type ImportParams struct {
	Key        string            `json:"r2_key"`
	Mapping    map[string]string `json:"column_mapping"`
	OnConflict string            `json:"on_conflict"`
}

// importState is the job's running tally, checkpointed with every batch.
type importState struct {
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Errors  []importError `json:"errors,omitempty"`
}

type importError struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// StartImport checks an uploaded file and queues its import. The job takes
// the id the presign put in the key.
func (s *Service) StartImport(ctx context.Context, p ImportParams) (uuid.UUID, error) {
	m := importKey.FindStringSubmatch(p.Key)
	if m == nil || m[1] != tenantOf(ctx).String() {
		return uuid.Nil, fieldError("r2_key", "not an import upload of this shop; presign with purpose product_import")
	}
	jobID, err := uuid.Parse(m[2])
	if err != nil {
		return uuid.Nil, fieldError("r2_key", "malformed import key")
	}
	if err := checkMapping(p.Mapping); err != nil {
		return uuid.Nil, err
	}
	if p.OnConflict != "update" && p.OnConflict != "error" {
		return uuid.Nil, fieldError("on_conflict", "on_conflict is update or error")
	}
	if _, _, err := s.files.Head(ctx, p.Key); errors.Is(err, storage.ErrNotFound) {
		return uuid.Nil, fieldError("r2_key", "nothing was uploaded under this key")
	} else if err != nil {
		return uuid.Nil, err
	}

	var job jobs.Job
	err = s.tx(ctx, func(_ *sqlcgen.Queries, tx pgx.Tx) error {
		var err error
		job, err = jobs.CreateWithID(ctx, tx, jobID, "product_import", p, actorOf(ctx))
		if code, _, ok := db.Violation(err); ok && code == "23505" {
			return fieldError("r2_key", "this upload has already been imported")
		}
		if err != nil {
			return err
		}
		return db.Audit(ctx, tx, db.AuditEntry{Action: "product.import", SubjectType: "job", SubjectID: jobID.String(),
			After: map[string]any{"r2_key": p.Key, "on_conflict": p.OnConflict}})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return jobID, s.jobs.Enqueue(ctx, job)
}

func checkMapping(mapping map[string]string) error {
	seen := map[string]bool{}
	for header, target := range mapping {
		name, isOption := strings.CutPrefix(target, "option:")
		switch {
		case isOption && strings.TrimSpace(name) == "":
			return fieldError("column_mapping", "column "+header+": an option needs a name, option:<Name>")
		case !isOption && !importTargets[target]:
			return fieldError("column_mapping", "column "+header+": "+target+" is not an import field")
		case seen[strings.ToLower(target)]:
			return fieldError("column_mapping", target+" is mapped twice")
		}
		seen[strings.ToLower(target)] = true
	}
	if !seen["title"] && !seen["sku"] {
		return fieldError("column_mapping", "map a column to title or sku")
	}
	return nil
}

// ImportHandler is the worker's product_import handler (P1-073).
func (s *Service) ImportHandler() jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress jobs.Progress) (any, error) {
		var p ImportParams
		if err := json.Unmarshal(job.Params, &p); err != nil {
			return nil, err
		}
		obj, err := s.files.Get(ctx, p.Key)
		if err != nil {
			return nil, err
		}
		header, rows, err := readCSV(obj, p.Mapping)
		_ = obj.Close()
		if err != nil {
			return nil, apperrors.ValidationFailed("The file could not be read: " + err.Error())
		}
		total := len(rows)
		if err := progress(int(job.Processed), &total, int(job.Failed)); err != nil {
			return nil, err
		}

		// Resume: the tally and the row count committed by earlier deliveries.
		state := importState{}
		if job.Result != nil {
			_ = json.Unmarshal(job.Result, &state)
		}
		done := int(job.Processed)
		names := optionNames(p.Mapping)
		for _, batch := range importBatches(rows) {
			if done >= batch.end {
				continue
			}
			if err := s.importBatch(ctx, job.ID, rows[batch.start:batch.end], names, p.OnConflict, &state, batch.end); err != nil {
				return nil, err
			}
		}

		result := map[string]any{"created": state.Created, "updated": state.Updated}
		if len(state.Errors) > 0 {
			key := strings.TrimSuffix(p.Key, "upload.csv") + "errors.csv"
			if err := s.writeErrors(ctx, key, header, rows, state.Errors); err != nil {
				return nil, err
			}
			result["error_report_key"] = key
		}
		return result, nil
	}
}

type span struct{ start, end int }

// importBatches cuts rows into batches of about importBatch, never splitting
// a product: rows sharing a title are moved together, so a product and all
// its variants commit in one batch and nothing spans a checkpoint.
func importBatches(rows []importRow) []span {
	order := []string{}
	groups := map[string][]int{}
	for i, r := range rows {
		k := groupKey(r)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}
	// Reorder in place so each group is contiguous, first appearance first.
	sorted := make([]importRow, 0, len(rows))
	for _, k := range order {
		for _, i := range groups[k] {
			sorted = append(sorted, rows[i])
		}
	}
	copy(rows, sorted)

	var out []span
	start := 0
	for _, k := range order {
		end := start + len(groups[k])
		if len(out) > 0 && out[len(out)-1].end == start && end-out[len(out)-1].start <= importBatch {
			out[len(out)-1].end = end
		} else {
			out = append(out, span{start, end})
		}
		start = end
	}
	return out
}

// groupKey is the product a row belongs to: its title, or -- with none -- the
// row alone (it can only update a SKU).
func groupKey(r importRow) string {
	if t := strings.ToLower(strings.TrimSpace(r.Values["title"])); t != "" {
		return "t:" + t
	}
	return "l:" + strconv.Itoa(r.Line)
}

func optionNames(mapping map[string]string) []string {
	var names []string
	for _, target := range mapping {
		if name, ok := strings.CutPrefix(target, "option:"); ok {
			names = append(names, strings.TrimSpace(name))
		}
	}
	slices.Sort(names)
	slices.SortStableFunc(names, func(a, b string) int { // Colour leads (BR-040)
		switch {
		case isColour(a) && !isColour(b):
			return -1
		case isColour(b) && !isColour(a):
			return 1
		}
		return 0
	})
	return names
}

// importBatch applies one batch of rows and checkpoints the job, in one
// transaction: either the batch and its count commit, or neither does.
func (s *Service) importBatch(ctx context.Context, jobID uuid.UUID, rows []importRow, names []string, onConflict string, state *importState, processed int) error {
	next := *state
	next.Errors = slices.Clone(state.Errors)
	err := s.tx(ctx, func(q *sqlcgen.Queries, tx pgx.Tx) error {
		products := map[string]uuid.UUID{}
		for _, r := range rows {
			if _, err := tx.Exec(ctx, "SAVEPOINT import_row"); err != nil {
				return err
			}
			status, err := s.importRow(ctx, q, r, names, onConflict, products)
			if err != nil {
				if _, rerr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT import_row"); rerr != nil {
					return rerr
				}
				next.Errors = append(next.Errors, importError{Line: r.Line, Reason: reason(err)})
				continue
			}
			_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT import_row")
			if status == "created" {
				next.Created++
			} else {
				next.Updated++
			}
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return q.CheckpointJob(ctx, sqlcgen.CheckpointJobParams{ID: jobID, Processed: int32(processed),
			Failed: int32(len(next.Errors)), Result: raw})
	})
	if err == nil {
		*state = next
	}
	return err
}

// importRow applies one row: an existing SKU is updated (or a conflict), any
// other row becomes a variant of its title's product, created on first use.
func (s *Service) importRow(ctx context.Context, q *sqlcgen.Queries, r importRow, names []string, onConflict string, products map[string]uuid.UUID) (string, error) {
	f, err := rowFields(r)
	if err != nil {
		return "", err
	}
	sku := r.Values["sku"]
	if sku != "" {
		f.SKU = &sku
		existing, err := q.VariantBySKU(ctx, sku)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		if err == nil {
			if onConflict == "error" {
				holder, _ := q.SKUHolder(ctx, sku)
				return "", apperrors.DuplicateSKU(sku, holder.Title)
			}
			if existing.ArchivedAt != nil {
				return "", fieldError("sku", "this SKU belongs to an archived variant")
			}
			if _, err := q.UpdateVariant(ctx, updateParams(existing.ID, int(existing.Version), f)); err != nil {
				return "", variantWriteError(err)
			}
			return "updated", nil
		}
	}

	title := strings.TrimSpace(r.Values["title"])
	if title == "" {
		return "", fieldError("title", "title is required for a SKU that is not in the catalog yet")
	}
	options := make([]string, len(names))
	for i, n := range names {
		options[i] = r.Values["option:"+n]
		if strings.TrimSpace(options[i]) == "" {
			return "", fieldError("option:"+n, "option "+n+" is empty")
		}
	}
	key := groupKey(r)
	product, ok := products[key]
	if !ok {
		slug, err := freeSlug(ctx, q, title)
		if err != nil {
			return "", err
		}
		p, err := q.CreateProduct(ctx, sqlcgen.CreateProductParams{ID: uuid.Must(uuid.NewV7()), Title: title,
			Slug: slug, Attributes: []byte("{}")})
		if err != nil {
			return "", err
		}
		if len(names) > 0 {
			if _, err := q.BumpProduct(ctx, sqlcgen.BumpProductParams{ID: p.ID, OptionNames: names}); err != nil {
				return "", err
			}
		}
		product = p.ID
		products[key] = product
	}
	if _, err := q.CreateVariant(ctx, createParams(uuid.Must(uuid.NewV7()), product, options, f)); err != nil {
		return "", variantWriteError(err)
	}
	return "created", nil
}

func rowFields(r importRow) (VariantFields, error) {
	var f VariantFields
	if v := r.Values["regular_price"]; v != "" {
		n, err := parseRupiah(v)
		if err != nil {
			return f, fieldError("regular_price", err.Error())
		}
		f.RegularPrice = &n
	}
	if v := r.Values["sale_price"]; v != "" {
		n, err := parseRupiah(v)
		if err != nil {
			return f, fieldError("sale_price", err.Error())
		}
		f.SalePrice = &n
	}
	if v := r.Values["weight_grams"]; v != "" {
		n, err := parseGrams(v)
		if err != nil {
			return f, fieldError("weight_grams", err.Error())
		}
		f.WeightGrams = &n
	}
	if v := r.Values["barcode"]; v != "" {
		f.Barcode = &v
	}
	return f, nil
}

func reason(err error) string {
	var e *apperrors.Error
	if errors.As(err, &e) {
		return e.Detail
	}
	slog.Error("import row failed", "error", err)
	return "Something went wrong on our side with this row."
}

// writeErrors stores errors.csv: the original line number and reason, then
// the row as it was, with a BOM so Excel opens it as UTF-8 (BR-044, BR-064).
func (s *Service) writeErrors(ctx context.Context, key string, header []string, rows []importRow, errs []importError) error {
	byLine := map[int][]string{}
	for _, r := range rows {
		byLine[r.Line] = r.Raw
	}
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buf)
	_ = w.Write(append([]string{"line", "error"}, header...))
	for _, e := range errs {
		_ = w.Write(append([]string{strconv.Itoa(e.Line), e.Reason}, byLine[e.Line]...))
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return s.files.Put(ctx, key, &buf, int64(buf.Len()), "text/csv")
}

// SignDownload is a fresh 15-minute link to a private object (BR-063).
func (s *Service) SignDownload(ctx context.Context, key string) (string, error) {
	return s.files.PresignGet(ctx, key, downloadTTL)
}

const downloadTTL = 15 * time.Minute
