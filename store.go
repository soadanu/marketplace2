package main

import (
	"encoding/json"
	"os"
	"sync"
)

const dataFile = "data/db.json"

type dbSnapshot struct {
	Users          map[string]*User               `json:"users"`
	Sessions       map[string]*Session             `json:"sessions"`
	Resets         map[string]*PasswordReset       `json:"resets"`
	Applications   map[string]*SellerApplication   `json:"applications"`
	Categories     map[string]*Category            `json:"categories"`
	Listings       map[string]*Listing             `json:"listings"`
	Orders         map[string]*Order               `json:"orders"`
}

type Store struct {
	mu           sync.RWMutex
	Users        map[string]*User
	Sessions     map[string]*Session
	Resets       map[string]*PasswordReset
	Applications map[string]*SellerApplication
	Categories   map[string]*Category
	Listings     map[string]*Listing
	Orders       map[string]*Order
}

func NewStore() *Store {
	s := &Store{
		Users:        map[string]*User{},
		Sessions:     map[string]*Session{},
		Resets:       map[string]*PasswordReset{},
		Applications: map[string]*SellerApplication{},
		Categories:   map[string]*Category{},
		Listings:     map[string]*Listing{},
		Orders:       map[string]*Order{},
	}
	s.load()
	if len(s.Categories) == 0 {
		s.seedCategories()
	}
	return s
}

func (s *Store) seedCategories() {
	defaults := []string{"Electronics", "Fashion", "Home & Living", "Phones & Tablets", "Beauty & Health", "Groceries"}
	for _, name := range defaults {
		c := &Category{ID: newID(), Name: name}
		s.Categories[c.ID] = c
	}
	s.save()
}

// load reads the JSON file into memory. Missing file just means fresh start.
func (s *Store) load() {
	b, err := os.ReadFile(dataFile)
	if err != nil {
		return
	}
	var snap dbSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return
	}
	if snap.Users != nil {
		s.Users = snap.Users
	}
	if snap.Sessions != nil {
		s.Sessions = snap.Sessions
	}
	if snap.Resets != nil {
		s.Resets = snap.Resets
	}
	if snap.Applications != nil {
		s.Applications = snap.Applications
	}
	if snap.Categories != nil {
		s.Categories = snap.Categories
	}
	if snap.Listings != nil {
		s.Listings = snap.Listings
	}
	if snap.Orders != nil {
		s.Orders = snap.Orders
	}
}

// save must be called with s.mu held (or right after a write while still holding it).
func (s *Store) save() {
	snap := dbSnapshot{
		Users:        s.Users,
		Sessions:     s.Sessions,
		Resets:       s.Resets,
		Applications: s.Applications,
		Categories:   s.Categories,
		Listings:     s.Listings,
		Orders:       s.Orders,
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	tmp := dataFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	os.Rename(tmp, dataFile)
}
