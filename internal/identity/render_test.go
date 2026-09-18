package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func TestRenderDesignDocWebCookie(t *testing.T) {
	ct := compileTestType(t, "web_cookie.yaml")
	payload, err := ct.Normalize(map[string]any{
		"cookies":    "sessionid=a1b2c3; csrf_token=1%7Cabc",
		"user_agent": "Mozilla/5.0 ...",
		"signature":  "x9y8z7",
	})
	require.NoError(t, err)

	cred, usedSecrets, err := ct.Render(context.Background(), payload, nil)
	require.NoError(t, err)
	require.False(t, usedSecrets)
	require.Equal(t, &Credential{
		Cookies:      map[string]string{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc"},
		CookieHeader: "csrf_token=1%7Cabc; sessionid=a1b2c3",
		Headers:      map[string]string{"User-Agent": "Mozilla/5.0 ..."},
		Query:        map[string]string{},
		JSON:         nil,
		Values:       map[string]any{"signature": "x9y8z7"},
	}, cred)

	// The design doc response serializes exactly like this.
	out, err := json.Marshal(map[string]any{
		"cookies": cred.Cookies, "cookie_header": cred.CookieHeader, "headers": cred.Headers,
		"query": cred.Query, "json": cred.JSON, "values": cred.Values,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"cookies": { "sessionid": "a1b2c3", "csrf_token": "1%7Cabc" },
		"cookie_header": "csrf_token=1%7Cabc; sessionid=a1b2c3",
		"headers": { "User-Agent": "Mozilla/5.0 ..." },
		"query": {},
		"json": null,
		"values": { "signature": "x9y8z7" }
	}`, string(out))

	// The credential does not alias the payload.
	cred.Cookies["sessionid"] = "changed"
	require.Equal(t, "a1b2c3", payload["cookies"].(map[string]string)["sessionid"])
}

func TestRenderMissingOptionalFields(t *testing.T) {
	ct := compileTestType(t, "web_cookie.yaml")
	cred, _, err := ct.Render(context.Background(), map[string]any{"cookies": map[string]string{"sessionid": "s"}}, nil)
	require.NoError(t, err)
	require.Equal(t, &Credential{
		Cookies:      map[string]string{"sessionid": "s"},
		CookieHeader: "sessionid=s",
		Headers:      map[string]string{},
		Query:        map[string]string{},
		Values:       map[string]any{},
	}, cred)

	empty, _, err := ct.Render(context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, &Credential{Cookies: map[string]string{}, Headers: map[string]string{}, Query: map[string]string{}, Values: map[string]any{}}, empty)
}

func TestRenderAppDevice(t *testing.T) {
	ct := compileTestType(t, "app_device.yaml")
	// Payload as loaded back from storage with encoding/json (cookie map as map[string]any).
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"device_id": "7300000000000000001",
		"install_id": "7300000000000000002",
		"cookies": {"sessionid": "abc", "install_id": "7300000000000000002"},
		"extra": {"aid": 1001, "channel": "store", "flags": [true, null]}
	}`), &payload))

	cred, used, err := ct.Render(context.Background(), payload, nil)
	require.NoError(t, err)
	require.False(t, used)
	require.Equal(t, map[string]string{"device_id": "7300000000000000001", "iid": "7300000000000000002"}, cred.Query)
	require.Equal(t, "install_id=7300000000000000002; sessionid=abc", cred.CookieHeader)
	require.Empty(t, cred.Cookies)
	require.Equal(t, map[string]any{"aid": 1001.0, "channel": "store", "flags": []any{true, nil}}, cred.JSON)

	// JSON is a deep copy.
	cred.JSON.(map[string]any)["channel"] = "changed"
	require.Equal(t, "store", payload["extra"].(map[string]any)["channel"])

	// Without optional fields: no cookie header, json null.
	cred, _, err = ct.Render(context.Background(), map[string]any{"device_id": "d", "install_id": "i"}, nil)
	require.NoError(t, err)
	require.Equal(t, "", cred.CookieHeader)
	require.Nil(t, cred.JSON)
	require.Equal(t, map[string]string{"device_id": "d", "iid": "i"}, cred.Query)
}

