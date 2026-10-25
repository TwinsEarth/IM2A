package api

import "strconv"

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}
