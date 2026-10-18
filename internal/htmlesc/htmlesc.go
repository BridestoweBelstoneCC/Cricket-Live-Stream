// Package htmlesc is Python's html.escape(s, quote=True), byte for byte. Go's
// html.EscapeString writes ' and " as &#39; / &#34;; the pages and tests here expect
// Python's &#x27; / &quot;.
package htmlesc

import "strings"

var replacer = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

func E(s string) string { return replacer.Replace(s) }
