package cmd

import (
	"fmt"
	"os"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

type pickRow struct {
	name string
	note string
}

func pick(header string, rows []pickRow, selected int) (int, bool) {
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	width := 0
	for _, row := range rows {
		width = max(width, runewidth.StringWidth(row.name))
	}
	width++

	render := func(i int, current bool) {
		marker := " "
		name := runewidth.FillRight(rows[i].name, width)
		if current {
			marker = colorCyan + ">" + colorReset
			name = colorCyan + name + colorReset
		}
		fmt.Printf("\r\x1b[K%s %s %s%s%s\r\n", marker, name, colorGray, rows[i].note, colorReset)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Println(colorRed+"error:"+colorReset, "an interactive terminal is required:", err)
		return 0, false
	}
	defer term.Restore(fd, oldState)

	fmt.Print(header + " " + colorGray + "(↑/↓ move, enter confirms, esc cancels)" + colorReset + "\r\n")
	for i := range rows {
		render(i, i == selected)
	}

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return 0, false
		}
		pending := buf[:n]
		for len(pending) > 0 {
			prev := selected
			switch {
			case pending[0] == '\x1b' && len(pending) >= 3 && (pending[1] == '[' || pending[1] == 'O'):
				switch pending[2] {
				case 'A': // up
					if selected > 0 {
						selected--
					}
				case 'B': // down
					if selected < len(rows)-1 {
						selected++
					}
				}
				pending = pending[3:]
			case pending[0] == '\r' || pending[0] == '\n':
				return selected, true
			case pending[0] == '\x1b' || pending[0] == 'q' || pending[0] == '\x03': // esc, q, ctrl-c
				return 0, false
			default:
				pending = pending[1:]
			}
			if selected != prev {
				fmt.Printf("\x1b[%dA", len(rows))
				for i := range rows {
					render(i, i == selected)
				}
			}
		}
	}
}

func pickMulti(header string, rows []pickRow, checked []bool) ([]bool, bool) {
	state := make([]bool, len(rows))
	copy(state, checked)
	selected := 0
	width := 0
	for _, row := range rows {
		width = max(width, runewidth.StringWidth(row.name))
	}
	width++

	render := func(i int, current bool) {
		marker := " "
		box := "[ ]"
		if state[i] {
			box = "[" + colorGreen + "x" + colorReset + "]"
		}
		name := runewidth.FillRight(rows[i].name, width)
		if current {
			marker = colorCyan + ">" + colorReset
			name = colorCyan + name + colorReset
		}
		fmt.Printf("\r\x1b[K%s %s %s %s%s%s\r\n", marker, box, name, colorGray, rows[i].note, colorReset)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Println(colorRed+"error:"+colorReset, "an interactive terminal is required:", err)
		return nil, false
	}
	defer term.Restore(fd, oldState)

	fmt.Print(header + " " + colorGray + "(↑/↓ move, space toggles, enter confirms, esc cancels)" + colorReset + "\r\n")
	for i := range rows {
		render(i, i == selected)
	}
	repaint := func() {
		fmt.Printf("\x1b[%dA", len(rows))
		for i := range rows {
			render(i, i == selected)
		}
	}

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return nil, false
		}
		pending := buf[:n]
		for len(pending) > 0 {
			switch {
			case pending[0] == '\x1b' && len(pending) >= 3 && (pending[1] == '[' || pending[1] == 'O'):
				switch pending[2] {
				case 'A':
					if selected > 0 {
						selected--
					}
				case 'B':
					if selected < len(rows)-1 {
						selected++
					}
				}
				pending = pending[3:]
				repaint()
			case pending[0] == ' ':
				state[selected] = !state[selected]
				pending = pending[1:]
				repaint()
			case pending[0] == '\r' || pending[0] == '\n':
				return state, true
			case pending[0] == '\x1b' || pending[0] == 'q' || pending[0] == '\x03':
				return nil, false
			default:
				pending = pending[1:]
			}
		}
	}
}
