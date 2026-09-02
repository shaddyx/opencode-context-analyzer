// Package tokenizer provides token estimation for text content.
//
// It uses a lightweight heuristic approximating the OpenAI tiktoken
// cl100k_base tokenizer. It is not byte-for-byte identical to tiktoken,
// but is accurate enough for context-usage reporting.
package tokenizer

import (
	"strconv"
	"unicode"
	"unicode/utf8"
)

// Estimate returns an approximate token count for the given text.
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	return countTokens(text)
}

// countTokens implements a heuristic tokenizer.
//
// Rules (approximating cl100k_base):
//   - whitespace runs collapse to a single token
//   - a run of 4+ consecutive ASCII letters is split every 4 chars
//   - a run of 4+ consecutive ASCII digits is split every 3 chars
//   - a run of 4+ consecutive ASCII punctuation is split every 4 chars
//   - each non-ASCII rune counts as one token
//   - a single ASCII char (letter/digit/punct) counts as one token
func countTokens(s string) int {
	tokens := 0
	i := 0
	n := len(s)
	for i < n {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// invalid byte
			tokens++
			i++
			continue
		}
		if unicode.IsSpace(r) {
			// collapse whitespace run into one token
			for i < n {
				r2, sz := utf8.DecodeRuneInString(s[i:])
				if !unicode.IsSpace(r2) {
					break
				}
				i += sz
			}
			tokens++
			continue
		}
		if r < 128 {
			// ASCII
			if isAsciiLetter(r) {
				run := 1
				for i+run < n {
					rr, _ := utf8.DecodeRuneInString(s[i+run:])
					if !isAsciiLetter(rr) {
						break
					}
					run++
				}
				tokens += (run + 3) / 4
				i += run
				continue
			}
			if isAsciiDigit(r) {
				run := 1
				for i+run < n {
					rr, _ := utf8.DecodeRuneInString(s[i+run:])
					if !isAsciiDigit(rr) {
						break
					}
					run++
				}
				tokens += (run + 2) / 3
				i += run
				continue
			}
			// punctuation / other ascii
			run := 1
			for i+run < n {
				rr, _ := utf8.DecodeRuneInString(s[i+run:])
				if rr >= 128 || isAsciiLetter(rr) || isAsciiDigit(rr) || unicode.IsSpace(rr) {
					break
				}
				run++
			}
			tokens += (run + 3) / 4
			i += run
			continue
		}
		// non-ASCII rune: one token
		tokens++
		i += size
	}
	return tokens
}

func isAsciiLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isAsciiDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// EstimateJSON is a convenience wrapper for estimating a JSON-encoded value.
func EstimateJSON(data []byte) int {
	return Estimate(string(data))
}

// Format returns a human-readable token count with thousands separators.
func Format(n int) string {
	if n < 0 {
		n = 0
	}
	s := strconv.Itoa(n)
	var out []byte
	cnt := 0
	for i := len(s) - 1; i >= 0; i-- {
		out = append(out, s[i])
		cnt++
		if cnt%3 == 0 && i > 0 {
			out = append(out, ',')
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
