package orders

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/miqbalhamdani/new-commerce-api/internal/db/sqlcgen"
	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// MaxDashboardSpan is how long a dashboard period may be (04-api-spec.md
// §5.7): a year, and a leap day.
const MaxDashboardSpan = 366 * 24 * time.Hour

// Dashboard is GET /v1/dashboard (P1-113): BR-066's figures for orders placed
// in [From, To) that are paid and not cancelled, against the same span just
// before, and what needs action now.
type Dashboard struct {
	From, To    time.Time
	NeedsAction sqlcgen.DashboardNeedsActionRow
	Current     Figures
	Previous    Figures
	// Daily has every WIB day of the period; BySource and ByPaymentMethod
	// every value, in a fixed order. All zero-filled.
	Daily           []Day
	BySource        []Split
	ByPaymentMethod []Split
	New, Returning  int
	TopProducts     []TopProduct
}

// TopProduct is one of the period's best sellers with its main-tree
// categories, by path.
type TopProduct struct {
	sqlcgen.DashboardTopProductsRow
	Categories []sqlcgen.DashboardProductCategoriesRow
}

// Figures are one period's totals.
type Figures struct {
	Sales             int64
	Orders            int
	AverageOrderValue int64
	ItemsSold         int64
}

// Day is one WIB calendar day, as "2006-01-02".
type Day struct {
	Date   string
	Sales  int64
	Orders int
}

// Split is the sales and orders of one source or payment method.
type Split struct {
	Key    string
	Sales  int64
	Orders int
}

var (
	sources        = []string{"manual", "storefront"}
	paymentMethods = []string{"bank_transfer", "midtrans"}
)

// Dashboard reads the figures in one transaction, in the caller's tenant.
func (s *Service) Dashboard(ctx context.Context, from, to time.Time) (Dashboard, error) {
	if !to.After(from) {
		return Dashboard{}, fieldError("to", "to is after from")
	}
	if to.Sub(from) > MaxDashboardSpan {
		return Dashboard{}, fieldError("to", "to is at most 366 days after from")
	}
	d := Dashboard{From: from, To: to}
	period := sqlcgen.DashboardFiguresParams{PeriodFrom: from, PeriodTo: to}
	before := sqlcgen.DashboardFiguresParams{PeriodFrom: from.Add(-to.Sub(from)), PeriodTo: from}
	err := s.tx(ctx, func(q *sqlcgen.Queries, _ pgx.Tx) error {
		var err error
		if d.NeedsAction, err = q.DashboardNeedsAction(ctx); err != nil {
			return err
		}
		if d.Current, err = figures(ctx, q, period); err != nil {
			return err
		}
		if d.Previous, err = figures(ctx, q, before); err != nil {
			return err
		}
		days, err := q.DashboardDaily(ctx, sqlcgen.DashboardDailyParams(period))
		if err != nil {
			return err
		}
		d.Daily = everyDay(from, to, days)
		bySource, err := q.DashboardBySource(ctx, sqlcgen.DashboardBySourceParams(period))
		if err != nil {
			return err
		}
		d.BySource = splits(sources, len(bySource), func(i int) Split {
			return Split{bySource[i].Source, bySource[i].Sales, int(bySource[i].Orders)}
		})
		byMethod, err := q.DashboardByPaymentMethod(ctx, sqlcgen.DashboardByPaymentMethodParams(period))
		if err != nil {
			return err
		}
		d.ByPaymentMethod = splits(paymentMethods, len(byMethod), func(i int) Split {
			return Split{byMethod[i].PaymentMethod, byMethod[i].Sales, int(byMethod[i].Orders)}
		})
		firsts, err := q.DashboardBuyers(ctx, sqlcgen.DashboardBuyersParams{PeriodFrom: from, PeriodTo: to})
		if err != nil {
			return err
		}
		for _, first := range firsts {
			if first.Before(from) {
				d.Returning++
			} else {
				d.New++
			}
		}
		top, err := q.DashboardTopProducts(ctx, sqlcgen.DashboardTopProductsParams(period))
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(top))
		for i, p := range top {
			ids[i] = p.ProductID
		}
		cats, err := q.DashboardProductCategories(ctx, ids)
		if err != nil {
			return err
		}
		byProduct := map[uuid.UUID][]sqlcgen.DashboardProductCategoriesRow{}
		for _, c := range cats {
			byProduct[c.ProductID] = append(byProduct[c.ProductID], c)
		}
		d.TopProducts = make([]TopProduct, len(top))
		for i, p := range top {
			d.TopProducts[i] = TopProduct{DashboardTopProductsRow: p, Categories: byProduct[p.ProductID]}
		}
		return nil
	})
	return d, err
}

func figures(ctx context.Context, q *sqlcgen.Queries, p sqlcgen.DashboardFiguresParams) (Figures, error) {
	row, err := q.DashboardFigures(ctx, p)
	if err != nil {
		return Figures{}, err
	}
	f := Figures{Sales: row.Sales, Orders: int(row.Orders), ItemsSold: row.ItemsSold}
	if f.Orders > 0 {
		f.AverageOrderValue = f.Sales / int64(f.Orders) // rounded down (BR-066)
	}
	return f, nil
}

// everyDay lists each WIB day from the one from falls in to the one just
// before to, with the query's figures where it has any.
func everyDay(from, to time.Time, rows []sqlcgen.DashboardDailyRow) []Day {
	got := make(map[string]sqlcgen.DashboardDailyRow, len(rows))
	for _, r := range rows {
		got[r.Day.Time.Format(time.DateOnly)] = r
	}
	first := from.In(config.WIB)
	last := to.Add(-time.Nanosecond).In(config.WIB)
	day := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, config.WIB)
	end := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, config.WIB)
	var out []Day
	for ; !day.After(end); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		out = append(out, Day{Date: key, Sales: got[key].Sales, Orders: int(got[key].Orders)})
	}
	return out
}

// splits is every key in order, with the n query rows' figures where present.
func splits(keys []string, n int, row func(i int) Split) []Split {
	got := make(map[string]Split, n)
	for i := range n {
		r := row(i)
		got[r.Key] = r
	}
	out := make([]Split, len(keys))
	for i, k := range keys {
		out[i] = Split{Key: k, Sales: got[k].Sales, Orders: got[k].Orders}
	}
	return out
}
