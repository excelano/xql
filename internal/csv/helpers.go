package csv

// plural returns "s" when n != 1; lets us emit
// "1 row" / "3 rows" without a branch at every call site.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
