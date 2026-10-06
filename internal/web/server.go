// Package web is the browser UI: server-rendered pages (templ), HTMX for
// partial updates and a small script for the keyboard-driven inbox.
package web

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/GumboYaYa/budgeteer/internal/store"
)

//go:embed static
var staticFiles embed.FS

type Options struct {
	// RawDir receives the original of every imported file.
	RawDir string
	// Addr is the listen address; its host is accepted in the Host header in
	// addition to localhost.
	Addr string
}

type server struct {
	st     *store.Store
	rawDir string
}

// New returns the handler for the whole UI.
func New(st *store.Store, opts Options) http.Handler {
	s := &server{st: st, rawDir: opts.RawDir}
	mux := http.NewServeMux()

	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /inbox", s.inbox)
	mux.HandleFunc("GET /transactions", s.transactions)
	mux.HandleFunc("POST /api/categorize", s.categorize)
	mux.HandleFunc("POST /api/transfer", s.transfer)
	mux.HandleFunc("POST /api/undo", s.undo)
	mux.HandleFunc("POST /api/reserve", s.reserveMark)
	mux.HandleFunc("GET /reserve", s.reservePage)
	mux.HandleFunc("POST /reserve/account", s.reserveAccount)
	mux.HandleFunc("GET /import", s.importPage)
	mux.HandleFunc("POST /import", s.importUpload)
	mux.HandleFunc("GET /categories", s.categories)
	mux.HandleFunc("POST /categories", s.categoryCreate)
	mux.HandleFunc("POST /categories/{id}/rename", s.categoryRename)
	mux.HandleFunc("POST /categories/{id}/move", s.categoryMove)
	mux.HandleFunc("POST /categories/{id}/merge", s.categoryMerge)

	// There is no login, so the browser is the only gate. Reject requests
	// made by other websites (cross-origin) or through a foreign host name
	// pointed at this machine (DNS rebinding).
	return localOnly(opts.Addr, http.NewCrossOriginProtection().Handler(mux))
}

func localOnly(addr string, next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		allowed[strings.ToLower(host)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		if !allowed[host] {
			http.Error(w, "forbidden: unexpected host "+strconv.Quote(r.Host), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		log.Printf("render %s: %v", r.URL.Path, err)
	}
}

// fail reports an error. Bad input is the user's to fix; anything else is
// logged and hidden.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var bad badRequest
	switch {
	case errors.As(err, &bad):
		http.Error(w, bad.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

type badRequest struct{ msg string }

func (b badRequest) Error() string { return b.msg }

func badRequestf(format string, args ...any) error {
	return badRequest{fmt.Sprintf(format, args...)}
}

// ids parses the repeated "ids" form field.
func ids(r *http.Request) ([]int64, error) {
	if err := r.ParseForm(); err != nil {
		return nil, badRequestf("invalid form")
	}
	var out []int64
	for _, v := range r.PostForm["ids"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, badRequestf("invalid transaction id %q", v)
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, badRequestf("no transactions given")
	}
	return out, nil
}

func formID(r *http.Request, field string) (int64, error) {
	id, err := strconv.ParseInt(r.FormValue(field), 10, 64)
	if err != nil {
		return 0, badRequestf("invalid %s %q", field, r.FormValue(field))
	}
	return id, nil
}
