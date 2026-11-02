package briefing

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]string{"error": msg})
}
func Handler(s *Store, token string, counters ...BudgetCounter) http.Handler {
	var counter BudgetCounter
	if len(counters) > 0 {
		counter = counters[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.db.PingContext(r.Context()) != nil {
			fail(w, 503, "unhealthy")
			return
		}
		write(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/briefings", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if !validID(key) {
			fail(w, 400, "Idempotency-Key required (maximum 128 bytes)")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
		b, err := io.ReadAll(r.Body)
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				fail(w, 413, "body_too_large")
			} else {
				fail(w, 400, "body_read_error")
			}
			return
		}
		var in Input
		if err = Strict(b, &in); err == nil {
			err = in.Normalize()
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		// Lookup precedes counting, so retries of accepted jobs survive helper errors/capacity.
		respondExisting := func() bool {
			if old, lookupErr := s.Lookup(r.Context(), in, key); lookupErr == nil {
				write(w, 202, map[string]any{"job_id": old.ID, "status": old.Status, "created_at": old.Created})
				return true
			} else if errors.Is(lookupErr, ErrConflict) {
				fail(w, 409, lookupErr.Error())
				return true
			} else if !errors.Is(lookupErr, sql.ErrNoRows) {
				fail(w, 500, "storage_error")
				return true
			}
			return false
		}
		if respondExisting() {
			return
		}
		system, user := PromptInput(in)
		budget, budgetErr := checkBudget(r.Context(), counter, system, user)
		err = budgetErr
		// Another identical request may have committed while this one waited for the helper.
		if (err != nil || !budget.Verified) && respondExisting() {
			return
		}
		if err != nil {
			var exceeded *BudgetExceeded
			if errors.As(err, &exceeded) {
				write(w, 422, struct {
					Error string `json:"error"`
					Budget
				}{"input_budget_exceeded", exceeded.Budget})
			} else {
				fail(w, 503, budgetFailure(err))
			}
			return
		}
		if !budget.Verified {
			fail(w, 503, "context_runtime_not_verified")
			return
		}
		j, err := s.Enqueue(r.Context(), in, key)
		switch {
		case errors.Is(err, ErrConflict):
			fail(w, 409, err.Error())
		case errors.Is(err, ErrFull):
			w.Header().Set("Retry-After", "5")
			fail(w, 429, err.Error())
		case err != nil:
			fail(w, 500, "storage_error")
		default:
			write(w, 202, map[string]any{"job_id": j.ID, "status": j.Status, "created_at": j.Created})
		}
	})
	mux.HandleFunc("GET /v1/briefings/{id}", func(w http.ResponseWriter, r *http.Request) {
		company := r.URL.Query().Get("company_id")
		if !validID(company) {
			fail(w, 400, "company_id required")
			return
		}
		j, err := s.Get(r.Context(), company, r.PathValue("id"))
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "not_found")
			return
		}
		if err != nil {
			fail(w, 500, "storage_error")
			return
		}
		write(w, 200, j)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			header := r.Header.Get("Authorization")
			expected := sha256.Sum256([]byte("Bearer " + token))
			actual := sha256.Sum256([]byte(header))
			if strings.TrimSpace(token) == "" || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				fail(w, 401, "unauthorized")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
