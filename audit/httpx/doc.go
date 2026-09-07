// Package httpx provides a ready-to-use HTTP handler for the audit log —
// List and GetByID — plus the composable pieces it is built from: DTO
// mapping (ToResponse, ToResponseList), filter parsing (FiltersFromRequest),
// and configurable user-facing messages (Messages).
//
// It lives as a subpackage of audit, rather than inside audit itself,
// because CI enforces that audit never imports net/http — see audit's
// package doc for why that inversion matters. Splitting the transport-layer
// concern into audit/httpx keeps that invariant intact while still shipping
// a drop-in handler for applications that want one.
package httpx