// richSpec exercises every template form.
func richSpec() *TypeSpec {
	return &TypeSpec{
		Name: "rich", Site: "example", Client: "app",
		Fields: map[string]FieldSpec{
			"device_id": {Type: FieldString, Required: true},
			"aid":       {Type: FieldNumber},
			"debug":     {Type: FieldBool},
			"cookies":   {Type: FieldCookieMap},
			"extra":     {Type: FieldJSON},
			"token":     {Type: FieldSecretRef},
			"signer":    {Type: FieldSecretRef},
		},
		Deliver: map[string]any{
			"cookies": map[string]any{
				"sid":     "{{ cookies.sessionid }}",
				"device":  "dev-{{ device_id }}",
				"missing": "{{ cookies.nope }}",
			},
			"cookie_header": "{{ cookies }}; extra={{ device_id }}",
			"headers": map[string]any{
				"Authorization": "Bearer {{ token }}",
				"X-Aid":         "{{ aid }}",
				"X-Debug":       "{{ debug }}",
				"X-Extra":       "{{ extra.meta }}",
				"X-Cookie":      "{{ cookies }}",
				"X-Empty":       "{{ extra.none }}",
			},
			"query": map[string]any{
				"aid":   "{{aid}}",
				"item":  "{{ extra.items.1 }}",
				"oob":   "{{ extra.items.9 }}",
				"idx":   "{{ extra.meta.k.0 }}",
				"const": "v1",
			},
			"values": map[string]any{
				"aid":     "{{ aid }}",
				"debug":   "{{ debug }}",
				"cookies": "{{ cookies }}",
				"meta":    "{{ extra.meta }}",
				"token":   "{{ token }}",
				"signer":  "{{ signer }}",
				"label":   "id:{{ device_id }}",
				"none":    "{{ extra.none }}",
			},
			"json": map[string]any{
				"device":  "{{ device_id }}",
				"aid":     "{{ aid }}",
				"missing": "{{ extra.none }}",
				"list":    []any{"{{ debug }}", 2.0, nil, false, "lit"},
				"nested":  map[string]any{"auth": "{{ token }}", "text": "a{{ aid }}b"},
			},
		},
	}
}

func TestRenderTemplates(t *testing.T) {
	ct := compileSpec(t, richSpec())
	calls := map[string]int{}
	resolve := func(_ context.Context, path string) (string, error) {
		calls[path]++
		return "plain-" + path, nil
	}
	payload := map[string]any{
		"device_id": "d1",
		"aid":       1001.0,
		"debug":     true,
		"cookies":   map[string]string{"sessionid": "s1", "b": "2"},
		"extra":     map[string]any{"meta": map[string]any{"k": []any{"v0"}}, "items": []any{"i0", 5.0}},
		"token":     "signing/token",
		"signer":    "",
	}
	cred, used, err := ct.Render(context.Background(), payload, resolve)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, map[string]int{"signing/token": 1}, calls, "each secret path is resolved once per render")

	require.Equal(t, map[string]string{"sid": "s1", "device": "dev-d1"}, cred.Cookies)
	require.Equal(t, "b=2; sessionid=s1; extra=d1", cred.CookieHeader)
	require.Equal(t, map[string]string{
		"Authorization": "Bearer plain-signing/token",
		"X-Aid":         "1001",
		"X-Debug":       "true",
		"X-Extra":       `{"k":["v0"]}`,
		"X-Cookie":      "b=2; sessionid=s1",
	}, cred.Headers)
	require.Equal(t, map[string]string{"aid": "1001", "item": "5", "idx": "v0", "const": "v1"}, cred.Query)
	require.Equal(t, map[string]any{
		"aid":     1001.0,
		"debug":   true,
		"cookies": map[string]any{"b": "2", "sessionid": "s1"},
		"meta":    map[string]any{"k": []any{"v0"}},
		"token":   "plain-signing/token",
		"label":   "id:d1",
	}, cred.Values)
	require.Equal(t, map[string]any{
		"device":  "d1",
		"aid":     1001.0,
		"missing": nil,
		"list":    []any{true, 2.0, nil, false, "lit"},
		"nested":  map[string]any{"auth": "plain-signing/token", "text": "a1001b"},
	}, cred.JSON)

	// Typed copies never alias the payload.
	cred.Values["meta"].(map[string]any)["k"] = "changed"
	cred.Values["cookies"].(map[string]any)["b"] = "changed"
	require.Equal(t, []any{"v0"}, payload["extra"].(map[string]any)["meta"].(map[string]any)["k"])
	require.Equal(t, "2", payload["cookies"].(map[string]string)["b"])
}

