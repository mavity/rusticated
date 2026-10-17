package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/fabric"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
)

// Rect represents a DOM element's physical bounding box in CSS pixels.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func (r Rect) Valid() bool {
	return r.W > 0 && r.H > 0
}

func (r Rect) Center() (float64, float64) {
	return r.X + r.W/2.0, r.Y + r.H/2.0
}

// NearestDistance returns the Euclidean distance between the closest points of two rectangles.
func NearestDistance(a, b Rect) float64 {
	var dx, dy float64
	if a.X+a.W < b.X {
		dx = b.X - (a.X + a.W)
	} else if b.X+b.W < a.X {
		dx = a.X - (b.X + b.W)
	}
	if a.Y+a.H < b.Y {
		dy = b.Y - (a.Y + a.H)
	} else if b.Y+b.H < a.Y {
		dy = a.Y - (b.Y + b.H)
	}
	return math.Hypot(dx, dy)
}

// TextFitsBoundsHeuristically reports whether an element's physical width and height
// approximately match its text length (plus/minus standard padding/margins).
// When true, only the element's `(x,y)` location needs to be emitted, not `WxH`.
func TextFitsBoundsHeuristically(text string, r Rect) bool {
	if !r.Valid() {
		return true
	}
	clean := strings.TrimSpace(text)
	charCount := len([]rune(clean))
	if charCount == 0 {
		return r.W <= 48 && r.H <= 48
	}
	// Typical UI font width is ~6-11px per char + up to 48px horizontal padding/icon space.
	// Single-line height is typically 14-46px; multi-line wraps proportionally.
	minExpectedW := float64(charCount)*4.5 - 16
	maxExpectedW := float64(charCount)*13.5 + 64
	if r.H >= 12 && r.H <= 48 && r.W >= minExpectedW && r.W <= maxExpectedW {
		return true
	}
	// Multi-line check: area roughly proportional to character count
	if r.W > 40 {
		estLines := math.Ceil(float64(charCount*8) / r.W)
		if estLines < 1 {
			estLines = 1
		}
		expectedH := estLines * 22.0
		if math.Abs(r.H-expectedH) <= 32.0 {
			return true
		}
	}
	return false
}

// AXNode is a trimmed, hierarchical Accessibility & DOM node.
// Note: Per design requirements:
//   - HTML tag names NEVER include angle brackets (e.g. `header`, `nav`, `section`, not `<header>`).
//   - Sibling repetition folding is NEVER performed (all 50+ data items in a list/table are preserved).
//   - Parent-child lexical text deduplication IS performed: if a parent's name/text equals its child's
//     text or contains all its children's text in lexical order, the parent's redundant text is cleared.
//   - Stable short ref IDs (`@e12`) are NOT emitted; elements are addressed naturally by role/caption/selector.
type AXNode struct {
	NodeID        string
	BackendDOMID  int64
	Role          string
	Tag           string
	Name          string
	Value         string
	Focused       bool
	Selected      bool
	Checked       string
	Disabled      bool
	Bounds        Rect
	HasBounds     bool
	FixedOrSticky bool
	Offscreen     bool
	OccludedBy    string
	Children      []*AXNode
}

// AXSnapshot holds a full trimmed AX tree, its rendered lines, and structural hashes for diffing.
type AXSnapshot struct {
	URL            string
	Title          string
	ViewportW      float64
	ViewportH      float64
	Roots          []*AXNode
	Lines          []string
	StructuralHash uint64
	NodeSignatures map[string]string // pathKey -> rendered line signature
	NodeOrder      []string
}

type rawAXValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func (v rawAXValue) String() string {
	if len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err == nil {
		return s
	}
	var b bool
	if err := json.Unmarshal(v.Value, &b); err == nil {
		if b {
			return "true"
		}
		return "false"
	}
	var n float64
	if err := json.Unmarshal(v.Value, &n); err == nil {
		return fmt.Sprintf("%v", n)
	}
	return strings.Trim(string(v.Value), `"`)
}

type rawAXProp struct {
	Name  string     `json:"name"`
	Value rawAXValue `json:"value"`
}

type rawAXNode struct {
	NodeID           string      `json:"nodeId"`
	Ignored          bool        `json:"ignored"`
	Role             rawAXValue  `json:"role"`
	Name             rawAXValue  `json:"name"`
	Value            rawAXValue  `json:"value"`
	Properties       []rawAXProp `json:"properties"`
	ChildIDs         []string    `json:"childIds"`
	BackendDOMNodeID int64       `json:"backendDOMNodeId"`
}

