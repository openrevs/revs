package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cursor is a selection in a scrolling list of n items.
type cursor struct {
	i   int // selected item
	top int // first visible item
}

// move handles a navigation key, reporting whether it was one.
func (c *cursor) move(msg tea.Msg, n, page int) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "up", "k":
		c.i--
	case "down", "j":
		c.i++
	case "pgup", "ctrl+u":
		c.i -= max(page, 1)
	case "pgdown", "ctrl+d":
		c.i += max(page, 1)
	case "home", "g":
		c.i = 0
	case "end", "G":
		c.i = n - 1
	default:
		return false
	}
	c.clamp(n)
	return true
}

func (c *cursor) clamp(n int) {
	c.i = min(max(c.i, 0), max(n-1, 0))
}

// render draws the visible part of a list of n items in height lines,
// each item one line, scrolling to keep the selection in view. line
// renders item i; selected marks the cursor's item.
func (c *cursor) render(n, height int, line func(i int, selected bool) string) string {
	if n == 0 {
		return ""
	}
	c.clamp(n)
	height = max(height, 1)
	more := n > height
	if more {
		height = max(height-1, 1) // room for the "more" line
	}
	if c.i < c.top {
		c.top = c.i
	}
	if c.i >= c.top+height {
		c.top = c.i - height + 1
	}
	c.top = min(c.top, max(n-height, 0))

	var b strings.Builder
	for i := c.top; i < min(n, c.top+height); i++ {
		if i > c.top {
			b.WriteByte('\n')
		}
		sel := i == c.i
		mark := "  "
		if sel {
			mark = styleSelected.Render("▸ ")
		}
		b.WriteString(mark + line(i, sel))
	}
	if more {
		b.WriteString("\n" + styleDim.Render(fmt.Sprintf("  %d–%d of %d", c.top+1, min(n, c.top+height), n)))
	}
	return b.String()
}