func TestRenderValueRepresentations(t *testing.T) {
	ct := compileSpec(t, richSpec())
	cred, used, err := ct.Render(context.Background(), map[string]any{
		"device_id": "d1",
		"aid":       json.Number("7"),
		"debug":     "false",
		"cookies":   "sessionid=s2",
		"extra":     map[string]any{"meta": "text", "items": []any{"a", json.Number("2.50")}},
	}, nil)
	require.NoError(t, err)
	require.False(t, used)
	require.Equal(t, "7", cred.Headers["X-Aid"])
	require.Equal(t, "false", cred.Headers["X-Debug"])
	require.Equal(t, "text", cred.Headers["X-Extra"])
	require.Equal(t, "2.5", cred.Query["item"])
	require.Equal(t, 7.0, cred.Values["aid"])
	require.Equal(t, false, cred.Values["debug"])
	require.Equal(t, map[string]string{"sid": "s2", "device": "dev-d1"}, cred.Cookies)
	require.Equal(t, "sessionid=s2; extra=d1", cred.CookieHeader)
}

func TestRenderSecretErrors(t *testing.T) {
	ct := compileSpec(t, richSpec())
	payload := map[string]any{"device_id": "d1", "token": "team/token"}

	_, _, err := ct.Render(context.Background(), payload, nil)
	require.ErrorContains(t, err, `field "token" references secret "team/token" but no secret resolver is configured`)

	sentinel := errors.New("vault unavailable")
	_, used, err := ct.Render(context.Background(), payload, func(context.Context, string) (string, error) {
		return "", sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.False(t, used)
	require.ErrorContains(t, err, `render identity type "rich"`)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	_, used, err = ct.Render(ctx, payload, func(got context.Context, _ string) (string, error) {
		require.Equal(t, "marker", got.Value(ctxKey{}))
		return "x", nil
	})
	require.NoError(t, err)
	require.True(t, used)

	_, used, err = ct.Render(context.Background(), map[string]any{"device_id": "d1"}, nil)
	require.NoError(t, err)
	require.False(t, used, "unset secret_ref fields are not resolved")
}

func TestRenderErrors(t *testing.T) {
	ct := compileSpec(t, richSpec())
	okResolver := func(context.Context, string) (string, error) { return "ok", nil }
	tests := []struct {
		name     string
		payload  map[string]any
		resolve  SecretResolver
		contains string
	}{
		{"string field type", map[string]any{"device_id": 1.0}, nil, `field "device_id": expected a string`},
		{"number field type", map[string]any{"aid": "abc"}, nil, `field "aid": expected a number`},
		{"bool field type", map[string]any{"debug": 3.0}, nil, `field "debug": expected a boolean`},
		{"secret ref type", map[string]any{"token": 3.0}, okResolver, `field "token": secret reference must be a string`},
		{"cookie map invalid", map[string]any{"cookies": 3.0}, nil, `field "cookies": cookie map must be`},
		{"rendered cookie invalid", map[string]any{"device_id": "a;b"}, nil, `cookies["device"]: rendered value contains invalid characters`},
		{"rendered header invalid", map[string]any{"token": "x"}, func(context.Context, string) (string, error) { return "a\r\nX-Evil: 1", nil }, `headers["Authorization"]: rendered value contains invalid characters`},
		{"non finite json value", map[string]any{"extra": map[string]any{"meta": []any{json.Number("x")}}}, nil, "invalid number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, used, err := ct.Render(context.Background(), tt.payload, tt.resolve)
			require.Nil(t, cred)
			require.False(t, used)
			require.ErrorContains(t, err, tt.contains)
		})
	}

	// Corrupt stored payloads are server-side failures, never invalid_argument.
	_, _, err := ct.Render(context.Background(), map[string]any{"cookies": "broken"}, nil)
	require.Error(t, err)
	_, isAppErr := apperr.As(err)
	require.False(t, isAppErr)

	web := compileTestType(t, "web_cookie.yaml")
	_, _, err = web.Render(context.Background(), map[string]any{"cookies": "a=1", "user_agent": "UA\n"}, nil)
	require.ErrorContains(t, err, `headers["User-Agent"]: rendered value contains invalid characters`)
	_, _, err = web.Render(context.Background(), map[string]any{"cookies": 5.0}, nil)
	require.ErrorContains(t, err, "cookies: field")

	headerOnly := compileSpec(t, &TypeSpec{
		Name: "h", Site: "s", Client: "web",
		Fields:  map[string]FieldSpec{"raw": {Type: FieldString, Required: true}, "j": {Type: FieldJSON}},
		Deliver: map[string]any{"cookie_header": "{{ raw }}", "json": []any{"{{ j }}"}, "values": map[string]any{"j": "{{ j }}"}},
	})
	_, _, err = headerOnly.Render(context.Background(), map[string]any{"raw": "a=1\n"}, nil)
	require.ErrorContains(t, err, "cookie_header: rendered value contains invalid characters")
	_, _, err = headerOnly.Render(context.Background(), map[string]any{"raw": 1.0}, nil)
	require.ErrorContains(t, err, "cookie_header:")
	_, _, err = headerOnly.Render(context.Background(), map[string]any{"raw": "a", "j": []any{struct{}{}}}, nil)
	require.ErrorContains(t, err, "json:")
	_, _, err = headerOnly.Render(context.Background(), map[string]any{"raw": "a", "j": map[string]any{"x": struct{}{}}}, nil)
	require.ErrorContains(t, err, "json:")
}

func TestRenderSecretMustBeUTF8(t *testing.T) {
	ct := compileSpec(t, richSpec())
	_, used, err := ct.Render(context.Background(), map[string]any{"device_id": "d1", "token": "team/token"},
		func(context.Context, string) (string, error) { return "bad\xff", nil })
	require.False(t, used)
	require.ErrorIs(t, err, errInvalidUTF8)
	require.ErrorContains(t, err, `secret "team/token" for field "token"`)
	require.NotContains(t, err.Error(), "bad")
}

func TestRenderTypedValuesAreJSONTypes(t *testing.T) {
	ct := compileSpec(t, &TypeSpec{
		Name: "typed", Site: "s", Client: "app",
		Fields: map[string]FieldSpec{"id": {Type: FieldString, Required: true}, "j": {Type: FieldJSON}},
		Deliver: map[string]any{
			"values": map[string]any{"list": "{{ j.list }}", "map": "{{ j.map }}", "int": "{{ j.int }}", "raw": "{{ j.raw }}"},
			"json":   "{{ j }}",
		},
	})
	list := []string{"a", "b"}
	inner := map[any]any{"k": int32(7)}
	payload := map[string]any{"id": "x", "j": map[string]any{"list": list, "map": inner, "int": uint16(3), "raw": "s"}}
	cred, _, err := ct.Render(context.Background(), payload, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"list": []any{"a", "b"},
		"map":  map[string]any{"k": 7.0},
		"int":  3.0,
		"raw":  "s",
	}, cred.Values)
	require.Equal(t, map[string]any{
		"list": []any{"a", "b"},
		"map":  map[string]any{"k": 7.0},
		"int":  3.0,
		"raw":  "s",
	}, cred.JSON)
	cred.Values["list"].([]any)[0] = "changed"
	require.Equal(t, "a", list[0], "typed values never alias the payload")

	_, _, err = ct.Render(context.Background(), map[string]any{"id": "x", "j": map[string]any{"list": []any{make(chan int)}}}, nil)
	require.ErrorContains(t, err, "unsupported value of type chan int")
}