type domNodeMeta struct {
	Tag       string  `json:"tag"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	W         float64 `json:"w"`
	H         float64 `json:"h"`
	Fixed     bool    `json:"fixed"`
	Offscreen bool    `json:"offscreen"`
	Occluded  string  `json:"occluded"`
}

// CaptureAXSnapshot fetches the live Accessibility tree + DOM geometry metadata and applies
// the four heuristic passes:
//  1. Tag-Name fallback (without angle brackets: `header`, `nav`, `main`, etc.) & generic/none role pruning
//  2. Parent-child lexical text deduplication (never folding repeated sibling items!)
//  3. Non-obvious geometry jump detection (>240px distance) & size omission when bounds match text
//  4. Natural human-readable formatting without churn-prone `@ref` IDs.
func CaptureAXSnapshot(ctx context.Context, conn *Conn, tab *TabSession, rootSelector string) (*AXSnapshot, error) {
	sid := tab.SessionID

	var axRes struct {
		Nodes []rawAXNode `json:"nodes"`
	}
	if err := conn.Call(ctx, sid, "Accessibility.getFullAXTree", nil, &axRes); err != nil {
		return nil, err
	}

	pageMeta, domMap := fetchPageAndDOMMeta(ctx, conn, sid, rootSelector)

	roots := BuildTrimmedAXTree(axRes.Nodes, domMap, pageMeta.AllowedBackendIDs)
	lines, sigs, order := FormatAXTreeLines(roots, pageMeta.ViewportW, pageMeta.ViewportH)

	fullJoined := strings.Join(lines, "\n")
	hash := hashline.XXHash64String(fullJoined)

	snap := &AXSnapshot{
		URL:            pageMeta.URL,
		Title:          pageMeta.Title,
		ViewportW:      pageMeta.ViewportW,
		ViewportH:      pageMeta.ViewportH,
		Roots:          roots,
		Lines:          lines,
		StructuralHash: hash,
		NodeSignatures: sigs,
		NodeOrder:      order,
	}
	return snap, nil
}

type pageEvalMeta struct {
	URL               string  `json:"url"`
	Title             string  `json:"title"`
	ViewportW         float64 `json:"vw"`
	ViewportH         float64 `json:"vh"`
	AllowedBackendIDs map[int64]bool
}

func fetchPageAndDOMMeta(ctx context.Context, conn *Conn, sessionID string, rootSelector string) (pageEvalMeta, map[int64]domNodeMeta) {
	meta := pageEvalMeta{
		ViewportW: 1280,
		ViewportH: 800,
	}
	domMap := make(map[int64]domNodeMeta)

	// Query basic page URL, Title, and viewport dimensions
	var evalRes struct {
		Result struct {
			Value struct {
				URL   string  `json:"url"`
				Title string  `json:"title"`
				VW    float64 `json:"vw"`
				VH    float64 `json:"vh"`
			} `json:"value"`
		} `json:"result"`
	}
	_ = conn.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
		"expression":    `({url: location.href, title: document.title, vw: window.innerWidth || 1280, vh: window.innerHeight || 800})`,
		"returnByValue": true,
	}, &evalRes)
	if evalRes.Result.Value.URL != "" {
		meta.URL = evalRes.Result.Value.URL
	}
	meta.Title = evalRes.Result.Value.Title
	if evalRes.Result.Value.VW > 0 {
		meta.ViewportW = evalRes.Result.Value.VW
	}
	if evalRes.Result.Value.VH > 0 {
		meta.ViewportH = evalRes.Result.Value.VH
	}

	// Try DOMSnapshot.captureSnapshot for batched tag names and bounding client rects
	var snapRes struct {
		Documents []struct {
			Nodes struct {
				BackendNodeID []int64 `json:"backendNodeId"`
				NodeName      []int   `json:"nodeName"`
			} `json:"nodes"`
			Layout struct {
				NodeIndex []int       `json:"nodeIndex"`
				Bounds    [][]float64 `json:"bounds"`
			} `json:"layout"`
		} `json:"documents"`
		Strings []string `json:"strings"`
	}
	err := conn.Call(ctx, sessionID, "DOMSnapshot.captureSnapshot", map[string]any{
		"computedStyles":  []string{"position"},
		"includeDOMRects": true,
	}, &snapRes)
	if err == nil && len(snapRes.Documents) > 0 {
		doc := snapRes.Documents[0]
		strAt := func(idx int) string {
			if idx >= 0 && idx < len(snapRes.Strings) {
				return snapRes.Strings[idx]
			}
			return ""
		}
		for i, bid := range doc.Nodes.BackendNodeID {
			if bid == 0 {
				continue
			}
			tag := ""
			if i < len(doc.Nodes.NodeName) {
				tag = strings.ToLower(strings.TrimPrefix(strAt(doc.Nodes.NodeName[i]), "#"))
			}
			domMap[bid] = domNodeMeta{Tag: tag}
		}
		for i, nodeIdx := range doc.Layout.NodeIndex {
			if nodeIdx >= 0 && nodeIdx < len(doc.Nodes.BackendNodeID) && i < len(doc.Layout.Bounds) {
				bid := doc.Nodes.BackendNodeID[nodeIdx]
				b := doc.Layout.Bounds[i]
				if len(b) >= 4 {
					entry := domMap[bid]
					entry.X = b[0]
					entry.Y = b[1]
					entry.W = b[2]
					entry.H = b[3]
					entry.Offscreen = (b[1]+b[3] < 0) || (b[0]+b[2] < 0) || (b[1] > meta.ViewportH*1.5)
					domMap[bid] = entry
				}
			}
		}
	}

	return meta, domMap
}

var semanticRolesToKeep = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true, "combobox": true,
	"checkbox": true, "radio": true, "switch": true, "slider": true, "spinbutton": true,
	"menuitem": true, "menuitemcheckbox": true, "menuitemradio": true, "option": true,
	"tab": true, "tabpanel": true, "treeitem": true, "heading": true, "img": true,
	"image": true, "list": true, "listitem": true, "table": true, "row": true,
	"cell": true, "gridcell": true, "columnheader": true, "rowheader": true,
	"navigation": true, "main": true, "banner": true, "contentinfo": true,
	"complementary": true, "form": true, "region": true, "Search": true, "dialog": true,
	"alert": true, "alertdialog": true, "status": true, "progressbar": true,
	"article": true,
}

var semanticHTMLTags = map[string]bool{
	"header": true, "nav": true, "main": true, "article": true, "section": true,
	"aside": true, "footer": true, "form": true, "button": true, "a": true,
	"input": true, "select": true, "textarea": true, "label": true, "ul": true,
	"ol": true, "li": true, "table": true, "tr": true, "td": true, "th": true,
	"dialog": true, "details": true, "summary": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true,
}

// NormalizeRoleOrTag maps generic/none AX roles to clean angle-bracket-free HTML tag names.
func NormalizeRoleOrTag(role, tag string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	// Strip any accidental angle brackets
	tag = strings.ToLower(strings.Trim(strings.TrimSpace(tag), "<>"))

	switch role {
	case "", "none", "generic", "presentation", "group", "paragraph", "section":
		if semanticHTMLTags[tag] {
			return tag
		}
		if tag != "" && tag != "div" && tag != "span" && tag != "text" {
			return tag
		}
		if role == "group" {
			return "group"
		}
		return "generic"
	case "rootwebarea", "webarea":
		return "document"
	case "statictext", "inlinetextbox":
		return "text"
	case "banner":
		if tag == "header" {
			return "header"
		}
		return "banner"
	case "navigation":
		return "nav"
	case "contentinfo":
		if tag == "footer" {
			return "footer"
		}
		return "contentinfo"
	default:
		return strings.Trim(role, "<>")
	}
}

// BuildTrimmedAXTree converts raw CDP AX nodes into a pruned, deduplicated AX tree.
func BuildTrimmedAXTree(rawNodes []rawAXNode, domMap map[int64]domNodeMeta, allowedBackendIDs map[int64]bool) []*AXNode {
	if len(rawNodes) == 0 {
		return nil
	}
	byID := make(map[string]rawAXNode, len(rawNodes))
	hasParent := make(map[string]bool, len(rawNodes))
	for _, rn := range rawNodes {
		byID[rn.NodeID] = rn
		for _, cid := range rn.ChildIDs {
			hasParent[cid] = true
		}
	}

	visited := make(map[string]bool, len(rawNodes))
	var build func(id string) []*AXNode
	build = func(id string) []*AXNode {
		if visited[id] {
			return nil
		}
		visited[id] = true
		rn, ok := byID[id]
		if !ok {
			return nil
		}
		if allowedBackendIDs != nil && rn.BackendDOMNodeID != 0 && !allowedBackendIDs[rn.BackendDOMNodeID] {
			return nil
		}

		var children []*AXNode
		for _, cid := range rn.ChildIDs {
			children = append(children, build(cid)...)
		}

		if rn.Ignored {
			return children
		}

		rawRole := rn.Role.String()
		if strings.EqualFold(rawRole, "InlineTextBox") {
			return nil
		}

		dm := domMap[rn.BackendDOMNodeID]
		roleLabel := NormalizeRoleOrTag(rawRole, dm.Tag)
		name := collapseInlineSpaces(rn.Name.String())
		val := collapseInlineSpaces(rn.Value.String())

		node := &AXNode{
			NodeID:        rn.NodeID,
			BackendDOMID:  rn.BackendDOMNodeID,
			Role:          roleLabel,
			Tag:           strings.Trim(dm.Tag, "<>"),
			Name:          name,
			Value:         val,
			Children:      children,
			FixedOrSticky: dm.Fixed,
			Offscreen:     dm.Offscreen,
			OccludedBy:    dm.Occluded,
		}
		if dm.W > 0 && dm.H > 0 {
			node.Bounds = Rect{X: dm.X, Y: dm.Y, W: dm.W, H: dm.H}
			node.HasBounds = true
		}
		for _, prop := range rn.Properties {
			switch prop.Name {
			case "focused":
				node.Focused = prop.Value.String() == "true"
			case "selected":
				node.Selected = prop.Value.String() == "true"
			case "disabled":
				node.Disabled = prop.Value.String() == "true"
			case "checked":
				if v := prop.Value.String(); v != "" && v != "false" {
					node.Checked = v
				}
			}
		}

		// Pass 1: Collapse pure StaticText leaf into parent or keep if standalone
		if node.Role == "text" && len(node.Children) == 0 && node.Name == "" {
			return nil
		}

		// Pass 2: Parent-Child Lexical Text Deduplication
		// When a parent contains the same name/text as its child, or all children's texts
		// appear in the parent in lexical order, the parent's text is redundant and cleared
		// so the AI reads the text from individual children without duplication.
		// Conversely, if a leaf interactive element (button/link/heading) has a single `text`
		// child with the exact same Name, drop the redundant `text` child leaf.
		if len(node.Children) > 0 {
			if len(node.Children) == 1 && node.Children[0].Role == "text" &&
				len(node.Children[0].Children) == 0 &&
				(node.Name == node.Children[0].Name || node.Name == "") {
				if node.Name == "" {
					node.Name = node.Children[0].Name
				}
				node.Children = nil
			} else if isParentTextRedundantWithChildren(node.Name, node.Children) {
				node.Name = ""
			}
		}

		// Pass 1b: Collapse structural wrapper (`generic` or `document`) when it has no name/value
		if (node.Role == "generic" || node.Role == "document") && node.Name == "" && node.Value == "" && !node.Focused {
			if len(node.Children) == 0 {
				return nil
			}
			if len(node.Children) == 1 || node.Role == "document" || node.Role == "generic" {
				return node.Children
			}
		}

		return []*AXNode{node}
	}

	var roots []*AXNode
	for _, rn := range rawNodes {
		if !hasParent[rn.NodeID] {
			roots = append(roots, build(rn.NodeID)...)
		}
	}
	if len(roots) == 0 && len(rawNodes) > 0 {
		roots = append(roots, build(rawNodes[0].NodeID)...)
	}
	return roots
}

func collectSubtreeTexts(nodes []*AXNode) []string {
	var out []string
	var walk func(n *AXNode)
	walk = func(n *AXNode) {
		if n.Name != "" {
			out = append(out, n.Name)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}

// isParentTextRedundantWithChildren returns true when parentText is non-empty and either:
//  - matches a child's text directly, OR
//  - all non-empty descendant texts appear inside parentText in their lexical order.
func isParentTextRedundantWithChildren(parentText string, children []*AXNode) bool {
	p := collapseInlineSpaces(parentText)
	if p == "" || len(children) == 0 {
		return false
	}
	childTexts := collectSubtreeTexts(children)
	if len(childTexts) == 0 {
		return false
	}
	if len(childTexts) == 1 && strings.EqualFold(p, collapseInlineSpaces(childTexts[0])) {
		return true
	}

	// Check if every child text appears in parentText in lexical order
	lowerP := strings.ToLower(p)
	cursor := 0
	matchedChars := 0
	for _, ct := range childTexts {
		cleanC := strings.ToLower(collapseInlineSpaces(ct))
		if cleanC == "" {
			continue
		}
		idx := strings.Index(lowerP[cursor:], cleanC)
		if idx < 0 {
			return false
		}
		cursor += idx + len(cleanC)
		matchedChars += len(cleanC)
	}
	return matchedChars > 0
}

func collapseInlineSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const geometryJumpThresholdPx = 220.0

// FormatAXTreeLines renders the trimmed AXNode forest into indented lines, annotating
// non-obvious geometry jumps (returning both previous and current bounds on large jumps,
// and omitting `WxH` when the element's size matches its text heuristically).
func FormatAXTreeLines(roots []*AXNode, viewportW, viewportH float64) (lines []string, sigs map[string]string, order []string) {
	sigs = make(map[string]string)
	var prevBounds Rect
	hasPrevBounds := false
	prevLineIdx := -1
	var prevNode *AXNode

	formatGeom := func(n *AXNode, includeSize bool) string {
		if !n.HasBounds {
			return ""
		}
		b := n.Bounds
		x := int(math.Round(b.X))
		y := int(math.Round(b.Y))
		w := int(math.Round(b.W))
		h := int(math.Round(b.H))
		fitsText := TextFitsBoundsHeuristically(n.Name+" "+n.Value, b)
		if !includeSize && fitsText {
			return fmt.Sprintf("(@%d,%d)", x, y)
		}
		if fitsText {
			return fmt.Sprintf("(@%d,%d)", x, y)
		}
		return fmt.Sprintf("(@%d,%d %dx%d)", x, y, w, h)
	}

	renderNodeLine := func(n *AXNode, depth int, forceBounds bool, jumpNote string) string {
		indent := strings.Repeat("  ", depth)
		var sb strings.Builder
		sb.WriteString(indent)
		sb.WriteString(n.Role)
		if n.Name != "" {
			sb.WriteString(fmt.Sprintf(" %q", n.Name))
		}
		if n.Value != "" && n.Value != n.Name {
			sb.WriteString(fmt.Sprintf(" val=%q", n.Value))
		}
		if n.Focused {
			sb.WriteString(" [focused]")
		}
		if n.Selected {
			sb.WriteString(" [selected]")
		}
		if n.Checked != "" {
			sb.WriteString(" [checked=" + n.Checked + "]")
		}
		if n.Disabled {
			sb.WriteString(" [disabled]")
		}
		if n.FixedOrSticky {
			sb.WriteString(" [fixed]")
		}
		if n.Offscreen {
			sb.WriteString(" [offscreen]")
		}
		if n.OccludedBy != "" {
			sb.WriteString(" [occluded by " + n.OccludedBy + "]")
		}
		if forceBounds && n.HasBounds {
			sb.WriteString(" " + formatGeom(n, false))
		}
		if jumpNote != "" {
			sb.WriteString(" " + jumpNote)
		}
		return sb.String()
	}

	type nodeMeta struct {
		node     *AXNode
		depth    int
		pathKey  string
		forceBnd bool
		jumpNote string
	}
	var flat []nodeMeta
	siblingCounts := make(map[string]int)

	var walk func(nodes []*AXNode, depth int, parentPath string)
	walk = func(nodes []*AXNode, depth int, parentPath string) {
		for _, n := range nodes {
			baseKey := parentPath + "/" + n.Role + ":" + n.Name
			siblingCounts[baseKey]++
			pathKey := fmt.Sprintf("%s#%d", baseKey, siblingCounts[baseKey])

			forceBnd := false
			jumpNote := ""
			if n.HasBounds {
				if hasPrevBounds {
					dist := NearestDistance(prevBounds, n.Bounds)
					if dist > geometryJumpThresholdPx {
						forceBnd = true
						if prevLineIdx >= 0 && prevLineIdx < len(flat) {
							flat[prevLineIdx].forceBnd = true
						}
						dy := int(math.Round(n.Bounds.Y - prevBounds.Y))
						if dy <= -120 {
							jumpNote = fmt.Sprintf("[jump %dpx up]", dy)
						} else if dist >= 350 {
							jumpNote = fmt.Sprintf("[jump +%dpx]", int(math.Round(dist)))
						}
					}
				} else {
					forceBnd = true
				}
				prevBounds = n.Bounds
				hasPrevBounds = true
				prevNode = n
				_ = prevNode
				prevLineIdx = len(flat)
			}

			flat = append(flat, nodeMeta{
				node:     n,
				depth:    depth,
				pathKey:  pathKey,
				forceBnd: forceBnd,
				jumpNote: jumpNote,
			})
			walk(n.Children, depth+1, pathKey)
		}
	}
	walk(roots, 0, "")

	lines = make([]string, 0, len(flat))
	order = make([]string, 0, len(flat))
	for _, item := range flat {
		line := renderNodeLine(item.node, item.depth, item.forceBnd, item.jumpNote)
		lines = append(lines, line)
		sigs[item.pathKey] = strings.TrimSpace(line)
		order = append(order, item.pathKey)
	}
	return lines, sigs, order
}

// DiffAXSnapshots compares `before` and `after` AX snapshots.
//   - If `before` is nil, URL changed, or >=35% of nodes changed, it emits `[full_page_change]`
//     followed by the full trimmed AX tree.
//   - If <35% of nodes changed, it emits only the added/updated lines anchored by context.
func DiffAXSnapshots(nickname string, settleMs int64, before, after *AXSnapshot) string {
	if after == nil {
		return ""
	}
	if before == nil || len(before.Lines) == 0 || (before.URL != "" && after.URL != "" && before.URL != after.URL) {
		hdr := fmt.Sprintf("[page on tab %q | url: %s | settled in %dms]", nickname, after.URL, settleMs)
		if len(after.Lines) == 0 {
			return hdr + "\n  (empty document)"
		}
		return hdr + "\n" + strings.Join(after.Lines, "\n")
	}

	changed := 0
	var diffLines []string
	for i, key := range after.NodeOrder {
		afterSig := after.NodeSignatures[key]
		beforeSig, existed := before.NodeSignatures[key]
		if !existed || beforeSig != afterSig {
			changed++
			if i < len(after.Lines) {
				prefix := "+ "
				if existed {
					prefix = "~ "
				}
				diffLines = append(diffLines, prefix+after.Lines[i])
			}
		}
	}
	removed := 0
	for key := range before.NodeSignatures {
		if _, stillThere := after.NodeSignatures[key]; !stillThere {
			removed++
		}
	}

	totalDenom := len(after.NodeOrder)
	if len(before.NodeOrder) > totalDenom {
		totalDenom = len(before.NodeOrder)
	}
	if totalDenom == 0 {
		return fmt.Sprintf("[ax_diff on tab %q | settled in %dms | no changes]", nickname, settleMs)
	}

	changeRatio := float64(changed+removed) / float64(totalDenom)
	if changed == 0 && removed == 0 {
		return fmt.Sprintf("[ax_diff on tab %q | settled in %dms | 0 nodes changed]", nickname, settleMs)
	}
	if changeRatio >= 0.35 {
		hdr := fmt.Sprintf("[full_page_change on tab %q | url: %s | settled in %dms | %d/%d nodes updated]",
			nickname, after.URL, settleMs, changed+removed, totalDenom)
		return hdr + "\n" + strings.Join(after.Lines, "\n")
	}

	hdr := fmt.Sprintf("[ax_diff on tab %q | settled in %dms | %d updated, %d removed]",
		nickname, settleMs, changed, removed)
	return hdr + "\n" + strings.Join(diffLines, "\n")
}

// SearchAXSnapshot filters the AXSnapshot lines using Kabibi's 4-tier progressive search funnel
// (Exact -> Regex -> Fuzzy -> EmbeddingGemma 2 semantic embeddings) with indentation fabric expansion.
func SearchAXSnapshot(snap *AXSnapshot, query string, embedder ai.PartEmbedder) string {
	if snap == nil || len(snap.Lines) == 0 {
		return "(empty AX tree)"
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return strings.Join(snap.Lines, "\n")
	}
	res := fabric.SearchAndExpand(
		"browser_ax:"+snap.URL,
		int64(snap.StructuralHash),
		snap.Lines,
		q,
		"auto",
		embedder,
		fabric.DefaultTotalBudget,
	)
	if len(res.Ranges) == 0 {
		return fmt.Sprintf("[0 AX matches for %q on %s]", q, snap.URL)
	}
	var out []string
	out = append(out, fabric.FormatMatchNote(len(snap.Lines), res, q))
	for idx, r := range res.Ranges {
		if idx > 0 {
			out = append(out, "…")
		}
		for ln := r.Start; ln <= r.End && ln <= len(snap.Lines); ln++ {
			out = append(out, snap.Lines[ln-1])
		}
	}
	return strings.Join(out, "\n")
}
