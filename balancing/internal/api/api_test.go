package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/cmplx"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"balancing/internal/store"
)

func complexPolar(amp, deg float64) complex128 {
	r := deg * math.Pi / 180
	return complex(amp*math.Cos(r), amp*math.Sin(r))
}
func cmplxAbs(z complex128) float64 { return cmplx.Abs(z) }
func cmplxPhase(z complex128) float64 {
	return cmplx.Phase(z) * 180 / math.Pi
}
func normDeg(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

func newTestServer(t *testing.T) (*Server, store.Store) {
	t.Helper()
	s := store.NewMemoryStore()
	return NewServer(s), s
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

func mustID(t *testing.T, m map[string]any) string {
	t.Helper()
	id, ok := m["id"].(string)
	if !ok {
		t.Fatalf("no id in response: %v", m)
	}
	return id
}

func createTestMachine(t *testing.T, h http.Handler, planes, points int, holes []any) string {
	t.Helper()
	body := map[string]any{
		"name": "fan", "plane_count": planes, "point_count": points,
		"speeds": []int{1500}, "phase_direction": "against_rotation",
		"reference_deg": 0, "fusion_strategy": "uncertainty_weighted",
		"hole_layout": holes,
	}
	code, resp := doJSON(t, h, http.MethodPost, "/api/v1/machines", body)
	if code != http.StatusCreated {
		t.Fatalf("create machine: %d %v", code, resp)
	}
	return mustID(t, resp)
}

func rfc(min int) string {
	return time.Date(2026, 3, 2, 10, min, 0, 0, time.UTC).Format(time.RFC3339)
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)
	code, body := doJSON(t, srv.Handler(), http.MethodGet, "/api/v1/health", nil)
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("health: %d %v", code, body)
	}
}

// TestHTTPReferenceFlow walks the full single-plane workflow over HTTP and
// verifies the resulting coefficient and correction.
func TestHTTPReferenceFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	mid := createTestMachine(t, h, 1, 1, nil)
	code, resp := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs",
		map[string]any{"operator": "li"})
	if code != 201 {
		t.Fatalf("create job: %d %v", code, resp)
	}
	jid := mustID(t, resp)

	postRun := func(body map[string]any) {
		t.Helper()
		code, r := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", body)
		if code != 201 {
			t.Fatalf("post run: %d %v", code, r)
		}
	}
	postRun(map[string]any{
		"kind": "original", "run_time": rfc(0),
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 80, "phase_deg": 30}},
	})
	postRun(map[string]any{
		"kind": "trial", "run_time": rfc(1),
		"weights":      []map[string]any{{"plane": 0, "grams": 20, "angle_deg": 0}},
		"trial_plane":  0,
		"trial_weight": map[string]any{"grams": 20, "angle_deg": 0},
		"readings":     []map[string]any{{"speed": 1500, "point": 0, "amp_um": 89.44, "phase_deg": 56.57}},
	})

	code, job := doJSON(t, h, http.MethodGet, "/api/v1/jobs/"+jid, nil)
	if code != 200 {
		t.Fatalf("get job: %d %v", code, job)
	}
	sr := firstSpeed(t, job)
	if status, _ := sr["status"].(string); status != "ok" {
		t.Fatalf("status: %v", sr)
	}
	inf := sr["influence"].([]any)[0].([]any)[0].(map[string]any)
	if approx(inf["amp"].(float64), 2, 1e-3) != nil || approx(inf["phase_deg"].(float64), 120, 1e-2) != nil {
		t.Errorf("influence %v", inf)
	}
	corr := sr["correction"].([]any)[0].(map[string]any)
	if e := approx(corr["amp"].(float64), 40, 1e-2); e != nil {
		t.Errorf("correction mass: %v", e)
	}
	if e := approx(corr["phase_deg"].(float64), 90, 1e-2); e != nil {
		t.Errorf("correction angle: %v", e)
	}
}

// TestHTTPRejectionFields asserts the named-field rejections over HTTP.
func TestHTTPRejectionFields(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	mid := createTestMachine(t, h, 1, 1, nil)
	_, jresp := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs",
		map[string]any{"operator": "li"})
	jid := mustID(t, jresp)

	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"negative amp", map[string]any{
			"kind": "original", "run_time": rfc(0),
			"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": -3, "phase_deg": 0}},
		}, "negative_amplitude"},
		{"bad phase", map[string]any{
			"kind": "original", "run_time": rfc(0),
			"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 1, "phase_deg": 400}},
		}, "phase_out_of_range"},
		{"zero trial weight", map[string]any{
			"kind": "trial", "run_time": rfc(0), "trial_plane": 0,
			"trial_weight": map[string]any{"grams": 0, "angle_deg": 0},
			"readings":     []map[string]any{{"speed": 1500, "point": 0, "amp_um": 1, "phase_deg": 0}},
		}, "zero_trial_weight"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", tc.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %v", code, resp)
			}
			fields := resp["fields"].([]any)
			found := false
			for _, f := range fields {
				if f.(map[string]any)["code"] == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("fields %v lack %s", fields, tc.code)
			}
		})
	}

	// geometry / hole rejections at machine creation
	bad := []map[string]any{
		{"name": "x", "plane_count": 3, "point_count": 1, "speeds": []int{1}, "phase_direction": "against_rotation"},
		{"name": "x", "plane_count": 1, "point_count": 9, "speeds": []int{1}, "phase_direction": "against_rotation"},
		{"name": "x", "plane_count": 1, "point_count": 1, "speeds": []int{1}, "phase_direction": "against_rotation",
			"hole_layout": []map[string]any{{"count": 2}}},
	}
	for _, b := range bad {
		code, resp := doJSON(t, h, http.MethodPost, "/api/v1/machines", b)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d: %v", code, resp)
		}
	}
}

