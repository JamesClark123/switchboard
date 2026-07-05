package ui

import (
	"github.com/charmbracelet/bubbles/list"
)

// listItem is the generic, richly-rendered row used by every list screen. Title
// and Description feed the bubbles DefaultDelegate; payload carries the domain
// object (a *pb.Sandbox, store.Group, …) so handlers can act on the selection.
type listItem struct {
	id      string
	host    string // owning host id (sandbox rows); empty for other screens
	title   string
	desc    string
	filter  string
	payload any
}

func (i listItem) Title() string       { return i.title }
func (i listItem) Description() string { return i.desc }
func (i listItem) FilterValue() string { // used by the "/" fuzzy filter
	if i.filter != "" {
		return i.filter
	}
	return i.title
}

// newItemList builds a themed bubbles list. The app chrome owns the header and
// help, so the list renders only its own title, items, status bar, pagination,
// and (when active) filter input.
func newItemList(title, singular, plural string, w, h int) list.Model {
	d := list.NewDefaultDelegate()
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(colAccent).BorderForeground(colAccent)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(colAccentB).BorderForeground(colAccent)
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(colText)
	d.Styles.NormalDesc = d.Styles.NormalDesc.Foreground(colMuted)
	d.Styles.DimmedTitle = d.Styles.DimmedTitle.Foreground(colMuted)
	d.Styles.FilterMatch = d.Styles.FilterMatch.Foreground(colWarn).Underline(true)

	l := list.New(nil, d, w, h)
	l.Title = title
	l.SetShowHelp(false) // chrome footer renders help
	l.SetStatusBarItemName(singular, plural)
	l.Styles.Title = l.Styles.Title.Background(colAccent).Foreground(colOnAccnt).Bold(true)
	l.Styles.NoItems = l.Styles.NoItems.Foreground(colMuted)
	return l
}

// selectByID moves the list cursor to the item with the given id, if present.
func selectByID(l *list.Model, id string) {
	for i, it := range l.Items() {
		if li, ok := it.(listItem); ok && li.id == id {
			l.Select(i)
			return
		}
	}
}
