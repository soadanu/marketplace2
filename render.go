package main

import (
	"html/template"
	"log"
	"net/http"
	"path/filepath"
)

var templates *template.Template

func loadTemplates() {
	funcs := template.FuncMap{
		"naira": func(kobo int) string {
			return formatNaira(kobo)
		},
		"basename": func(path string) string {
			if path == "" {
				return ""
			}
			return filepath.Base(path)
		},
	}
	templates = template.Must(template.New("").Funcs(funcs).ParseGlob("templates/*.html"))
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error (%s): %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func formatNaira(kobo int) string {
	naira := kobo / 100
	remainder := kobo % 100
	if remainder == 0 {
		return "₦" + addThousandsSep(naira)
	}
	return "₦" + addThousandsSep(naira) + "." + twoDigits(remainder)
}

func addThousandsSep(n int) string {
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if len(digits) == 0 {
		digits = []byte{'0'}
	}
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			s += ","
		}
		s += string(d)
	}
	if neg {
		s = "-" + s
	}
	return s
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
