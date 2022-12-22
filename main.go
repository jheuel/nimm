package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	bm "github.com/charmbracelet/wish/bubbletea"
	lm "github.com/charmbracelet/wish/logging"
	"github.com/muesli/reflow/indent"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/termenv"
)

const (
	host = "boosted.science"
	port = 2222
)

var normalStyle = lipgloss.NewStyle()
var helpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))

type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Left   key.Binding
	Right  key.Binding
	Help   key.Binding
	Quit   key.Binding
	Submit key.Binding
	Select key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "move down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "move left"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "move right"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	Submit: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("ENTER", "submit"),
	),
	Select: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("SPACE", "select"),
	),
}

// ShortHelp returns keybindings to be shown in the mini help view. It's part
// of the key.Map interface.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit}
}

// FullHelp returns keybindings for the expanded help view. It's part of the
// key.Map interface.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Left, k.Right},      // first column
		{k.Select, k.Submit, k.Help, k.Quit}, // second column
	}
}

func main() {
	s, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", host, port)),
		wish.WithHostKeyPath(".ssh/term_info_ed25519"),
		wish.WithMiddleware(
			myCustomBubbleteaMiddleware(),
			lm.Middleware(),
		),
	)
	if err != nil {
		log.Fatalln(err)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Printf("Starting SSH server on %s:%d", host, port)
	go func() {
		if err = s.ListenAndServe(); err != nil {
			log.Fatalln(err)
		}
	}()

	<-done
	log.Println("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil {
		log.Fatalln(err)
	}
}

func myCustomBubbleteaMiddleware() wish.Middleware {
	newProg := func(m tea.Model, opts ...tea.ProgramOption) *tea.Program {
		p := tea.NewProgram(m, opts...)
		go func() {
			for {
				<-time.After(1 * time.Second)
				p.Send(timeMsg(time.Now()))
			}
		}()
		return p
	}
	teaHandler := func(s ssh.Session) *tea.Program {
		pty, _, active := s.Pty()
		if !active {
			wish.Fatalln(s, "no active terminal, skipping")
			return nil
		}
		m := model{
			term:   pty.Term,
			width:  pty.Window.Width,
			height: pty.Window.Height,
			time:   time.Now(),
			help:   help.New(),
			keys:   keys,
			field: [][]bool{
				{false, false, false, true, false, false, false},
				{false, false, true, true, true, false, false},
				{false, true, true, true, true, true, false},
				{true, true, true, true, true, true, true},
			},
			rows:   4,
			cols:   7,
			player: 1,
		}
		return newProg(m, tea.WithInput(s), tea.WithOutput(s), tea.WithAltScreen())
	}
	return bm.MiddlewareWithProgramHandler(teaHandler, termenv.ANSI256)
}

type model struct {
	term           string
	width          int
	height         int
	time           time.Time
	help           help.Model
	keys           keyMap
	field          [][]bool
	row            int
	col            int
	rows           int
	cols           int
	marked_row     int
	marked_columns []int
	player         int
}

type timeMsg time.Time

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case timeMsg:
		m.time = time.Time(msg)
	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.width = msg.Width
		m.help.Width = msg.Width
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Submit):
			// see if the move is valid
			if m.marked_columns == nil {
				return m, nil
			}
			n_available := 0
			for row, columns := range m.field {
				for col, avail := range columns {
					if !avail {
						continue
					}
					if row == m.marked_row && col >= m.marked_columns[0] && col <= m.marked_columns[1] {
						continue
					}
					n_available++
				}
			}
			if n_available == 0 {
				return m, nil
			}

			// disable marked columns
			for col := m.marked_columns[0]; col <= m.marked_columns[1]; col++ {
				m.field[m.marked_row][col] = false
			}

			// reset selection and switch players
			m.row = 0
			m.col = 0
			m.marked_columns = nil
			m.marked_row = m.rows
			m.player %= 2
			m.player++
			return m, nil
		case key.Matches(msg, m.keys.Select):
			// do nothing if current column is already disabled
			if !m.field[m.row][m.col] {
				return m, nil
			}

			// start new selection if row changed
			if m.marked_row != m.row {
				m.marked_columns = nil
			}
			m.marked_row = m.row

			// cancel selection when on already marked column
			if len(m.marked_columns) > 0 {
				if m.col >= m.marked_columns[0] && m.col <= m.marked_columns[1] {
					m.marked_columns = nil
					return m, nil
				}
			}

			m.marked_columns = append(m.marked_columns, m.col)
			// sort columns ascending
			sort.Slice(m.marked_columns, func(i, j int) bool { return m.marked_columns[i] < m.marked_columns[j] })
			// delete everything but first and last element
			m.marked_columns = append(m.marked_columns[0:1], m.marked_columns[len(m.marked_columns)-1:]...)
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.row++
			if m.row > m.rows-2 {
				m.row = m.rows - 1
			}
		case key.Matches(msg, m.keys.Up):
			m.row--
			if m.row < 0 {
				m.row = 0
			}
		case key.Matches(msg, m.keys.Right):
			m.col++
			if m.col > m.cols-2 {
				m.col = m.cols - 1
			}
		case key.Matches(msg, m.keys.Left):
			m.col--
			if m.col < 0 {
				m.col = 0
			}
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
		}
	}
	return m, nil
}

func contains(s []int, e int) bool {
	if len(s) == 1 {
		return e == s[0]
	}
	if len(s) == 2 {
		return e >= s[0] && e <= s[1]
	}
	return false
}

func available(field [][]bool) int {
	sum := 0
	for _, row := range field {
		for _, col := range row {
			if col {
				sum++
			}
		}
	}
	return sum
}

func (m model) View() string {
	s := ""
	s += indent.String(normalStyle.Bold(true).Render("== Nimm =="), uint(m.width-11)/2)
	s += "\n\n"
	s += indent.String(
		wordwrap.String(
			helpStyle.Render("Nim is a mathematical game of strategy in which"+
				" two players take turns removing (or \"nimming\") objects from"+
				" distinct heaps or piles. On each turn, a player must remove at"+
				"least one object, and may remove any number of objects provided"+
				" they all come from the same heap or pile. The goal of the game"+
				" is to avoid taking the last object."), m.width-12), 4)
	s += "\n\n"
	if available(m.field) == 1 {
		s += indent.String(fmt.Sprintf("Player %d lost  ", m.player), uint(m.width-15)/2) + "\n\n"
	} else {
		s += indent.String(fmt.Sprintf("Player %d's turn", m.player), uint(m.width-15)/2) + "\n\n"
	}
	game := ""
	for row := 0; row < m.rows; row++ {
		for column := 0; column < m.cols; column++ {
			style := normalStyle
			if row == m.row && column == m.col {
				style = style.Bold(true).Background(lipgloss.Color("#7D56F4"))
			}
			if row == m.marked_row && contains(m.marked_columns, column) {
				style = style.Foreground(lipgloss.Color("5"))
			}
			mark := " "
			if m.field[row][column] {
				mark = "X"
			}
			game += "  " + style.Render(mark)
		}
		game += "\n"
	}
	s += indent.String(game, uint(m.width-24)/2)
	helpIndent := uint(m.width-24) / 2
	if m.help.ShowAll {
		helpIndent = uint(m.width-34) / 2
	}
	helpView := indent.String(m.help.View(m.keys), helpIndent)
	height := m.height - 4 - strings.Count(s, "\n") - strings.Count(helpView, "\n")
	if height < 0 {
		height = 0
	}

	return indent.String("\n"+s+strings.Repeat("\n", height)+helpView, 2)
}
