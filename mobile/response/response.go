package response

// DefaultResponse is the {status, message, data} envelope, kept for parity
// with the web pack for any future endpoint that doesn't need to match a
// legacy flat shape.
type DefaultResponse struct {
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

// LegacyResponse mirrors the plain {"message":..., "status":...} shape the
// PHP driver API returns for simple errors (see moddriverapi201.php
// action_index, e.g. "Invalid Request "/-status 2, "invalid_request"/-1).
// Success payloads with extra dynamic fields (baseurl, androidPaths, ...)
// are built with gin.H directly in the controller instead of this struct,
// since those field sets vary per `type`.
type LegacyResponse struct {
	Message string `json:"message"`
	Status  int    `json:"status"`
}
