package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"qqbot/internal/logging"
)

// SetLogs installs the log manager behind the 日志 page. Optional: without it
// the log endpoints answer 503, and saving the settings simply has no logger to
// re-level.
func (r *Router) SetLogs(m *logging.Manager) { r.logs = m }

// statusRecorder remembers what a handler answered, so the request can be
// logged after the fact with its status and, for a failure, the error text.
type statusRecorder struct {
	http.ResponseWriter
	status int
	err    error
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// noteErr attaches the error a handler is answering with to the request's log
// record. writeAuthErr calls it with the real error even though the client only
// sees a generic line, so the detail it hides is still in the log.
func noteErr(w http.ResponseWriter, err error) {
	for w != nil {
		if s, ok := w.(*statusRecorder); ok {
			s.err = err
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// traceRequests logs every API request after it completes. A 5xx is an ERROR —
// it is our fault, and it is the record the operator is looking for when a
// button "did nothing" — while everything else is DEBUG, so INFO is not one line
// per panel poll. Static assets are not traced at all.
func traceRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.URL.Path, "/api/") {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, req)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{
			"method", req.Method,
			"path", req.URL.Path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if rec.err != nil {
			attrs = append(attrs, "err", rec.err.Error())
		}
		if status >= 500 {
			slog.Error("请求失败", attrs...)
			return
		}
		slog.Debug("请求", attrs...)
	})
}

// listLogs returns one day's records, filtered for the 日志 page.
func (r *Router) listLogs(w http.ResponseWriter, req *http.Request) {
	if r.logs == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("本实例未启用日志文件"))
		return
	}
	q := req.URL.Query()
	// Absent means the server's default cap; an explicit 0 means "as many as
	// the server will return", which is how a caller asks for the whole day.
	limit := logging.DefaultLimit
	if v, present := q["limit"]; present {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 0 {
			writeErr(w, 400, errors.New("limit 必须是非负整数"))
			return
		}
		if n == 0 {
			n = logging.MaxLimit
		}
		limit = n
	}
	res, err := r.logs.Query(logging.Query{
		Day:      q.Get("day"),
		MinLevel: q.Get("level"),
		Search:   q.Get("q"),
		Limit:    limit,
	})
	if err != nil {
		var bad *logging.QueryError
		if errors.As(err, &bad) {
			writeErr(w, 400, err)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, logResponse(res, r.logs))
}

// logResponse adds the day's timezone. The rows carry absolute timestamps, and
// a browser in another timezone would otherwise render a record filed under the
// operator's 10-13 as "10-12 15:54" — the very confusion the day boundary
// follows the operator's calendar to avoid.
func logResponse(res logging.Result, m *logging.Manager) map[string]any {
	out := map[string]any{
		"day":        res.Day,
		"entries":    res.Entries,
		"counts":     res.Counts,
		"truncated":  res.Truncated,
		"unparsed":   res.Unparsed,
		"incomplete": res.Incomplete,
	}
	if m != nil && m.Location() != nil {
		out["tz"] = m.Location().String()
	}
	return out
}

// listLogDays returns the daily files on disk and where they live.
func (r *Router) listLogDays(w http.ResponseWriter, _ *http.Request) {
	if r.logs == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("本实例未启用日志文件"))
		return
	}
	days, err := r.logs.Days()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := map[string]any{"days": days, "dir": r.logs.Dir()}
	if loc := r.logs.Location(); loc != nil {
		out["tz"] = loc.String()
	}
	writeJSON(w, 200, out)
}
