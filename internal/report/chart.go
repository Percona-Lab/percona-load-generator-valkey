package report

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
)

// Series is one line of a chart.
type Series struct {
	Name   string
	Class  string // CSS class: s1..s4
	Points []Point
}

// Point is one chart point.
type Point struct{ X, Y float64 }

// lineChart builds a self-contained inline SVG line chart. unit labels the
// y axis ("ms", "ops/s", "MiB"). It returns a note when there is no data.
func lineChart(title, unit string, series []Series) template.HTML {
	const (
		width, height          = 920.0, 260.0
		padL, padR, padT, padB = 56.0, 16.0, 16.0, 34.0
		plotW                  = width - padL - padR
		plotH                  = height - padT - padB
	)
	maxX, maxY, n := 0.0, 0.0, 0
	for _, s := range series {
		for _, p := range s.Points {
			n++
			maxX = math.Max(maxX, p.X)
			maxY = math.Max(maxY, p.Y)
		}
	}
	if n == 0 {
		return template.HTML(`<p class="muted">No data was captured for this chart.</p>`)
	}
	if maxX <= 0 {
		maxX = 1
	}
	if maxY <= 0 {
		maxY = 1
	}
	x := func(v float64) float64 { return padL + v/maxX*plotW }
	y := func(v float64) float64 { return padT + plotH - v/maxY*plotH }

	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" class="chart" role="img" aria-label="%s">`, width, height, template.HTMLEscapeString(title))
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" class="axis"/>`, padL, padT, padL, padT+plotH)
	fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" class="axis"/>`, padL, padT+plotH, padL+plotW, padT+plotH)
	for _, f := range []float64{0, 0.5, 1} {
		yy := padT + plotH - f*plotH
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" class="grid"/>`, padL, yy, padL+plotW, yy)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="tick" text-anchor="end">%s</text>`, padL-6, yy+4, tick(f*maxY))
	}
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="tick">0s</text>`, padL, padT+plotH+18)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="tick" text-anchor="end">%.0fs</text>`, padL+plotW, padT+plotH+18, maxX)
	fmt.Fprintf(&b, `<text x="%.1f" y="12" class="tick">%s</text>`, padL, template.HTMLEscapeString(unit))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		b.WriteString(`<polyline class="` + s.Class + `" points="`)
		for i, p := range s.Points {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%.1f,%.1f", x(p.X), y(p.Y))
		}
		b.WriteString(`"/>`)
	}
	b.WriteString(`</svg><div class="legend">`)
	for _, s := range series {
		fmt.Fprintf(&b, `<span class="k %s">%s</span>`, s.Class, template.HTMLEscapeString(s.Name))
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func tick(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e4:
		return fmt.Sprintf("%.0fk", v/1e3)
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 1:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}
