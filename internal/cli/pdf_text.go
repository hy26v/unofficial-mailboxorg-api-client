// ABOUTME: A small PDF text-operator interpreter: positions of the glyphs that a page shows.
// ABOUTME: Uses the pdf package's parser and fonts; covers text state, text matrices and forms.
package cli

import (
	"math"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pdfGlyph is one glyph that a page shows: its text, its position and advance width in
// user space, and its font size.
type pdfGlyph struct {
	text    string
	x, y, w float64
	size    float64
}

// pdfMatrix is a PDF transformation matrix [a b c d e f] as a 3x3 matrix.
type pdfMatrix [3][3]float64

var pdfIdentity = pdfMatrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (m pdfMatrix) mul(n pdfMatrix) pdfMatrix {
	var out pdfMatrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				out[i][j] += m[i][k] * n[k][j]
			}
		}
	}
	return out
}

func pdfTranslate(x, y float64) pdfMatrix {
	return pdfMatrix{{1, 0, 0}, {0, 1, 0}, {x, y, 1}}
}

// pdfMatrixOf reads six numbers [a b c d e f] as a matrix.
func pdfMatrixOf(values []pdf.Value) pdfMatrix {
	return pdfMatrix{
		{values[0].Float64(), values[1].Float64(), 0},
		{values[2].Float64(), values[3].Float64(), 0},
		{values[4].Float64(), values[5].Float64(), 1},
	}
}

// pdfFont decodes the codes of one font and knows their widths in 1/1000 of the font size.
// A Type0 (composite) font uses two-byte codes, as with the Identity-H encoding that PDF
// writers use for embedded font subsets.
type pdfFont struct {
	encoding pdf.TextEncoding
	twoByte  bool
	first    int
	widths   []float64
	cid      map[int]float64
	missing  float64
}

func newPDFFont(value pdf.Value) *pdfFont {
	font := pdf.Font{V: value}
	result := &pdfFont{encoding: font.Encoder()}
	if value.Key("Subtype").Name() != "Type0" {
		result.first, result.widths = font.FirstChar(), font.Widths()
		return result
	}
	result.twoByte, result.missing, result.cid = true, 1000, map[int]float64{}
	descendant := value.Key("DescendantFonts").Index(0)
	if dw := descendant.Key("DW"); dw.Kind() == pdf.Integer || dw.Kind() == pdf.Real {
		result.missing = dw.Float64()
	}
	// W holds "c [w1 w2 ...]" (widths from code c on) and "first last w" (one width for a
	// range) entries.
	widths := descendant.Key("W")
	for i := 0; i < widths.Len() && len(result.cid) < 1<<16; {
		start := int(widths.Index(i).Int64())
		if i+1 < widths.Len() && widths.Index(i+1).Kind() == pdf.Array {
			list := widths.Index(i + 1)
			for j := 0; j < list.Len(); j++ {
				result.cid[start+j] = list.Index(j).Float64()
			}
			i += 2
			continue
		}
		if i+2 >= widths.Len() {
			break
		}
		end, width := int(widths.Index(i+1).Int64()), widths.Index(i+2).Float64()
		for code := start; code <= end && code-start < 1<<16; code++ {
			result.cid[code] = width
		}
		i += 3
	}
	return result
}

func (f *pdfFont) width(code int) float64 {
	if f.twoByte {
		if width, ok := f.cid[code]; ok {
			return width
		}
		return f.missing
	}
	if index := code - f.first; index >= 0 && index < len(f.widths) {
		return f.widths[index]
	}
	return 0
}

// pdfTextState is the graphics and text state that glyph positions depend on.
type pdfTextState struct {
	ctm, tm, tlm               pdfMatrix
	font                       *pdfFont
	size, charSpace, wordSpace float64
	scale, leading, rise       float64
}

