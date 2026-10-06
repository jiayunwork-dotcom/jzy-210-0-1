package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"balancer/internal/service"
	"balancer/internal/store"
)

func newTestEcho() (*service.Service, http.Handler) {
	svc := service.New(store.NewMemory())
	return svc, NewServer(svc)
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func mustID(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key].(string)
	if !ok {
		t.Fatalf("missing %s in %v", key, m)
	}
	return v
}

func createMachine(t *testing.T, h http.Handler, body map[string]any) string {
	t.Helper()
	code, m := doJSON(t, h, http.MethodPost, "/api/v1/machines", body)
	if code != http.StatusCreated {
		t.Fatalf("create machine %d %v", code, m)
	}
	return mustID(t, m, "id")
}

func createJob(t *testing.T, h http.Handler, machineID string, useHistory bool) (string, map[string]any) {
	t.Helper()
	code, m := doJSON(t, h, http.MethodPost, "/api/v1/jobs",
		map[string]any{"machineId": machineID, "useHistory": useHistory})
	if code != http.StatusCreated {
		t.Fatalf("create job %d %v", code, m)
	}
	job, _ := m["job"].(map[string]any)
	return mustID(t, job, "id"), m
}

func defaultMachineBody(holes int) map[string]any {
	planes := []map[string]any{{"id": "A", "name": "impeller", "holeCount": holes}}
	return map[string]any{
		"name":       "fan-1",
		"planes":     planes,
		"points":     []map[string]any{{"id": "V", "name": "DE-H"}},
		"speedsRpm":  []int{1500},
		"convention": map[string]any{"zeroOffsetDeg": 0, "direction": 1},
	}
}

func runBody(kind string, min int, clientID string, weights, readings []any) map[string]any {
	runAt := time.Date(2026, 10, 6, 10, min, 0, 0, time.UTC).Format(time.RFC3339Nano)
	b := map[string]any{"kind": kind, "runAt": runAt, "weights": weights, "readings": readings}
	if clientID != "" {
		b["clientId"] = clientID
	}
	return b
}

func polar(amp, phase float64) map[string]any {
	return map[string]any{"amp": amp, "phase": phase}
}

// End-to-end reference example through the HTTP API.
func TestHTTPReferenceExample(t *testing.T) {
	_, h := newTestEcho()
	mid := createMachine(t, h, defaultMachineBody(0))
	jid, _ := createJob(t, h, mid, false)

	code, m := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("original", 0, "", nil, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(80, 30)},
		}))
	if code != http.StatusCreated {
		t.Fatalf("original %d %v", code, m)
	}

	code, m = doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("trial", 1, "", []any{
			map[string]any{"planeId": "A", "polar": polar(20, 0)},
		}, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(89.44, 56.57)},
		}))
	if code != http.StatusCreated {
		t.Fatalf("trial %d %v", code, m)
	}
	state, _ := m["state"].(map[string]any)
	plans, _ := state["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("plans %v", plans)
	}
	plan := plans[0].(map[string]any)
	weights, _ := plan["weights"].([]any)
	w0 := weights[0].(map[string]any)
	pp := w0["polar"].(map[string]any)
	if mathAbs(pp["amp"].(float64)-40) > 0.05 || mathAbs(pp["phase"].(float64)-90) > 0.05 {
		t.Fatalf("correction %v", w0)
	}
	residuals, _ := plan["predictedResidual"].([]any)
	r0 := residuals[0].(map[string]any)
	if r0["ampUm"].(float64) > 1e-6 {
		t.Fatalf("residual %v", r0)
	}

	// GET state independently.
	code, gm := doJSON(t, h, http.MethodGet, "/api/v1/jobs/"+jid, nil)
	if code != http.StatusOK {
		t.Fatalf("get %d", code)
	}
	if _, ok := gm["state"]; !ok {
		t.Fatal("state missing")
	}
}