// TestHTTPCorrectionAppendsEvent: correcting a run appends an event and the
// corrected flag appears on the run.
func TestHTTPCorrectionAppendsEvent(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	mid := createTestMachine(t, h, 1, 1, nil)
	_, jr := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs", map[string]any{"operator": "li"})
	jid := mustID(t, jr)
	doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", map[string]any{
		"kind": "original", "run_time": rfc(0),
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 80, "phase_deg": 30}},
	})
	doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", map[string]any{
		"kind": "trial", "run_time": rfc(1),
		"weights":     []map[string]any{{"plane": 0, "grams": 20, "angle_deg": 0}},
		"trial_plane": 0, "trial_weight": map[string]any{"grams": 20, "angle_deg": 0},
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 89.44, "phase_deg": 236.57}},
	})
	code, resp := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/corrections", map[string]any{
		"target_run_time": rfc(1),
		"readings":        []map[string]any{{"speed": 1500, "point": 0, "amp_um": 89.44, "phase_deg": 56.57}},
	})
	if code != 201 {
		t.Fatalf("correction: %d %v", code, resp)
	}
	code, job := doJSON(t, h, http.MethodGet, "/api/v1/jobs/"+jid, nil)
	runs := job["runs"].([]any)
	var corrected bool
	for _, rr := range runs {
		if rr.(map[string]any)["corrected"].(bool) {
			corrected = true
		}
	}
	if !corrected {
		t.Fatal("corrected flag missing")
	}
	sr := firstSpeed(t, job)
	inf := sr["influence"].([]any)[0].([]any)[0].(map[string]any)
	if e := approx(inf["phase_deg"].(float64), 120, 1e-2); e != nil {
		t.Errorf("post-correction influence: %v", e)
	}
}

// TestHTTPConcurrentRuns both arrive and are both returned, ordered by seq.
func TestHTTPConcurrentRuns(t *testing.T) {
	srv, _ := newTestServer(t)
	hd := srv.Handler()
	mid := createTestMachine(t, hd, 2, 2, nil)
	_, jr := doJSON(t, hd, http.MethodPost, "/api/v1/machines/"+mid+"/jobs", map[string]any{"operator": "li"})
	jid := mustID(t, jr)
	doJSON(t, hd, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", map[string]any{
		"kind": "original", "run_time": rfc(0),
		"readings": []map[string]any{
			{"speed": 1500, "point": 0, "amp_um": 60, "phase_deg": 20},
			{"speed": 1500, "point": 1, "amp_um": 45, "phase_deg": 200},
		},
	})
	// readings under a planted response matrix, computed in Go so each trial
	// actually moves the rotor enough to pass the effect check.
	cw := complexPolar
	hmat := [2][2]complex128{
		{cw(1.4, 45), cw(0.7, -20)},
		{cw(0.5, 160), cw(1.1, 210)},
	}
	v0m := [2]complex128{cw(60, 20), cw(45, 200)}
	trialReadings := func(w [2]complex128) []map[string]any {
		out := make([]map[string]any, 2)
		for i := 0; i < 2; i++ {
			v := v0m[i] + hmat[i][0]*w[0] + hmat[i][1]*w[1]
			out[i] = map[string]any{"speed": 1500, "point": i,
				"amp_um": cmplxAbs(v), "phase_deg": normDeg(cmplxPhase(v))}
		}
		return out
	}
	body := func(plane int, grams float64, angle float64, rd []map[string]any) map[string]any {
		return map[string]any{
			"kind": "trial", "run_time": rfc(5),
			"weights":      []map[string]any{{"plane": plane, "grams": grams, "angle_deg": angle}},
			"trial_plane":  plane,
			"trial_weight": map[string]any{"grams": grams, "angle_deg": angle},
			"readings":     rd,
		}
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		doJSON(t, hd, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
			body(0, 18, 0, trialReadings([2]complex128{cw(18, 0), 0})))
	}()
	go func() {
		defer wg.Done()
		doJSON(t, hd, http.MethodPost, "/api/v1/jobs/"+jid+"/runs",
			body(1, 14, 30, trialReadings([2]complex128{0, cw(14, 30)})))
	}()
	wg.Wait()
	code, job := doJSON(t, hd, http.MethodGet, "/api/v1/jobs/"+jid, nil)
	if code != 200 {
		t.Fatal(code)
	}
	runs := job["runs"].([]any)
	var sameTime int
	for _, rr := range runs {
		if strings.Contains(rr.(map[string]any)["run_time"].(string), "10:05:") {
			sameTime++
		}
	}
	if sameTime != 2 {
		t.Fatalf("concurrent runs retained: %d", sameTime)
	}
}

