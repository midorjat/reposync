// Package agent exposes a Node over HTTP so the other machine can sync with
// it, and provides the matching client.
//
// The API is plain HTTP with a shared bearer token: fine on a home network,
// not on the internet. Put it behind an encrypted link (e.g. Tailscale)
// before using it anywhere else.
package agent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"reposync/internal/node"
	"reposync/internal/repo"
)

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// Error codes let the client turn failures back into the sentinel errors.
var codes = map[string]error{
	"changed":      repo.ErrChanged,
	"busy":         repo.ErrBusy,
	"no_commits":   repo.ErrNoCommits,
	"unknown_repo": node.ErrUnknownRepo,
	"missing":      node.ErrMissing,
}

type applyReq struct {
	From   string `json:"from"`
	Expect string `json:"expect"`
}

type keepReq struct {
	From string `json:"from"`
}

type keepResp struct {
	Branch string `json:"branch"`
}

func NewServer(n *node.Node, token string, logger *log.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		h, _ := n.Hello(r.Context())
		writeJSON(w, h)
	})
	mux.HandleFunc("GET /v1/repos/{name}/state", func(w http.ResponseWriter, r *http.Request) {
		s, err := n.State(r.Context(), r.PathValue("name"))
		reply(w, s, err)
	})
	mux.HandleFunc("GET /v1/repos/{name}/bundle", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-bundle")
		if err := n.Export(r.Context(), r.PathValue("name"), w); err != nil {
			writeErr(w, err)
		}
	})
	mux.HandleFunc("POST /v1/repos/{name}/bundle", func(w http.ResponseWriter, r *http.Request) {
		s, err := n.Import(r.Context(), r.PathValue("name"), r.URL.Query().Get("from"), r.Body)
		reply(w, s, err)
	})
	mux.HandleFunc("POST /v1/repos/{name}/apply", func(w http.ResponseWriter, r *http.Request) {
		var req applyReq
		if !readJSON(w, r, &req) {
			return
		}
		s, err := n.Apply(r.Context(), r.PathValue("name"), req.From, req.Expect)
		reply(w, s, err)
	})
	mux.HandleFunc("POST /v1/repos/{name}/keep", func(w http.ResponseWriter, r *http.Request) {
		var req keepReq
		if !readJSON(w, r, &req) {
			return
		}
		b, err := n.KeepWork(r.Context(), r.PathValue("name"), req.From)
		reply(w, keepResp{Branch: b}, err)
	})
	mux.HandleFunc("GET /v1/repos/{name}/base/{peer}", func(w http.ResponseWriter, r *http.Request) {
		b, err := n.Base(r.Context(), r.PathValue("name"), r.PathValue("peer"))
		reply(w, b, err)
	})
	mux.HandleFunc("PUT /v1/repos/{name}/base/{peer}", func(w http.ResponseWriter, r *http.Request) {
		var b repo.Base
		if !readJSON(w, r, &b) {
			return
		}
		err := n.SetBase(r.Context(), r.PathValue("name"), r.PathValue("peer"), b)
		reply(w, struct{}{}, err)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		want := []byte("Bearer " + token)
		if subtle.ConstantTimeCompare(got, want) != 1 {
			logger.Printf("rejected %s %s from %s: bad token", r.Method, r.URL.Path, r.RemoteAddr)
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, errorBody{Error: "bad token (both machines need the same token in their config)"})
			return
		}
		logger.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		mux.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
}

func writeErr(w http.ResponseWriter, err error) {
	body := errorBody{Error: err.Error()}
	status := http.StatusInternalServerError
	for code, sentinel := range codes {
		if errors.Is(err, sentinel) {
			body.Code = code
			status = http.StatusConflict
			if sentinel == node.ErrUnknownRepo {
				status = http.StatusNotFound
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, errorBody{Error: "bad request: " + err.Error()})
		return false
	}
	return true
}