func TestCompileRejectsWholeCookieMapAsCookieValue(t *testing.T) {
	spec := &TypeSpec{
		Name: "c", Site: "s", Client: "web",
		Fields:  map[string]FieldSpec{"cookies": {Type: FieldCookieMap, Required: true}},
		Deliver: map[string]any{"cookies": map[string]any{"all": "x{{ cookies }}"}},
	}
	requireInvalid(t, spec.Validate(), `deliver.cookies["all"]: placeholder "cookies" renders a whole cookie map, use {{ cookies.<name> }}`)
	spec.Deliver = map[string]any{"cookies": map[string]any{"sid": "{{ cookies.sessionid }}"}}
	require.NoError(t, spec.Validate())
}

func TestRenderSegmentErrorsAreLabeled(t *testing.T) {
	spec := &TypeSpec{
		Name: "labels", Site: "s", Client: "web",
		Fields: map[string]FieldSpec{"n": {Type: FieldNumber, Required: true}},
	}
	bad := map[string]any{"n": "not-a-number"}
	for seg, deliver := range map[string]any{
		"cookies[":  map[string]any{"cookies": map[string]any{"c": "{{ n }}"}},
		"query[":    map[string]any{"query": map[string]any{"q": "{{ n }}"}},
		"values[":   map[string]any{"values": map[string]any{"v": "x{{ n }}"}},
		"json:":     map[string]any{"json": map[string]any{"a": []any{"{{ n }}"}}},
		"headers[":  map[string]any{"headers": map[string]any{"H": "{{ n }}"}},
		"cookie_he": map[string]any{"cookie_header": "x={{ n }}"},
	} {
		spec.Deliver = deliver.(map[string]any)
		ct := compileSpec(t, spec)
		_, _, err := ct.Render(context.Background(), bad, nil)
		require.ErrorContains(t, err, seg)
	}
}

