// Package svgsafe = sanitasi SVG denah dengan allowlist (docs/12 §5: SVG harus bebas script/handler/foreignObject).
// Memakai penguraian XML token demi token lalu menulis ulang hanya elemen & atribut yang diizinkan.
package svgsafe

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var allowedElems = map[string]bool{
	"svg": true, "g": true, "path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "text": true, "tspan": true, "title": true, "desc": true, "defs": true,
}

var allowedAttrs = map[string]bool{
	"id": true, "class": true, "d": true, "x": true, "y": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"x1": true, "y1": true, "x2": true, "y2": true, "width": true, "height": true, "fill": true, "stroke": true,
	"stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "stroke-dasharray": true, "transform": true,
	"points": true, "viewBox": true, "preserveAspectRatio": true, "xmlns": true, "version": true, "font-size": true,
	"font-family": true, "font-weight": true, "text-anchor": true, "dominant-baseline": true, "opacity": true,
	"fill-opacity": true, "stroke-opacity": true, "role": true, "aria-label": true, "tabindex": true, "fill-rule": true,
}

var (
	// nilai atribut yang berbahaya (url() ke luar, javascript:, data:, expression)
	badValue = regexp.MustCompile(`(?i)(javascript:|data:|vbscript:|expression\s*\(|url\s*\(\s*['"]?\s*(https?:|//|data:|javascript:))`)
	dataAttr = regexp.MustCompile(`^data-[a-z0-9_-]{1,40}$`)
)

var (
	ErrEmpty    = errors.New("svg kosong")
	ErrNotSVG   = errors.New("root bukan <svg>")
	ErrTooLarge = errors.New("svg terlalu besar")
	ErrDoctype  = errors.New("DOCTYPE/entity tidak diizinkan")
)

const MaxBytes = 2 << 20 // 2 MiB

// Sanitize mengembalikan SVG aman atau error. Elemen/atribut di luar allowlist DIBUANG (bukan ditoleransi);
// <script>, <foreignObject>, <style>, <use>, <image>, event handler (on*), dan href dibuang beserta isinya.
func Sanitize(in []byte) ([]byte, error) {
	if len(bytes.TrimSpace(in)) == 0 {
		return nil, ErrEmpty
	}
	if len(in) > MaxBytes {
		return nil, ErrTooLarge
	}
	if bytes.Contains(bytes.ToUpper(in), []byte("<!ENTITY")) || bytes.Contains(bytes.ToUpper(in), []byte("<!DOCTYPE")) {
		return nil, ErrDoctype
	}
	dec := xml.NewDecoder(bytes.NewReader(in))
	dec.Strict = true
	dec.Entity = nil
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	depth, skip := 0, 0 // skip>0: sedang di dalam elemen terlarang → buang semuanya
	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("svg tidak valid: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if skip > 0 {
				skip++
				continue
			}
			if !sawRoot {
				if name != "svg" {
					return nil, ErrNotSVG
				}
				sawRoot = true
			}
			if !allowedElems[name] {
				skip = 1
				continue
			}
			attrs := make([]xml.Attr, 0, len(t.Attr))
			for _, a := range t.Attr {
				an := a.Name.Local
				if a.Name.Space != "" && a.Name.Space != "xmlns" && an != "xmlns" {
					continue // buang atribut ber-namespace (xlink:href dll.)
				}
				lower := strings.ToLower(an)
				if strings.HasPrefix(lower, "on") || lower == "href" || lower == "style" {
					continue
				}
				if !allowedAttrs[an] && !dataAttr.MatchString(lower) {
					continue
				}
				if badValue.MatchString(a.Value) {
					continue
				}
				if an == "xmlns" && a.Value != "http://www.w3.org/2000/svg" {
					continue
				}
				attrs = append(attrs, xml.Attr{Name: xml.Name{Local: an}, Value: a.Value})
			}
			if name == "svg" && depth == 0 {
				hasNS := false
				for _, a := range attrs {
					if a.Name.Local == "xmlns" {
						hasNS = true
					}
				}
				if !hasNS {
					attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
				}
			}
			depth++
			if err := enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs}); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			depth--
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, err
			}
		case xml.CharData:
			if skip > 0 {
				continue
			}
			if depth > 0 {
				if err := enc.EncodeToken(xml.CharData(t)); err != nil {
					return nil, err
				}
			}
		case xml.Directive, xml.ProcInst, xml.Comment:
			// dibuang
		}
	}
	if !sawRoot {
		return nil, ErrNotSVG
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