// interpretPDFText runs one content stream. A form object that the stream draws is run
// with its own resources, up to a small depth.
func interpretPDFText(content, resources pdf.Value, state pdfTextState, depth int, glyphs *[]pdfGlyph) {
	fonts := map[string]*pdfFont{}
	fontFor := func(name string) *pdfFont {
		if font, ok := fonts[name]; ok {
			return font
		}
		var font *pdfFont
		if value := resources.Key("Font").Key(name); !value.IsNull() {
			font = newPDFFont(value)
		}
		fonts[name] = font
		return font
	}
	var stack []pdfMatrix
	show := func(raw string) {
		font := state.font
		if font == nil {
			return
		}
		step := 1
		if font.twoByte {
			step = 2
		}
		for i := 0; i+step <= len(raw); i += step {
			chunk := raw[i : i+step]
			code := int(chunk[0])
			if step == 2 {
				code = code<<8 | int(chunk[1])
			}
			w0 := font.width(code)
			trm := pdfMatrix{{state.size * state.scale, 0, 0}, {0, state.size, 0}, {0, state.rise, 1}}.mul(state.tm).mul(state.ctm)
			size := math.Hypot(trm[0][0], trm[0][1])
			*glyphs = append(*glyphs, pdfGlyph{text: font.encoding.Decode(chunk), x: trm[2][0], y: trm[2][1], w: w0 / 1000 * size, size: size})
			advance := w0/1000*state.size + state.charSpace
			if step == 1 && code == ' ' {
				advance += state.wordSpace
			}
			state.tm = pdfTranslate(advance*state.scale, 0).mul(state.tm)
		}
	}
	nextLine := func(x, y float64) {
		state.tlm = pdfTranslate(x, y).mul(state.tlm)
		state.tm = state.tlm
	}
	pdf.Interpret(content, func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		number := func(i int) float64 { return args[i].Float64() }
		switch {
		case op == "q":
			stack = append(stack, state.ctm)
		case op == "Q" && len(stack) > 0:
			state.ctm, stack = stack[len(stack)-1], stack[:len(stack)-1]
		case op == "cm" && len(args) == 6:
			state.ctm = pdfMatrixOf(args).mul(state.ctm)
		case op == "BT":
			state.tm, state.tlm = pdfIdentity, pdfIdentity
		case op == "Tf" && len(args) == 2:
			state.font, state.size = fontFor(args[0].Name()), number(1)
		case op == "Tc" && len(args) == 1:
			state.charSpace = number(0)
		case op == "Tw" && len(args) == 1:
			state.wordSpace = number(0)
		case op == "Tz" && len(args) == 1:
			state.scale = number(0) / 100
		case op == "TL" && len(args) == 1:
			state.leading = number(0)
		case op == "Ts" && len(args) == 1:
			state.rise = number(0)
		case op == "Td" && len(args) == 2:
			nextLine(number(0), number(1))
		case op == "TD" && len(args) == 2:
			state.leading = -number(1)
			nextLine(number(0), number(1))
		case op == "Tm" && len(args) == 6:
			state.tlm = pdfMatrixOf(args)
			state.tm = state.tlm
		case op == "T*":
			nextLine(0, -state.leading)
		case op == "Tj" && len(args) == 1:
			show(args[0].RawString())
		case op == "'" && len(args) == 1:
			nextLine(0, -state.leading)
			show(args[0].RawString())
		case op == "\"" && len(args) == 3:
			state.wordSpace, state.charSpace = number(0), number(1)
			nextLine(0, -state.leading)
			show(args[2].RawString())
		case op == "TJ" && len(args) == 1:
			items := args[0]
			for i := 0; i < items.Len(); i++ {
				item := items.Index(i)
				if item.Kind() == pdf.String {
					show(item.RawString())
				} else {
					state.tm = pdfTranslate(-item.Float64()/1000*state.size*state.scale, 0).mul(state.tm)
				}
			}
		case op == "Do" && len(args) == 1 && depth < 3:
			object := resources.Key("XObject").Key(args[0].Name())
			if object.Key("Subtype").Name() != "Form" {
				return
			}
			inner := state
			inner.ctm = state.ctm
			if matrix := object.Key("Matrix"); matrix.Len() == 6 {
				values := make([]pdf.Value, 6)
				for i := range values {
					values[i] = matrix.Index(i)
				}
				inner.ctm = pdfMatrixOf(values).mul(state.ctm)
			}
			formResources := object.Key("Resources")
			if formResources.IsNull() {
				formResources = resources
			}
			interpretPDFText(object, formResources, inner, depth+1, glyphs)
		}
	})
}

// pdfLines puts glyphs into lines of text. A glyph starts a new line when its baseline
// moves by more than half its size; a space goes between glyphs with a gap of more than a
// fifth of their size, or when the text goes back on the line.
func pdfLines(glyphs []pdfGlyph) string {
	var out strings.Builder
	var lastX, lastY, lastEnd float64
	started := false
	for _, glyph := range glyphs {
		if glyph.text == "" {
			continue
		}
		size := math.Max(glyph.size, 1)
		switch {
		case !started:
			started = true
		case math.Abs(glyph.y-lastY) > size/2:
			out.WriteByte('\n')
		case glyph.x < lastX-size/2 || glyph.x-lastEnd > size/5:
			out.WriteByte(' ')
		}
		out.WriteString(glyph.text)
		lastX, lastY, lastEnd = glyph.x, glyph.y, glyph.x+glyph.w
	}
	lines := strings.Split(out.String(), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.TrimSpace(blankLinesPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
