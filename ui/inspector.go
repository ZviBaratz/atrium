package ui

// InspectorPane is the skeleton of the per-session depth surface: the tab
// exists so the strip, the jump keys and the cycling order are final, while
// the content — the measured-but-never-displayed session data — lands with
// #805. Until then it renders a designed empty state, dim and centered like
// its siblings' placeholders (the diff pane's "No changes" is the model).
type InspectorPane struct {
	width  int
	height int
}

// inspectorEmptyState is the skeleton's whole surface. It promises nothing:
// "yet" marks the blank as deliberate, not as a failed load.
const inspectorEmptyState = "Nothing to inspect yet"

// NewInspectorPane creates an empty inspector pane.
func NewInspectorPane() *InspectorPane {
	return &InspectorPane{}
}

// SetSize records the pane's content area, like its sibling panes.
func (p *InspectorPane) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// String renders the pane. Before the first SetSize it yields "", matching
// TabbedWindow.String's own zero-size guard.
func (p *InspectorPane) String() string {
	if p.width == 0 || p.height == 0 {
		return ""
	}
	return centerInBox(p.width, p.height, metaStyle().Render(inspectorEmptyState))
}
