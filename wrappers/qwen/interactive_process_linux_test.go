// SPDX-License-Identifier: MIT
package qwen

import (
	"errors"
	"strings"
	"testing"
)

// The sweep treats errNativeNotLive as proof that a recorded process ended, so
// only a zombie or dead state may produce it; unreadable data must not.
func TestParseNativeStatSeparatesEndedFromMalformed(t *testing.T) {
	fields := func(state string) []byte {
		rest := []string{state}
		for i := 1; i <= 20; i++ {
			rest = append(rest, "7")
		}
		rest[19] = "123456"
		return []byte("42 (qwen (tui)) " + strings.Join(rest, " "))
	}
	p, err := parseNativeStat(42, fields("S"), "boot")
	if err != nil || p.parent != 7 || p.start != "boot:123456" {
		t.Fatalf("live stat = %+v, %v", p, err)
	}
	for _, state := range []string{"Z", "X"} {
		if _, err := parseNativeStat(42, fields(state), "boot"); !errors.Is(err, errNativeNotLive) {
			t.Fatalf("state %s = %v, want not live", state, err)
		}
	}
	for _, malformed := range [][]byte{[]byte("42 no parenthesis"), []byte("42 (qwen) S 1 2"), []byte("42 (qwen)"), []byte("42 (qwen) Z"), []byte("42 (qwen) X 1 2"), []byte("42 (qwen) S x 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20")} {
		if _, err := parseNativeStat(42, malformed, "boot"); err == nil || errors.Is(err, errNativeNotLive) {
			t.Fatalf("malformed %q = %v, want an ambiguous error", malformed, err)
		}
	}
}
