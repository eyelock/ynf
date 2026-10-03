// Package status describes HTTP status codes.
//
// The descriptions follow the IANA HTTP Status Code Registry:
// https://www.iana.org/assignments/http-status-codes/http-status-codes.xhtml
package status

// Describe returns the registered reason phrase for code, or "Unknown".
func Describe(code int) string {
	if d, ok := descriptions[code]; ok {
		return d
	}
	return "Unknown"
}

var descriptions = map[int]string{
	200: "OK",
	201: "Created",
	204: "No Content",
	301: "Moved Permanently",
	400: "Bad Request",
	401: "Unauthorized",
	403: "Forbidden",
	404: "Not Found",
	413: "Request Entity Too Large",
	422: "Unprocessable Entity",
	500: "Internal Server Error",
}
