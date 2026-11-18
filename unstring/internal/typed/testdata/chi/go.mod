// A minimal offline stand-in for github.com/go-chi/chi/v5, used by the mount
// fixtures through a replace directive. It keeps chi's method set and the
// split between the Router interface and the *Mux type; it does no routing.
module github.com/go-chi/chi/v5

go 1.22