// TestCompiledTypeConcurrentUse exercises the documented concurrency safety of
// CompiledType; run with -race.
func TestCompiledTypeConcurrentUse(t *testing.T) {
	ct := compileSpec(t, richSpec())
	raw := map[string]any{
		"device_id": "d1", "aid": "1001", "debug": "true",
		"cookies": "sessionid=s1; b=2", "extra": map[string]any{"meta": "m", "items": []any{"a", 1.0}},
		"token": "team/token",
	}
	resolve := func(context.Context, string) (string, error) { return "plain", nil }
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				payload, err := ct.Normalize(raw)
				if err != nil {
					errs <- err
					return
				}
				if _, err := ct.UniqueKey(payload); err != nil {
					errs <- err
					return
				}
				cred, used, err := ct.Render(context.Background(), payload, resolve)
				if err != nil {
					errs <- fmt.Errorf("render: %w", err)
					return
				}
				if !used || cred.Headers["Authorization"] != "Bearer plain" {
					errs <- fmt.Errorf("render: used=%v authorization=%q", used, cred.Headers["Authorization"])
					return
				}
				// Mutating a credential must not affect other renders.
				cred.Values["meta"] = "changed"
				cred.Cookies["sid"] = "changed"
				_ = ct.Mask(payload)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestStringify(t *testing.T) {
	s, err := stringify([]any{"a", 1.0})
	require.NoError(t, err)
	require.Equal(t, `["a",1]`, s)
	_, err = stringify(float64Inf())
	require.Error(t, err)
	_, err = stringify(map[string]any{"x": float64Inf()})
	require.Error(t, err)
	require.Nil(t, lookupJSON(map[string]string{"a": "b"}, []string{"x"}))
	require.Equal(t, "b", lookupJSON(map[string]string{"a": "b"}, []string{"a"}))
	require.Nil(t, lookupJSON("scalar", []string{"a"}))
	require.Nil(t, lookupJSON([]any{"a"}, []string{"-1"}))
}

func float64Inf() float64 {
	zero := 0.0
	return 1 / zero
}

func BenchmarkRender(b *testing.B) {
	web := compileTestType(b, "web_cookie.yaml")
	webPayload, err := web.Normalize(map[string]any{
		"cookies":    "sessionid=a1b2c3; csrf_token=1%7Cabc; device_id=0123456789abcdef; install_id=deadbeef; signature=verify_x",
		"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36",
		"signature":  "x9y8z7x9y8z7x9y8z7",
	})
	require.NoError(b, err)
	app := compileTestType(b, "app_device.yaml")
	appPayload, err := app.Normalize(map[string]any{
		"device_id":  "7300000000000000001",
		"install_id": "7300000000000000002",
		"extra":      map[string]any{"aid": 1001.0, "channel": "store"},
	})
	require.NoError(b, err)
	ctx := context.Background()

	b.Run("web_cookie", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := web.Render(ctx, webPayload, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("app_device", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := app.Render(ctx, appPayload, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestSecretRefsAreNamespaceRelative(t *testing.T) {
	ct := compileSpec(t, richSpec())
	for _, path := range []string{"signing/../prod/db", "signing//token", "signing/token/", "signing/./token"} {
		_, err := ct.Normalize(map[string]any{"device_id": "d", "token": path})
		require.Error(t, err, path)
		require.Contains(t, err.Error(), "token: expected a secret path", path)

		// Payloads stored before the check never resolve such a path.
		resolved := false
		resolve := func(context.Context, string) (string, error) {
			resolved = true
			return "plain", nil
		}
		_, _, err = ct.Render(context.Background(), map[string]any{"device_id": "d", "token": path}, resolve)
		require.Error(t, err, path)
		require.Contains(t, err.Error(), "not a namespace-relative secret path")
		require.False(t, resolved, path)
	}
}
