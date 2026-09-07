package main

import (
	"net/http"
	"sort"
	"strings"
)

func (a *App) sortedCategories() []*Category {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()

	cats := make([]*Category, 0, len(a.store.Categories))
	for _, c := range a.store.Categories {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })
	return cats
}

func (a *App) handleAdminCategoryCreate(w http.ResponseWriter, r *http.Request, admin *User) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	a.store.mu.Lock()
	c := &Category{ID: newID(), Name: name}
	a.store.Categories[c.ID] = c
	a.store.save()
	a.store.mu.Unlock()
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
