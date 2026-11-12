// Package catalog builds the set of closed-set member values a repository
// declares, and generates the two closed-set rules from it.
//
// A member comes from one of six recognised sources (SourceKind): a closed()
// call, a frozen record table, a TS string-literal union, a JSDoc
// string-literal union, a Go typed string constant, or the pinned schema
// module. A shape that is not listed is not a source. The catalog is rebuilt
// on every run and never written to the repository; each member keeps where
// it is declared, for the message and for the definition-site exemption.
package catalog
