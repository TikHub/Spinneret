package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	all := []BindingRow{
		{PolicyID: "pol_ns", Kind: KindRotation},
		{PolicyID: "pol_site", Kind: KindRotation, SiteID: "sit_a"},
		{PolicyID: "pol_client", Kind: KindRotation, SiteID: "sit_a", Client: "web"},
		{PolicyID: "pol_eg", Kind: KindRotation, SiteID: "sit_a", Client: "web", EndpointGroupID: "eg_search"},
		{PolicyID: "pol_eg_noclient", Kind: KindRotation, SiteID: "sit_a", EndpointGroupID: "eg_feed"},
		{PolicyID: "pol_other_site", Kind: KindRotation, SiteID: "sit_b"},
		{PolicyID: "pol_signal_eg", Kind: KindSignal, SiteID: "sit_a", Client: "web", EndpointGroupID: "eg_search"},
		{PolicyID: "pol_bad_client_only", Kind: KindRotation, Client: "app"},
		{PolicyID: "pol_bad_eg_only", Kind: KindRotation, EndpointGroupID: "eg_detail"},
	}
	tests := []struct {
		name      string
		kind      Kind
		bindings  []BindingRow
		site      string
		client    string
		eg        string
		wantID    string
		wantLevel Level
	}{
		{"endpoint group wins", KindRotation, all, "sit_a", "web", "eg_search", "pol_eg", LevelEndpointGroup},
		{"endpoint group binding without client", KindRotation, all, "sit_a", "app", "eg_feed", "pol_eg_noclient", LevelEndpointGroup},
		{"endpoint group binding with other client falls back", KindRotation, all, "sit_a", "app", "eg_search", "pol_site", LevelSite},
		{"client level", KindRotation, all, "sit_a", "web", "eg_other", "pol_client", LevelClient},
		{"client level without endpoint group", KindRotation, all, "sit_a", "web", "", "pol_client", LevelClient},
		{"site level for other client", KindRotation, all, "sit_a", "app", "eg_x", "pol_site", LevelSite},
		{"site level with empty client", KindRotation, all, "sit_a", "", "", "pol_site", LevelSite},
		{"other site", KindRotation, all, "sit_b", "web", "eg_search", "pol_other_site", LevelSite},
		{"namespace level", KindRotation, all, "sit_c", "web", "eg_detail", "pol_ns", LevelNamespace},
		{"kind filter", KindSignal, all, "sit_a", "web", "eg_search", "pol_signal_eg", LevelEndpointGroup},
		{"kind filter falls to builtin", KindSignal, all, "sit_a", "web", "eg_other", "", LevelBuiltin},
		{"no bindings", KindAction, nil, "sit_a", "web", "eg", "", LevelBuiltin},
		{"endpoint group on another site does not match", KindRotation, []BindingRow{
			{PolicyID: "pol_eg", Kind: KindRotation, SiteID: "sit_z", EndpointGroupID: "eg_search"},
		}, "sit_a", "web", "eg_search", "", LevelBuiltin},
		{"first row wins at same level", KindBreaker, []BindingRow{
			{PolicyID: "pol_1", Kind: KindBreaker, SiteID: "sit_a"},
			{PolicyID: "pol_2", Kind: KindBreaker, SiteID: "sit_a"},
		}, "sit_a", "web", "eg", "pol_1", LevelSite},
		{"order independent across levels", KindBreaker, []BindingRow{
			{PolicyID: "pol_eg", Kind: KindBreaker, SiteID: "sit_a", EndpointGroupID: "eg"},
			{PolicyID: "pol_ns", Kind: KindBreaker},
			{PolicyID: "pol_client", Kind: KindBreaker, SiteID: "sit_a", Client: "web"},
		}, "sit_a", "web", "eg", "pol_eg", LevelEndpointGroup},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, level := Resolve(tc.kind, tc.bindings, tc.site, tc.client, tc.eg)
			require.Equal(t, tc.wantID, id)
			require.Equal(t, tc.wantLevel, level)
		})
	}
}

