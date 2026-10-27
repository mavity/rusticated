package shell

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/muesli/termenv"
)

const (
	markdownDocumentColor     = "#E6EDF3"
	markdownHeadingColor      = "#58A6FF"
	markdownTitleColor        = "#FFFFFF"
	markdownTitleBackground   = "#1F6FEB"
	markdownEmphasisColor     = "#FFD866"
	markdownStrongColor       = "#FF7B72"
	markdownLinkColor         = "#79C0FF"
	markdownLinkTextColor     = "#A5D6FF"
	markdownInlineCodeColor   = "#FFA657"
	markdownInlineCodeBack    = "#30363D"
	markdownQuoteColor        = "#8B949E"
	markdownListColor         = "#3FB950"
	markdownTableColor        = "#8B949E"
	markdownCodeColor         = "#E6EDF3"
	markdownCodeBackground    = "#161B22"
	markdownCodeKeywordColor  = "#FF7B72"
	markdownCodeStringColor   = "#A5D6FF"
	markdownCodeNumberColor   = "#79C0FF"
	markdownCodeCommentColor  = "#8B949E"
	markdownCodeFunctionColor = "#D2A8FF"
)

func renderAssistantMarkdown(source string, width int) (string, error) {
	if width < 1 {
		width = 1
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(assistantMarkdownStyles()),
		glamour.WithColorProfile(termenv.TrueColor),
		glamour.WithChromaFormatter("terminal16m"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}
	return renderer.Render(sanitizeMarkdownControls(source))
}

func assistantMarkdownStyles() ansi.StyleConfig {
	style := styles.DarkStyleConfig
	chroma := *style.CodeBlock.Chroma
	style.CodeBlock.Chroma = &chroma

	style.Document.Color = markdownColor(markdownDocumentColor)
	style.Heading.Color = markdownColor(markdownHeadingColor)
	style.H1.Color = markdownColor(markdownTitleColor)
	style.H1.BackgroundColor = markdownColor(markdownTitleBackground)
	style.H2.Color = markdownColor(markdownHeadingColor)
	style.H3.Color = markdownColor(markdownHeadingColor)
	style.H4.Color = markdownColor(markdownHeadingColor)
	style.H5.Color = markdownColor(markdownHeadingColor)
	style.H6.Color = markdownColor(markdownHeadingColor)
	style.Emph.Color = markdownColor(markdownEmphasisColor)
	style.Strong.Color = markdownColor(markdownStrongColor)
	style.Link.Color = markdownColor(markdownLinkColor)
	style.LinkText.Color = markdownColor(markdownLinkTextColor)
	style.Code.Color = markdownColor(markdownInlineCodeColor)
	style.Code.BackgroundColor = markdownColor(markdownInlineCodeBack)
	style.BlockQuote.Color = markdownColor(markdownQuoteColor)
	style.Item.Color = markdownColor(markdownListColor)
	style.Enumeration.Color = markdownColor(markdownListColor)
	style.Table.Color = markdownColor(markdownTableColor)
	style.CodeBlock.Color = markdownColor(markdownCodeColor)
	style.CodeBlock.BackgroundColor = markdownColor(markdownCodeBackground)
	chroma.Text.Color = markdownColor(markdownCodeColor)
	chroma.Keyword.Color = markdownColor(markdownCodeKeywordColor)
	chroma.KeywordReserved.Color = markdownColor(markdownCodeKeywordColor)
	chroma.LiteralString.Color = markdownColor(markdownCodeStringColor)
	chroma.LiteralStringEscape.Color = markdownColor(markdownCodeNumberColor)
	chroma.LiteralNumber.Color = markdownColor(markdownCodeNumberColor)
	chroma.Comment.Color = markdownColor(markdownCodeCommentColor)
	chroma.NameFunction.Color = markdownColor(markdownCodeFunctionColor)
	chroma.Background.BackgroundColor = markdownColor(markdownCodeBackground)
	return style
}

func markdownColor(value string) *string {
	return &value
}

func sanitizeMarkdownControls(source string) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, source)
}
