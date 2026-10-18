package httpapi

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/miqbalhamdani/new-commerce-api/internal/auth"
	"github.com/miqbalhamdani/new-commerce-api/internal/orders"
)

// GetDashboard handles GET /v1/dashboard (P1-113, 04-api-spec.md §5.7):
// BR-066's figures for [from, to).
func (s *Server) GetDashboard(w http.ResponseWriter, r *http.Request, params GetDashboardParams) {
	requirePermission(auth.PermOrdersRead, func(w http.ResponseWriter, r *http.Request) {
		from, err := requiredTime("from", params.From)
		if err != nil {
			writeError(w, r, err)
			return
		}
		to, err := requiredTime("to", params.To)
		if err != nil {
			writeError(w, r, err)
			return
		}
		d, err := s.orders.Dashboard(r.Context(), from, to)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, dashboardOut(d))
	})(w, r)
}

// requiredTime is timeParam for a parameter that must be there.
func requiredTime(name, v string) (time.Time, error) {
	t, err := timeParam(name, &v)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, fieldErr(name, name+" is required")
	}
	return *t, nil
}

func dashboardOut(d orders.Dashboard) Dashboard {
	out := Dashboard{
		From: d.From, To: d.To,
		NeedsAction: DashboardNeedsAction{
			AwaitingPayment:    int(d.NeedsAction.AwaitingPayment),
			TransfersToConfirm: int(d.NeedsAction.TransfersToConfirm),
			ReadyToShip:        int(d.NeedsAction.ReadyToShip),
			Shipped:            int(d.NeedsAction.Shipped),
			RefundOwed:         int(d.NeedsAction.RefundOwed),
		},
		Current:         figuresOut(d.Current),
		Previous:        figuresOut(d.Previous),
		Daily:           make([]DashboardDay, 0, len(d.Daily)),
		BySource:        make([]DashboardSourceSales, 0, len(d.BySource)),
		ByPaymentMethod: make([]DashboardPaymentSales, 0, len(d.ByPaymentMethod)),
		Customers:       DashboardCustomers{New: d.New, Returning: d.Returning},
		TopProducts:     make([]DashboardProduct, 0, len(d.TopProducts)),
	}
	for _, day := range d.Daily {
		date, _ := time.Parse(time.DateOnly, day.Date)
		out.Daily = append(out.Daily, DashboardDay{Date: openapi_types.Date{Time: date}, Sales: day.Sales, Orders: day.Orders,
			ItemsSold: int(day.ItemsSold)})
	}
	for _, s := range d.BySource {
		out.BySource = append(out.BySource, DashboardSourceSales{Source: OrderSource(s.Key), Sales: s.Sales, Orders: s.Orders})
	}
	for _, m := range d.ByPaymentMethod {
		out.ByPaymentMethod = append(out.ByPaymentMethod,
			DashboardPaymentSales{PaymentMethod: DashboardPaymentSalesPaymentMethod(m.Key), Sales: m.Sales, Orders: m.Orders})
	}
	for _, p := range d.TopProducts {
		cats := make([]CategoryBrief, 0, len(p.Categories))
		for _, c := range p.Categories {
			cats = append(cats, CategoryBrief{Id: c.ID, Name: c.Name, Path: c.Path})
		}
		out.TopProducts = append(out.TopProducts, DashboardProduct{ProductId: p.ProductID, Title: p.Title,
			Categories: cats, ItemsSold: int(p.ItemsSold), Sales: p.Sales})
	}
	return out
}

func figuresOut(f orders.Figures) DashboardFigures {
	return DashboardFigures{Sales: f.Sales, Orders: f.Orders, AverageOrderValue: f.AverageOrderValue, ItemsSold: int(f.ItemsSold)}
}