func TestResolveExtends(t *testing.T) {
	sig := func(name, extends string) *SignalSpec { return &SignalSpec{Name: name, Extends: extends} }
	registry := map[string]Spec{
		"root":      sig("root", ""),
		"mid":       sig("mid", "root"),
		"leaf":      sig("leaf", "mid"),
		"loop-a":    sig("loop-a", "loop-b"),
		"loop-b":    sig("loop-b", "loop-a"),
		"dangling":  sig("dangling", "missing"),
		"d1":        sig("d1", ""),
		"d2":        sig("d2", "d1"),
		"d3":        sig("d3", "d2"),
		"d4":        sig("d4", "d3"),
		"d5":        sig("d5", "d4"),
		"d6":        sig("d6", "d5"),
		"to-action": sig("to-action", "an-action"),
		"an-action": &ActionSpec{Name: "an-action"},
		"liar":      sig("liar", "renamed"),
		"renamed":   sig("someone-else", ""),
		"typed-nil": sig("typed-nil", "nil-spec"),
		"nil-spec":  (*SignalSpec)(nil),
	}
	lookup := func(name string) (Spec, bool) {
		s, ok := registry[name]
		return s, ok
	}
	names := func(chain []Spec) []string {
		out := make([]string, 0, len(chain))
		for _, s := range chain {
			out = append(out, s.PolicyName())
		}
		return out
	}

	chain, err := ResolveExtends(KindSignal, registry["leaf"], lookup)
	require.NoError(t, err)
	require.Equal(t, []string{"root", "mid", "leaf"}, names(chain))

	chain, err = ResolveExtends(KindSignal, registry["root"], nil)
	require.NoError(t, err)
	require.Equal(t, []string{"root"}, names(chain))

	chain, err = ResolveExtends(KindSignal, registry["d5"], lookup)
	require.NoError(t, err)
	require.Len(t, chain, MaxExtendsDepth)

	actionLeaf := &ActionSpec{Name: "child", Extends: "parent"}
	chain, err = ResolveExtends(KindAction, actionLeaf, func(name string) (Spec, bool) {
		return &ActionSpec{Name: name}, name == "parent"
	})
	require.NoError(t, err)
	require.Equal(t, []string{"parent", "child"}, names(chain))

	rot := &RotationSpec{Name: "r"}
	chain, err = ResolveExtends(KindRotation, rot, nil)
	require.NoError(t, err)
	require.Equal(t, []Spec{rot}, chain)
	chain, err = ResolveExtends(KindBreaker, &BreakerSpec{Name: "b"}, nil)
	require.NoError(t, err)
	require.Len(t, chain, 1)

	errorCases := []struct {
		name string
		kind Kind
		leaf Spec
		look func(string) (Spec, bool)
		want string
	}{
		{"nil leaf", KindSignal, nil, lookup, "leaf policy is nil"},
		{"typed nil leaf", KindSignal, (*SignalSpec)(nil), lookup, "leaf policy is nil"},
		{"kind mismatch", KindAction, registry["root"], lookup, `policy "root" has kind signal, expected action`},
		{"self cycle", KindSignal, sig("self", "self"), lookup, "cycle detected: self -> self"},
		{"two cycle", KindSignal, registry["loop-a"], lookup, "cycle detected: loop-a -> loop-b -> loop-a"},
		{"missing parent", KindSignal, registry["dangling"], lookup, `policy "dangling" extends unknown signal policy "missing"`},
		{"too deep", KindSignal, registry["d6"], lookup, "chain d6 -> d5 -> d4 -> d3 -> d2 -> d1 exceeds the maximum depth of 5 policies"},
		{"parent of other kind", KindSignal, registry["to-action"], lookup, `extends "an-action", which has kind action, expected signal`},
		{"lookup returns wrong name", KindSignal, registry["liar"], lookup, `lookup of "renamed" returned policy "someone-else"`},
		{"lookup returns typed nil", KindSignal, registry["typed-nil"], lookup, `extends unknown signal policy "nil-spec"`},
		{"no lookup", KindSignal, registry["leaf"], nil, `policy "leaf" extends "mid" but no lookup was provided`},
	}
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			chain, err := ResolveExtends(tc.kind, tc.leaf, tc.look)
			require.Nil(t, chain)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestResolveExtendsThenCompile(t *testing.T) {
	base := mustSignal(t, "name: base\nrules:\n  - {when: {http_status: [429]}, outcome: rate_limited}\n")
	child := mustSignal(t, "name: child\nextends: base\nrules:\n  - {when: {http_status: [200]}, outcome: success}\n")
	chain, err := ResolveExtends(KindSignal, child, func(name string) (Spec, bool) {
		if name == "base" {
			return base, true
		}
		return nil, false
	})
	require.NoError(t, err)
	specs, err := SignalChain(chain)
	require.NoError(t, err)
	c, err := CompileSignal(specs)
	require.NoError(t, err)
	require.Equal(t, OutcomeRateLimited, c.Classify(ReportFacts{HTTPStatus: 429}).Outcome)
	require.Equal(t, 1, c.Classify(ReportFacts{HTTPStatus: 200}).RuleIndex)
}
