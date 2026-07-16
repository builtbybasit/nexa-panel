package jobs

import (
	"net/http"
	"strconv"
)

func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}
