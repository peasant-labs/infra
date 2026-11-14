// Package route is a sub-package of the contract; its sets are members too.
package route

// Method is the closed set of HTTP methods.
type Method string

const (
	MethodGet  Method = "GET"
	MethodPost Method = "POST"
)
