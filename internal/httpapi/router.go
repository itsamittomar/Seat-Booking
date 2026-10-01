package httpapi

import "net/http"

type Deps struct {
	DB Pinger
}

type handler struct {
	db Pinger
}

func NewHandler(d Deps) http.Handler {
	h := &handler{db: d.DB}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	return mux
}