// Rejections carry field names and HTTP 422.
func TestHTTPRejections(t *testing.T) {
	_, h := newTestEcho()
	// Negative amplitude on machine-independent reading needs a job first.
	mid := createMachine(t, h, defaultMachineBody(0))
	jid, _ := createJob(t, h, mid, false)

	code, m := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("original", 0, "", nil, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(-1, 30)},
		}))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d %v", code, m)
	}
	fields, _ := m["fields"].([]any)
	found := false
	for _, f := range fields {
		fm := f.(map[string]any)
		if fm["field"] == "readings[0].polar.amp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected amplitude field in %v", fields)
	}

	// Machine-level: too few holes.
	bad := defaultMachineBody(2)
	bad["name"] = "bad-holes"
	code, m = doJSON(t, h, http.MethodPost, "/api/v1/machines", bad)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d %v", code, m)
	}

	// 404 for unknown job.
	code, _ = doJSON(t, h, http.MethodGet, "/api/v1/jobs/nope", nil)
	if code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}

// Correcting a run via HTTP yields the same plan as a correct entry.
func TestHTTPCorrection(t *testing.T) {
	_, h := newTestEcho()
	mid := createMachine(t, h, defaultMachineBody(0))
	jid, _ := createJob(t, h, mid, false)
	doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("original", 0, "", nil, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(80, 30)},
		}))
	_, res := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("trial", 1, "", []any{
			map[string]any{"planeId": "A", "polar": polar(20, 0)},
		}, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(89.44, 236.57)}, // wrong
		}))
	state := res["state"].(map[string]any)
	runs, _ := state["runs"].([]any)
	runID := runs[1].(map[string]any)["runId"].(string)

	code, m := doJSON(t, h, http.MethodPost,
		"/api/v1/jobs/"+jid+"/runs/"+runID+"/corrections",
		map[string]any{"readings": []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(89.44, 56.57)},
		}})
	if code != http.StatusOK {
		t.Fatalf("correct %d %v", code, m)
	}
	st := m["state"].(map[string]any)
	plans, _ := st["plans"].([]any)
	w := plans[0].(map[string]any)["weights"].([]any)[0].(map[string]any)["polar"].(map[string]any)
	if mathAbs(w["amp"].(float64)-40) > 0.05 || mathAbs(w["phase"].(float64)-90) > 0.05 {
		t.Fatalf("post-correction plan %v", w)
	}
}

// Two clients POST simultaneously: both runs are retained.
func TestHTTPConcurrentRuns(t *testing.T) {
	_, h := newTestEcho()
	mid := createMachine(t, h, defaultMachineBody(0))
	jid, _ := createJob(t, h, mid, false)
	doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
		runBody("original", 0, "", nil, []any{
			map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(80, 30)},
		}))

	var wg sync.WaitGroup
	for i, rb := range []map[string]any{
		runBody("trial", 3, "A", []any{
			map[string]any{"planeId": "A", "polar": polar(20, 0)}},
			[]any{map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(89.44, 56.57)}}),
		runBody("verify", 2, "B", []any{
			map[string]any{"planeId": "A", "polar": polar(40, 90)}},
			[]any{map[string]any{"pointId": "V", "speedRpm": 1500, "polar": polar(1, 100)}}),
	} {
		wg.Add(1)
		go func(_ int, body map[string]any) {
			defer wg.Done()
			code, _ := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", body)
			if code != http.StatusCreated {
				t.Errorf("concurrent POST code %d", code)
			}
		}(i, rb)
	}
	wg.Wait()
	_, m := doJSON(t, h, http.MethodGet, "/api/v1/jobs/"+jid, nil)
	state := m["state"].(map[string]any)
	runs, _ := state["runs"].([]any)
	if len(runs) != 3 {
		t.Fatalf("runs retained = %d, want 3", len(runs))
	}
	// Field-time order: verify (minute 2) before trial (minute 3).
	k1 := runs[1].(map[string]any)["kind"].(string)
	k2 := runs[2].(map[string]any)["kind"].(string)
	if k1 != "verify" || k2 != "trial" {
		t.Fatalf("order %s, %s", k1, k2)
	}
}

func TestHTTPHealth(t *testing.T) {
	_, h := newTestEcho()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health %d", rec.Code)
	}
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
