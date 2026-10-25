package orchestration

import "testing"

func TestNormalizeTool(t *testing.T) {
	cases := map[string]string{
		"search_products": "purchase.search",
		"SearchProducts":  "purchase.search",
		"buy":             "purchase.checkout",
		"create_event":    "calendar.add",
		"remind_me":       "calendar.add",
		"list_events":     "calendar.list",
		"repair":          "property.report",
		"calendar.add":    "calendar.add",
		"purchase.search": "purchase.search",
	}
	for in, want := range cases {
		if got := normalizeTool(in); got != want {
			t.Errorf("normalizeTool(%q)=%q want %q", in, got, want)
		}
	}
}
