package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	buddyFieldWidth = 36
	buddyStep       = 2
	buddyMinX       = 4
	buddyTravel     = 7 // steps from one side of the meadow to the other
	buddyPeriod     = 2 * buddyTravel
	butterfly       = "ʚɞ"
)

// A low, side-on fox facing right; mirrored when running back.
var (
	foxRun = [2][]string{
		{
			"           ╱╲ ",
			"  ⁀⁀‿─────╯ •ᐳ",
			"     ╱╲  ╱╲   ",
		},
		{
			"           ╱╲ ",
			"  ⁀‿⁀─────╯ •ᐳ",
			"     ╲╱  ╲╱   ",
		},
	}
	foxSleep = []string{
		"               ᶻ",
		"           ╱╲ z ",
		"  ⁀⁀‿─────╯ ‿ᐳ  ",
		"      ╰╯  ╰╯    ",
	}
)

// stepBuddy advances the fox once per triage action; it never animates on its own.
func (m *Model) stepBuddy() {
	m.buddyFrame = (m.buddyFrame + 1) % buddyPeriod
}

// The meadow only uses spare inbox rows, with breathing room on every side.
func (m Model) inboxPadding(rows int) string {
	remaining := len(m.items)
	space := rows - min(rows, max(1, remaining))
	blank := strings.Repeat("\n", space)
	if m.err != nil || (remaining == 0 && m.loading && !m.cached) {
		return blank
	}
	lines := buddyLines(remaining, m.buddyFrame)
	if space < len(lines)+2 || m.width < buddyFieldWidth+4 {
		return blank
	}
	fox := lipgloss.NewStyle().Foreground(lipgloss.Color("173"))
	grass := lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
	wings := lipgloss.NewStyle().Foreground(lipgloss.Color("222"))
	sleep := lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	var b strings.Builder
	top := (space - len(lines)) / 2
	b.WriteString(strings.Repeat("\n", top))
	for i, line := range lines {
		b.WriteString(strings.Repeat(" ", (m.width-buddyFieldWidth)/2))
		switch {
		case i == len(lines)-1:
			b.WriteString(grass.Render(line))
		default:
			line = strings.ReplaceAll(line, butterfly, "\x00")
			for j, part := range strings.Split(line, "\x00") {
				if j > 0 {
					b.WriteString(wings.Render(butterfly))
				}
				part = strings.NewReplacer("ᶻ", sleep.Render("ᶻ"), "z", sleep.Render("z")).Replace(part)
				b.WriteString(fox.Render(part))
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("\n", space-top-len(lines)))
	return b.String()
}

func buddyLines(remaining, frame int) []string {
	lines := make([]string, 5)
	if remaining == 0 {
		for i, line := range foxSleep {
			lines[i] = fieldLine((buddyFieldWidth-lipgloss.Width(line))/2, line)
		}
	} else {
		phase := frame % buddyPeriod
		left := phase >= buddyTravel
		position := phase
		if left {
			position = buddyPeriod - phase
		}
		x := buddyMinX + position*buddyStep
		pose := foxRun[frame%2]
		width := lipgloss.Width(pose[0])
		lines[0] = fieldLine(0, "")
		for i, line := range pose {
			if left {
				line = mirrorFox(line)
			}
			lines[i+1] = fieldLine(x, line)
		}
		// The butterfly stays just out of reach, bobbing between two heights.
		lead := x + width + 1
		if left {
			lead = x - 3
		}
		row := frame % 2
		runes := []rune(lines[row])
		copy(runes[lead:], []rune(butterfly))
		lines[row] = string(runes)
	}
	lines[4] = "  ˎˏ   ˎ  ˏˎ    ˎˏ   ˎ  ˏˎ    ˎˏ    "
	return lines
}

func fieldLine(x int, text string) string {
	return strings.Repeat(" ", x) + text + strings.Repeat(" ", buddyFieldWidth-x-lipgloss.Width(text))
}

func mirrorFox(line string) string {
	runes := []rune(line)
	swap := map[rune]rune{'╱': '╲', '╲': '╱', '╭': '╮', '╮': '╭', '╰': '╯', '╯': '╰', 'ᐳ': 'ᐸ'}
	for i, j := 0, len(runes)-1; i <= j; i, j = i+1, j-1 {
		a, b := runes[i], runes[j]
		if r, ok := swap[a]; ok {
			a = r
		}
		if r, ok := swap[b]; ok {
			b = r
		}
		runes[i], runes[j] = b, a
	}
	return string(runes)
}
