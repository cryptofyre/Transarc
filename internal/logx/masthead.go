package logx

import "strings"

// Masthead is the startup wordmark. Every line is BoxWidth runes so it sits
// on the same grid as the schedule panel.
func Masthead() []string {
	word := wordmark("TRANSARC")
	lines := append(arch(runeLen(word[0])), word...)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = centerLine(line, BoxWidth)
	}
	return out
}

func centerLine(s string, width int) string {
	n := runeLen(s)
	if n >= width {
		return s
	}
	left := (width - n) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", width-n-left)
}

func wordmark(text string) []string {
	const rows = 6
	lines := make([]string, rows)
	for i, r := range text {
		g := glyphs[r]
		if i > 0 {
			for row := 0; row < rows; row++ {
				lines[row] += " "
			}
		}
		for row := 0; row < rows; row++ {
			lines[row] += g[row]
		}
	}
	return lines
}

// arch is a flat three-row span the same width as the wordmark.
// Each row steps inward so the curve actually meets the row above it.
func arch(width int) []string {
	topPad := width * 16 / 63
	midPad := width * 6 / 63
	if topPad < midPad+2 {
		topPad = midPad + 2
	}
	top := strings.Repeat(" ", topPad) + strings.Repeat("▀", width-2*topPad) + strings.Repeat(" ", topPad)

	ramp := topPad - midPad + 1
	left := strings.Repeat(" ", midPad) + "▄" + strings.Repeat("▀", ramp-1)
	right := strings.Repeat("▀", ramp-1) + "▄" + strings.Repeat(" ", midPad)
	mid := left + strings.Repeat(" ", width-runeLen(left)-runeLen(right)) + right

	foot := midPad + 1
	botLeft := "▄" + strings.Repeat("▀", foot-1)
	botRight := strings.Repeat("▀", foot-1) + "▄"
	bot := botLeft + strings.Repeat(" ", width-runeLen(botLeft)-runeLen(botRight)) + botRight
	return []string{top, mid, bot}
}

// glyphs are 7 runes wide and 6 rows tall.
var glyphs = map[rune][6]string{
	'T': {"███████", "   █   ", "   █   ", "   █   ", "   █   ", "   █   "},
	'R': {"██████ ", "█     █", "██████ ", "█   █  ", "█    █ ", "█     █"},
	'A': {" █████ ", "█     █", "█     █", "███████", "█     █", "█     █"},
	'N': {"█     █", "██    █", "█ █   █", "█  █  █", "█   █ █", "█    ██"},
	'S': {" █████ ", "█      ", "██████ ", "      █", "█     █", " █████ "},
	'C': {" █████ ", "█     █", "█      ", "█      ", "█     █", " █████ "},
}
