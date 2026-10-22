// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, checks ...Check) (int, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	New(checks...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var res response
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	return rec.Code, res
}

func TestHealthyWhenEveryCheckPasses(t *testing.T) {
	code, res := serve(t, Func("db", func(context.Context) error { return nil }))
	if code != http.StatusOK || !res.OK || res.Checks["db"] != "ok" {
		t.Fatalf("%d %+v", code, res)
	}
}

func TestOneFailingCheckIs503AndNamed(t *testing.T) {
	code, res := serve(t,
		Func("db", func(context.Context) error { return nil }),
		Func("scheduler", func(context.Context) error { return errors.New("password=hunter2") }))
	if code != http.StatusServiceUnavailable || res.OK {
		t.Fatalf("%d %+v", code, res)
	}
	if res.Checks["db"] != "ok" || res.Checks["scheduler"] != "unavailable" {
		t.Fatalf("checks %v", res.Checks)
	}
	if strings.Contains(res.Checks["scheduler"], "hunter2") {
		t.Fatal("an error text from a dependency leaked into the response")
	}
}

func TestFresh(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct {
		last, started time.Time
		pass          bool
	}{
		"recent":                  {now.Add(-10 * time.Second), now.Add(-time.Hour), true},
		"stale":                   {now.Add(-5 * time.Minute), now.Add(-time.Hour), false},
		"no event yet, just up":   {time.Time{}, now.Add(-10 * time.Second), true},
		"no event yet, long time": {time.Time{}, now.Add(-time.Hour), false},
	} {
		err := Fresh("x", func() time.Time { return tc.last }, 90*time.Second, tc.started).Fn(context.Background())
		if (err == nil) != tc.pass {
			t.Errorf("%s: %v", name, err)
		}
	}
}
