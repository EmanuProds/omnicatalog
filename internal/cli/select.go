package cli

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"unsafe"

)

// SelectItem represents an entry in a multiselect.
type SelectItem struct {
	ID       string
	Label    string
	Selected bool
}

func termIsTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return err == 0
}

func enableRawMode(fd int) (syscall.Termios, error) {
	var old syscall.Termios
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&old)))
	if err != 0 {
		return old, err
	}
	newt := old
	newt.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	newt.Oflag &^= syscall.OPOST
	newt.Cflag |= syscall.CS8
	newt.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	newt.Cc[syscall.VMIN] = 1
	newt.Cc[syscall.VTIME] = 0
	_, _, err = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&newt)))
	if err != 0 {
		return old, err
	}
	return old, nil
}

func restoreMode(fd int, t syscall.Termios) {
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&t)))
}

// Multiselect displays items and lets the user toggle with space and confirm with enter.
func Multiselect(title string, items []SelectItem) ([]SelectItem, error) {
	if !termIsTerminal(os.Stdin) || !termIsTerminal(os.Stdout) {
		return numberedMultiselect(title, items)
	}
	return terminalMultiselect(title, items)
}

func numberedMultiselect(title string, items []SelectItem) ([]SelectItem, error) {
	result := make([]SelectItem, len(items))
	copy(result, items)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println(title)
		for i, it := range result {
			mark := "[ ]"
			if it.Selected {
				mark = "[x]"
			}
			fmt.Printf("  %d) %s %s\n", i+1, mark, it.Label)
		}
		fmt.Print("Toggle indices (e.g. 1,3,5), 'a'=all, 'done'=confirm: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "done" || line == "" {
			break
		}
		if line == "a" || line == "all" {
			all := true
			for _, it := range result {
				if !it.Selected {
					all = false
					break
				}
			}
			for i := range result {
				result[i].Selected = !all
			}
			continue
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			var idx int
			fmt.Sscan(part, &idx)
			if idx >= 1 && idx <= len(result) {
				result[idx-1].Selected = !result[idx-1].Selected
			}
		}
	}
	return result, nil
}

func terminalMultiselect(title string, items []SelectItem) ([]SelectItem, error) {
	result := make([]SelectItem, len(items))
	copy(result, items)

	// prepend a synthetic "Select all" item
	allItem := SelectItem{ID: "__ALL__", Label: "Select ALL", Selected: allSelected(result)}
	result = append([]SelectItem{allItem}, result...)

	// fix __ALL__ selected in list dynamically during render

	fmt.Print("\033[2J\033[H")
	fd := int(os.Stdin.Fd())
	old, err := enableRawMode(fd)
	if err != nil {
		return numberedMultiselect(title, items)
	}
	defer restoreMode(fd, old)

	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h\n")

	cursor := 0
	viewport := 30
	prevLines := 0

	render := func() {
		// move up previous
		if prevLines > 0 {
			fmt.Printf("\033[%dA", prevLines)
		}
		// clear screen lines? use \r and \033[K
		lines := 0
		fmt.Printf("\r\033[K%s\n", title)
		lines++
		fmt.Printf("\033[K  ↑/↓ move, Space toggle, 'a' toggle all, Enter confirm, 'q' cancel\n")
		lines++

		// current all-selected sync
		allNow := true
		for i := 1; i < len(result); i++ {
			if !result[i].Selected {
				allNow = false
				break
			}
		}
		result[0].Selected = allNow

		start := 0
		if cursor >= viewport {
			start = cursor - viewport + 1
		}
		end := start + viewport
		if end > len(result) {
			end = len(result)
		}
		for i := start; i < end; i++ {
			it := result[i]
			mark := "[ ]"
			if it.Selected {
				mark = "[x]"
			}
			line := fmt.Sprintf("  %s %s", mark, it.Label)
			if i == cursor {
				fmt.Printf("\r\033[7m%s\033[0m\033[K\n", line) // reverse
			} else {
				fmt.Printf("\r\033[K%s\n", line)
			}
			lines++
		}
		fmt.Printf("\r\033[K(%d/%d selected)\n", countSelected(result[1:]), len(result)-1)
		lines++
		prevLines = lines
	}

	render()

	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}
		b := buf[0]
		if b == 'q' || b == 'Q' {
			// cancel -> keep original? return none? We'll return all false? Instead restore and return error.
			return nil, fmt.Errorf("cancelled")
		}
		if b == '\r' || b == '\n' {
			break
		}
		if b == ' ' {
			if cursor < len(result) {
				if result[cursor].ID == "__ALL__" {
					newState := !result[cursor].Selected
					for i := 1; i < len(result); i++ {
						result[i].Selected = newState
					}
					result[0].Selected = newState
				} else {
					result[cursor].Selected = !result[cursor].Selected
				}
			}
			render()
			continue
		}
		if b == 'a' || b == 'A' {
			all := true
			for i := 1; i < len(result); i++ {
				if !result[i].Selected {
					all = false
					break
				}
			}
			for i := 1; i < len(result); i++ {
				result[i].Selected = !all
			}
			result[0].Selected = !all
			render()
			continue
		}
		if b == 27 { // escape sequences
			// read next two
			seq := make([]byte, 2)
			os.Stdin.Read(seq)
			if seq[0] == '[' {
				if seq[1] == 'A' { // up
					if cursor > 0 {
						cursor--
					}
				} else if seq[1] == 'B' { // down
					if cursor < len(result)-1 {
						cursor++
					}
				}
			}
			render()
			continue
		}
		// ignore
	}

	// strip all item
	out := append([]SelectItem(nil), result[1:]...)
	// ensure selected state of items reflects actual toggles (all item only visual)
	return out, nil
}

func allSelected(items []SelectItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if !it.Selected {
			return false
		}
	}
	return true
}

func countSelected(items []SelectItem) int {
	n := 0
	for _, it := range items {
		if it.Selected {
			n++
		}
	}
	return n
}

func intRange(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
