// Package chi mirrors the routing method set of github.com/go-chi/chi/v5 for
// offline fixtures. Bodies are empty: only the types matter to the pass.
package chi

import "net/http"

// Router is chi's routing interface. Route callbacks receive it and With
// returns it, so most mounts in a real tree have this receiver type.
type Router interface {
	http.Handler
	Use(middlewares ...func(http.Handler) http.Handler)
	With(middlewares ...func(http.Handler) http.Handler) Router
	Group(fn func(r Router)) Router
	Route(pattern string, fn func(r Router)) Router
	Mount(pattern string, h http.Handler)
	Handle(pattern string, h http.Handler)
	HandleFunc(pattern string, h http.HandlerFunc)
	Method(method, pattern string, h http.Handler)
	MethodFunc(method, pattern string, h http.HandlerFunc)
	Connect(pattern string, h http.HandlerFunc)
	Delete(pattern string, h http.HandlerFunc)
	Get(pattern string, h http.HandlerFunc)
	Head(pattern string, h http.HandlerFunc)
	Options(pattern string, h http.HandlerFunc)
	Patch(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Put(pattern string, h http.HandlerFunc)
	Query(pattern string, h http.HandlerFunc)
	Trace(pattern string, h http.HandlerFunc)
	NotFound(h http.HandlerFunc)
	MethodNotAllowed(h http.HandlerFunc)
}

// Mux is chi's concrete router, returned by NewRouter.
type Mux struct{}

// NewRouter returns a new router.
func NewRouter() *Mux { return &Mux{} }

// URLParam reads a route parameter; its key argument is not a pattern.
func URLParam(r *http.Request, key string) string { return "" }

func (mx *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request)           {}
func (mx *Mux) Use(middlewares ...func(http.Handler) http.Handler)         {}
func (mx *Mux) With(middlewares ...func(http.Handler) http.Handler) Router { return mx }
func (mx *Mux) Group(fn func(r Router)) Router                             { return mx }
func (mx *Mux) Route(pattern string, fn func(r Router)) Router             { return mx }
func (mx *Mux) Mount(pattern string, h http.Handler)                       {}
func (mx *Mux) Handle(pattern string, h http.Handler)                      {}
func (mx *Mux) HandleFunc(pattern string, h http.HandlerFunc)              {}
func (mx *Mux) Method(method, pattern string, h http.Handler)              {}
func (mx *Mux) MethodFunc(method, pattern string, h http.HandlerFunc)      {}
func (mx *Mux) Connect(pattern string, h http.HandlerFunc)                 {}
func (mx *Mux) Delete(pattern string, h http.HandlerFunc)                  {}
func (mx *Mux) Get(pattern string, h http.HandlerFunc)                     {}
func (mx *Mux) Head(pattern string, h http.HandlerFunc)                    {}
func (mx *Mux) Options(pattern string, h http.HandlerFunc)                 {}
func (mx *Mux) Patch(pattern string, h http.HandlerFunc)                   {}
func (mx *Mux) Post(pattern string, h http.HandlerFunc)                    {}
func (mx *Mux) Put(pattern string, h http.HandlerFunc)                     {}
func (mx *Mux) Query(pattern string, h http.HandlerFunc)                   {}
func (mx *Mux) Trace(pattern string, h http.HandlerFunc)                   {}
func (mx *Mux) NotFound(h http.HandlerFunc)                                {}
func (mx *Mux) MethodNotAllowed(h http.HandlerFunc)                        {}
