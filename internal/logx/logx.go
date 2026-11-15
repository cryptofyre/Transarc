package logx

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cryptofyre/transarc/internal/human"
	"golang.org/x/term"
)

// Logger is safe for concurrent use.
type Logger struct {
	mu        sync.Mutex
	out       io.Writer
	color     bool
	now       func() time.Time
	lastProg  map[string]time.Time
	progEvery time.Duration
}

// New writes to out. Color follows the terminal unless TRANSARC_COLOR or NO_COLOR is set.
func New(out io.Writer) *Logger {
	every := 10 * time.Second
	if os.Getenv("TRANSARC_PROGRESS_SEC") == "1" {
		every = time.Second
	}
	return &Logger{
		out:       out,
		color:     wantColor(out),
		now:       time.Now,
		lastProg:  map[string]time.Time{},
		progEvery: every,
	}
}

func wantColor(w io.Writer) bool {
	switch strings.ToLower(os.Getenv("TRANSARC_COLOR")) {
	case "1", "true", "always":
		return true
	case "0", "false", "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func (l *Logger) stamp() string {
	return l.now().Format("15:04:05")
}

func (l *Logger) paint(code, s string) string {
	if !l.color || code == "" {
		return s
	}
	return code + s + "\033[0m"
}

// Info writes one informational line.
func (l *Logger) Info(format string, args ...any) {
	l.line("INFO", "\033[36m", fmt.Sprintf(format, args...))
}

// Warn writes one warning line.
func (l *Logger) Warn(format string, args ...any) {
	l.line("WARN", "\033[33m", fmt.Sprintf(format, args...))
}

// Error writes one error line.
func (l *Logger) Error(format string, args ...any) {
	l.line("FAIL", "\033[31m", fmt.Sprintf(format, args...))
}

func (l *Logger) line(tag, code, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, "%s  %s  %s\n", l.stamp(), l.paint(code, fmt.Sprintf("%-4s", tag)), msg)
}

// BoxWidth is the panel width in runes, including both borders.
const BoxWidth = 96

// Rule is a Box row that draws a horizontal rule between sections.
const Rule = "\x1e"

// Box writes a titled panel. Workers can call it directly. The lock holds for the whole panel.
func (l *Logger) Box(kind, title string, rows []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeBox(kind, title, rows)
}

// Schedule writes the wordmark and then the work-plan panel as one piece.
func (l *Logger) Schedule(kind, title string, rows []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	code := kindColor(kind)
	for _, line := range Masthead() {
		fmt.Fprintln(l.out, l.paint(code, line))
	}
	l.writeBox(kind, title, rows)
}

func (l *Logger) writeBox(kind, title string, rows []string) {
	width := BoxWidth
	code := kindColor(kind)
	label := title
	if kind != "" {
		label = kind + "  " + title
	}
	label = human.Truncate(label, width-6)
	top := "╭─ " + label + " " + strings.Repeat("─", max(0, width-5-runeLen(label))) + "╮"
	fmt.Fprintln(l.out, l.paint(code, top))
	for _, row := range rows {
		if row == Rule {
			fmt.Fprintln(l.out, l.paint(code, "├"+strings.Repeat("─", width-2)+"┤"))
			continue
		}
		row = human.Truncate(row, width-4)
		pad := width - 4 - runeLen(row)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(l.out, "%s\n", l.paint(code, "│ "+row+strings.Repeat(" ", pad)+" │"))
	}
	fmt.Fprintln(l.out, l.paint(code, "╰"+strings.Repeat("─", width-2)+"╯"))
}

func kindColor(kind string) string {
	switch strings.ToUpper(kind) {
	case "KEPT ENCODE", "PASS", "GPU OK", "START":
		return "\033[32m"
	case "IDLE":
		return "\033[36m"
	case "KEPT ORIGINAL", "SKIP", "SAMPLE":
		return "\033[33m"
	case "FAILED":
		return "\033[31m"
	default:
		return "\033[36m"
	}
}

// Progress writes an encode heartbeat. Calls closer together than the interval are dropped.
func (l *Logger) Progress(gpu, name string, pct float64, fps, speed float64, outBytes int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.lastProg[gpu]; ok && now.Sub(last) < l.progEvery {
		return
	}
	l.lastProg[gpu] = now
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	bar := bar(pct, 18)
	fmt.Fprintf(l.out, "%s  %s  %-6s  %s %5.1f%%  %5.0f fps  %4.1fx  %8s  %s\n",
		l.stamp(),
		l.paint("\033[35m", "PROG"),
		human.Truncate(gpu, 6),
		bar,
		pct,
		fps,
		speed,
		human.Bytes(outBytes),
		human.Truncate(name, 42),
	)
}

func bar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func runeLen(s string) int {
	return len([]rune(s))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
