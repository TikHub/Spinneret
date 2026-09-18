package site_test

import (
	"fmt"

	"github.com/TikHub/Spinneret/internal/site"
)

func Example() {
	m, err := site.NewMatcher([]site.Rule{
		{ID: "uri_1", GroupID: "eg_detail", GroupName: "item_detail", Kind: site.RuleExact, Pattern: "/api/v1/item/detail/"},
		{ID: "uri_2", GroupID: "eg_item", GroupName: "item", Kind: site.RuleTemplate, Pattern: "/api/item/{id}/"},
		{ID: "uri_3", GroupID: "eg_search", GroupName: "search", Kind: site.RulePrefix, Pattern: "/api/v1/search/"},
	}, "eg_default")
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, uri := range []string{
		"https://target.example.com/api/v1/item/detail/?item_id=1",
		"/api/item/42/",
		"/api/v1/search/single/?keyword=cat",
		"/api/item/42",
	} {
		res, err := m.MatchURI(uri)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println(res.GroupName, res.Kind, res.Default)
	}
	// Output:
	// item_detail exact false
	// item template false
	// search prefix false
	// _default  true
}