// TestHTTPSmallTrialEffectRejected covers the dynamic ill-conditioned case.
func TestHTTPSmallTrialEffectRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	mid := createTestMachine(t, h, 1, 1, nil)
	_, jr := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs", map[string]any{"operator": "li"})
	jid := mustID(t, jr)
	doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", map[string]any{
		"kind": "original", "run_time": rfc(0),
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 80, "phase_deg": 30}},
	})
	code, resp := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", map[string]any{
		"kind": "trial", "run_time": rfc(1),
		"weights":     []map[string]any{{"plane": 0, "grams": 20, "angle_deg": 0}},
		"trial_plane": 0, "trial_weight": map[string]any{"grams": 20, "angle_deg": 0},
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 80.1, "phase_deg": 30.05}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d %v", code, resp)
	}
	if !strings.Contains(fmt.Sprint(resp["fields"]), "small_trial_effect") {
		t.Fatalf("want small_trial_effect, got %v", resp["fields"])
	}
}

// TestHTTPHistoryReuseAndReject establishes a machine, verifies history, then a
// reuse job is allowed; deleting history by failing verification makes the next
// reuse request return 409.
func TestHTTPHistoryReuseAndReject(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	mid := createTestMachine(t, h, 1, 1, nil)
	_, jr := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs", map[string]any{"operator": "li"})
	j1 := mustID(t, jr)
	post := func(jid string, body map[string]any) {
		code, r := doJSON(t, h, http.MethodPost, "/api/v1/jobs/"+jid+"/runs", body)
		if code != 201 {
			t.Fatalf("%d %v", code, r)
		}
	}
	post(j1, map[string]any{"kind": "original", "run_time": rfc(0),
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 80, "phase_deg": 30}}})
	post(j1, map[string]any{"kind": "trial", "run_time": rfc(1),
		"weights":     []map[string]any{{"plane": 0, "grams": 20, "angle_deg": 0}},
		"trial_plane": 0, "trial_weight": map[string]any{"grams": 20, "angle_deg": 0},
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 89.44, "phase_deg": 56.57}}})
	_, job := doJSON(t, h, http.MethodGet, "/api/v1/jobs/"+j1, nil)
	corr := firstSpeed(t, job)["correction"].([]any)[0].(map[string]any)
	post(j1, map[string]any{"kind": "verification", "run_time": rfc(2),
		"weights":  []map[string]any{{"plane": 0, "grams": corr["amp"], "angle_deg": corr["phase_deg"]}},
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 0.001, "phase_deg": 0}}})

	code, hist := doJSON(t, h, http.MethodGet, "/api/v1/machines/"+mid+"/history", nil)
	if code != 200 {
		t.Fatalf("history %d", code)
	}
	speeds := hist["speeds"].([]any)
	if avail := speeds[0].(map[string]any)["available"].(bool); !avail {
		t.Fatal("history not available")
	}

	// reuse job is accepted and immediately offers a correction from history
	code, rj := doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs",
		map[string]any{"operator": "wang", "use_history": true})
	if code != 201 {
		t.Fatalf("reuse create %d %v", code, rj)
	}
	j2 := mustID(t, rj)
	post(j2, map[string]any{"kind": "original", "run_time": rfc(10),
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 70, "phase_deg": 40}}})
	// model fails badly: verification contradicts the prediction
	post(j2, map[string]any{"kind": "verification", "run_time": rfc(11),
		"weights":  []map[string]any{{"plane": 0, "grams": 100, "angle_deg": 180}},
		"readings": []map[string]any{{"speed": 1500, "point": 0, "amp_um": 200, "phase_deg": 270}}})

	code, _ = doJSON(t, h, http.MethodPost, "/api/v1/machines/"+mid+"/jobs",
		map[string]any{"operator": "zhao", "use_history": true})
	if code != http.StatusConflict {
		t.Fatalf("want 409 after invalidation, got %d", code)
	}
}

func firstSpeed(t *testing.T, job map[string]any) map[string]any {
	t.Helper()
	res, ok := job["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", job)
	}
	sp := res["speeds"].([]any)
	if len(sp) == 0 {
		t.Fatal("no speed results")
	}
	return sp[0].(map[string]any)
}

func approx(got, want, tol float64) error {
	if d := got - want; d > tol || d < -tol {
		return fmt.Errorf("got %.5f want %.5f", got, want)
	}
	return nil
}
